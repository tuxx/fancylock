package internal

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"
	ext "github.com/tuxx/wayland-ext-session-lock-go"
	"golang.org/x/sys/unix"
)

//go:embed fonts/DejaVuSans-Bold.ttf
var fontBytes []byte

type WaylandLocker struct {
	display         *wl.Display
	registry        *wl.Registry
	registryHandler *RegistryHandler
	compositor      *wl.Compositor
	lockManager     *ext.SessionLockManager
	lock            *ext.SessionLock
	keyboard        *wl.Keyboard
	seat            *wl.Seat
	shm             *wl.Shm
	securePassword  *SecurePassword
	surfaces        map[*wl.Output]struct {
		wlSurface   *wl.Surface
		lockSurface *ext.SessionLockSurface
	}
	redrawCh        chan int
	outputs         map[uint32]*wl.Output
	mediaPlayer     *MediaPlayer
	done            chan struct{}
	config          Configuration
	helper          *LockHelper
	lockActive      bool
	countdownActive bool
	failedAttempts  int
	lockoutUntil    time.Time
	lockoutActive   bool
	lastFailureTime time.Time
	mu              sync.Mutex
}

var _ wl.KeyboardKeyHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardEnterHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardLeaveHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardKeymapHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardModifiersHandler = (*WaylandLocker)(nil)

type surfaceHandler struct {
	client      *WaylandLocker
	surface     *wl.Surface
	lockSurface *ext.SessionLockSurface
}

func (h *surfaceHandler) HandleSessionLockSurfaceConfigure(ev ext.SessionLockSurfaceConfigureEvent) {
	Info("Surface configure: serial=%d, width=%d, height=%d\n", ev.Serial, ev.Width, ev.Height)

	// Acknowledge the configure
	h.lockSurface.AckConfigure(ev.Serial)
	Debug("Acknowledged configure")

	// Create a shared memory buffer for the surface
	stride := int(ev.Width) * 4
	size := stride * int(ev.Height)

	// Create memory-backed file descriptor
	fd, err := unix.MemfdCreate("buffer", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd: %v", err)
		return
	}
	defer unix.Close(fd) // Ensure fd is closed on all exit paths

	// Set the size of the file
	if err = syscall.Ftruncate(fd, int64(size)); err != nil {
		Error("Failed to truncate memfd: %v", err)
		return
	}

	// Map the file into memory
	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to mmap: %v", err)
		return
	}
	defer syscall.Munmap(data)

	// Fill with black transparent color (RGBA format)
	for i := 0; i < size; i += 4 {
		data[i+0] = 0 // Blue
		data[i+1] = 0 // Green
		data[i+2] = 0 // Red
		data[i+3] = 0 // Alpha (fully transparent)
	}

	pool, err := h.client.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create pool: %v", err)
		return
	}

	// Create a buffer from the pool
	buffer, err := pool.CreateBuffer(0, int32(ev.Width), int32(ev.Height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer: %v", err)
		// Explicitly destroy the pool as it's no longer needed if buffer creation fails
		pool.Destroy()
		return
	}

	// Attach buffer to surface and commit
	h.surface.Attach(buffer, 0, 0)
	h.surface.Damage(0, 0, int32(ev.Width), int32(ev.Height))
	h.surface.SetInputRegion(nil)
	h.surface.Commit()

	Info("Created %dx%d buffer with transparent background and committed surface\n", ev.Width, ev.Height)
}

func NewWaylandLocker(config Configuration) *WaylandLocker {
	InitLogger(LevelDebug, true)
	Debug("WaylandLocker logger initialized")

	return &WaylandLocker{
		display: nil,
		surfaces: make(map[*wl.Output]struct {
			wlSurface   *wl.Surface
			lockSurface *ext.SessionLockSurface
		}),
		outputs:         make(map[uint32]*wl.Output),
		done:            make(chan struct{}),
		redrawCh:        make(chan int, 1),
		config:          config,
		helper:          NewLockHelper(config),
		lockActive:      false,
		mediaPlayer:     NewMediaPlayer(config),
		failedAttempts:  0,
		lockoutActive:   false,
		countdownActive: false,
		securePassword:  NewSecurePassword(), // Initialize the new securePassword field
	}
}

func (l *WaylandLocker) StartIdleMonitor() error {
	return nil
}

// Handle keyboard enter events
func (l *WaylandLocker) HandleKeyboardEnter(ev wl.KeyboardEnterEvent) {
	Info("Keyboard enter event received: surface=%d, keys=%v\n", ev.Surface.Id(), ev.Keys)
}

// Handle keyboard leave events
func (l *WaylandLocker) HandleKeyboardLeave(ev wl.KeyboardLeaveEvent) {
	Info("Keyboard leave event received: surface=%d\n", ev.Surface.Id())
}

// Handle keyboard keymap events
func (l *WaylandLocker) HandleKeyboardKeymap(ev wl.KeyboardKeymapEvent) {
	Info("Keyboard keymap event received: format=%d, size=%d\n", ev.Format, ev.Size)
}

// Handle keyboard modifier events
func (l *WaylandLocker) HandleKeyboardModifiers(ev wl.KeyboardModifiersEvent) {
	Info("Keyboard modifiers event received: mods=%d,%d,%d\n",
		ev.ModsDepressed, ev.ModsLatched, ev.ModsLocked)
}

func drawPasswordFeedback(l *WaylandLocker, surface *wl.Surface, count int, offsetX int) {
	width := uint32(3840)
	height := uint32(2160)
	stride := int(width) * 4
	size := stride * int(height)

	fd, err := unix.MemfdCreate("pwfeedback", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memory file descriptor: %v", err)
		return
	}
	defer unix.Close(fd) // Ensure fd is always closed

	err = syscall.Ftruncate(fd, int64(size))
	if err != nil {
		Error("Failed to truncate memory file: %v", err)
		return
	}

	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to map memory: %v", err)
		return
	}
	defer syscall.Munmap(data) // Ensure memory mapping is cleaned up

	// Much larger dots
	dotSpacing := 40 // Increased spacing for larger dots
	dotRadius := 12  // 4x the original size (was 3)
	totalWidth := count * dotSpacing
	startX := (int(width)-totalWidth)/2 + offsetX
	y := int(height) - 100

	for i := 0; i < count && i < 32; i++ {
		x := startX + i*dotSpacing
		// Draw larger circular dots
		for dy := -dotRadius; dy <= dotRadius; dy++ {
			for dx := -dotRadius; dx <= dotRadius; dx++ {
				// Make sure we're within the circle
				if dx*dx+dy*dy <= dotRadius*dotRadius {
					px := x + dx
					py := y + dy
					if px >= 0 && py >= 0 && px < int(width) && py < int(height) {
						offset := (py*int(width) + px) * 4
						data[offset+0] = 0xff // Blue
						data[offset+1] = 0xff // Green
						data[offset+2] = 0xff // Red
						data[offset+3] = 0xff // Alpha
					}
				}
			}
		}
	}

	pool, err := l.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create shared memory pool: %v", err)
		return
	}

	buffer, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer: %v", err)
		pool.Destroy()
		return
	}

	surface.Attach(buffer, 0, 0)
	surface.Damage(0, 0, int32(width), int32(height))
	surface.SetInputRegion(nil)
	surface.Commit()
}

func (l *WaylandLocker) shakePasswordDots() {
	Debug("Starting password shake animation")

	// Number of shake iterations
	iterations := 4
	// Shake distance in pixels
	distance := 10
	// Time between movements in milliseconds
	delay := 80 * time.Millisecond

	// Get current dot count safely
	dotCount := l.securePassword.Length()

	// Perform the shake animation with horizontal movement
	for i := 0; i < iterations; i++ {
		// Move right
		for _, entry := range l.surfaces {
			if entry.wlSurface != nil {
				drawPasswordFeedback(l, entry.wlSurface, dotCount, distance)
			}
		}
		time.Sleep(delay)

		// Move left
		for _, entry := range l.surfaces {
			if entry.wlSurface != nil {
				drawPasswordFeedback(l, entry.wlSurface, dotCount, -distance)
			}
		}
		time.Sleep(delay)

		// Back to center
		for _, entry := range l.surfaces {
			if entry.wlSurface != nil {
				drawPasswordFeedback(l, entry.wlSurface, dotCount, 0)
			}
		}
		time.Sleep(delay)
	}

	// Final redraw with no dots
	for _, entry := range l.surfaces {
		if entry.wlSurface != nil {
			drawPasswordFeedback(l, entry.wlSurface, 0, 0)
		}
	}
}

func (l *WaylandLocker) HandleKeyboardKey(ev wl.KeyboardKeyEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if ev.State != 1 {
		return
	}

	if l.countdownActive {
		// Only handle key if it's ESC and debug exit is enabled
		if ev.Key == 1 && l.config.DebugExit {
			Info("ESC pressed during countdown, triggering debug exit\n")
			if l.lock != nil {
				l.lock.UnlockAndDestroy()
			}
			close(l.done)
		}

		// Ignore all other keys while countdown is active
		return
	}

	charAdded := false

	switch ev.Key {
	case 1: // Escape
		Info("ESC pressed, clearing password\n")
		l.securePassword.Clear()
		charAdded = true
		if l.config.DebugExit {
			Info("Debug exit triggered by ESC key\n")
			if l.lock != nil {
				l.lock.UnlockAndDestroy()
			}
			close(l.done)
			return
		}
	case 28, 96, 108, 65: // Enter
		Info("ENTER key detected (code=%d), authenticating\n", ev.Key)
		l.authenticate()
		return
	case 14: // Backspace
		Info("BACKSPACE pressed, removing last character\n")
		l.securePassword.RemoveLast()
		charAdded = true
	default:
		if ev.Key >= 2 && ev.Key <= 11 {
			if ev.Key == 11 {
				l.securePassword.Append('0')
			} else {
				l.securePassword.Append('1' + byte(ev.Key-2))
			}
			charAdded = true
		} else if ev.Key >= 16 && ev.Key <= 25 {
			chars := []byte{'q', 'w', 'e', 'r', 't', 'y', 'u', 'i', 'o', 'p'}
			l.securePassword.Append(chars[ev.Key-16])
			charAdded = true
		} else if ev.Key >= 30 && ev.Key <= 38 {
			chars := []byte{'a', 's', 'd', 'f', 'g', 'h', 'j', 'k', 'l'}
			l.securePassword.Append(chars[ev.Key-30])
			charAdded = true
		} else if ev.Key >= 44 && ev.Key <= 50 {
			chars := []byte{'z', 'x', 'c', 'v', 'b', 'n', 'm'}
			l.securePassword.Append(chars[ev.Key-44])
			charAdded = true
		} else {
			Info("Unhandled key: %d\n", ev.Key)
		}
	}

	if charAdded {
		select {
		case l.redrawCh <- l.securePassword.Length():
		default:
		}
	}
}

func (l *WaylandLocker) HandleSessionLockLocked(ev ext.SessionLockLockedEvent) {
	Info("Session is now locked! Lock is active.\n")
	l.lockActive = true
}

func (l *WaylandLocker) HandleSessionLockFinished(ev ext.SessionLockFinishedEvent) {
	Info("Lock manager finished the session lock. Was active? %v\n", l.lockActive)

	// Make sure media player is stopped
	if l.mediaPlayer != nil {
		Info("Ensuring media player is stopped during session lock finish")
		l.mediaPlayer.Stop()
	}

	if !l.lockActive {
		Info("Lock failed to activate before finishing\n")
	}

	// Signal that we're done
	close(l.done)
}

type outputInfo struct {
	x, y   int
	width  int
	height int
}

type RegistryHandler struct {
	wl.OutputGeometryHandler
	wl.OutputModeHandler

	registry         *wl.Registry
	compositor       *wl.Compositor
	lockManager      *ext.SessionLockManager
	seat             *wl.Seat
	shm              *wl.Shm
	outputs          map[uint32]*wl.Output
	outputGeometries map[*wl.Output]outputInfo
}

type handlerFunc func(wl.OutputGeometryEvent)

func (f handlerFunc) HandleOutputGeometry(ev wl.OutputGeometryEvent) { f(ev) }

type outputModeHandlerFunc func(ev wl.OutputModeEvent)

func (f outputModeHandlerFunc) HandleOutputMode(ev wl.OutputModeEvent) {
	f(ev)
}

func (h *RegistryHandler) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		h.compositor = wlclient.RegistryBindCompositorInterface(h.registry, ev.Name, ev.Version)
		Debug("Bound wl_compositor")
	case "wl_seat":
		h.seat = wlclient.RegistryBindSeatInterface(h.registry, ev.Name, ev.Version)
		Debug("Bound wl_seat")
	case "wl_shm":
		h.shm = wlclient.RegistryBindShmInterface(h.registry, ev.Name, ev.Version)
		Debug("Bound wl_shm")
	case "ext_session_lock_manager_v1":
		// Use the specialized binding function from the ext package
		h.lockManager = ext.BindSessionLockManager(h.registry, ev.Name, 1)
		Debug("Bound ext_session_lock_manager_v1")
	case "wl_output":
		output := wlclient.RegistryBindOutputInterface(h.registry, ev.Name, ev.Version)
		h.outputs[ev.Name] = output
		Debug("Bound wl_output")

		// Add geometry handler
		if h.outputGeometries == nil {
			h.outputGeometries = make(map[*wl.Output]outputInfo)
		}

		// Add handlers for output geometry and mode
		if h.outputGeometries == nil {
			h.outputGeometries = make(map[*wl.Output]outputInfo)
		}

		output.AddGeometryHandler(struct{ wl.OutputGeometryHandler }{
			OutputGeometryHandler: handlerFunc(func(ev wl.OutputGeometryEvent) {
				info := h.outputGeometries[output]
				info.x = int(ev.X)
				info.y = int(ev.Y)
				h.outputGeometries[output] = info
			}),
		})

		output.AddModeHandler(outputModeHandlerFunc(func(ev wl.OutputModeEvent) {
			if ev.Flags&wl.OutputModeCurrent != 0 {
				info := h.outputGeometries[output]
				info.width = int(ev.Width)
				info.height = int(ev.Height)
				h.outputGeometries[output] = info
			}
		}))
	}
}

func (l *WaylandLocker) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	Debug("Registry global event: name=%d interface=%s version=%d", ev.Name, ev.Interface, ev.Version)

	switch ev.Interface {
	case "wl_compositor":
		Debug("Found wl_compositor interface")
		l.compositor = wlclient.RegistryBindCompositorInterface(l.registry, ev.Name, 4)
		Debug("Bound wl_compositor interface")
	case "ext_session_lock_manager_v1":
		Debug("Found ext_session_lock_manager_v1 interface")
		l.lockManager = ext.BindSessionLockManager(l.registry, ev.Name, 1)
		Debug("Bound lock manager interface")
	case "wl_output":
		Debug("Found wl_output interface")
		output := wlclient.RegistryBindOutputInterface(l.registry, ev.Name, 3)
		l.outputs[ev.Name] = output
		Debug("Added output %d to outputs map", ev.Name)
	case "wl_shm":
		Debug("Found wl_shm interface")
		l.shm = wlclient.RegistryBindShmInterface(l.registry, ev.Name, 1)
		Debug("Bound wl_shm interface")
	case "wl_seat":
		Debug("Found wl_seat interface")
		l.seat = wlclient.RegistryBindSeatInterface(l.registry, ev.Name, 7)
		Debug("Bound wl_seat interface")
		l.seat.AddCapabilitiesHandler(l)
		Debug("Added capabilities handler to seat")
		wlclient.DisplayRoundtrip(l.display)
		Debug("Seat capabilities roundtrip completed")
	default:
		Debug("Ignoring interface: %s", ev.Interface)
	}
}

func (l *WaylandLocker) HandleSeatCapabilities(ev wl.SeatCapabilitiesEvent) {
	Debug("Seat capabilities: %d", ev.Capabilities)

	// If the seat now has a keyboard capability, initialize the keyboard
	if ev.Capabilities&wl.SeatCapabilityKeyboard != 0 {
		Debug("Keyboard capability detected")

		// Only set up the keyboard if it's not already set up
		if l.keyboard == nil {
			Debug("Setting up keyboard input...")

			// Get the keyboard object from the seat
			keyboard, err := l.seat.GetKeyboard()
			if err != nil {
				Error("Failed to get keyboard: %v", err)
				return
			}

			Debug("Keyboard obtained, adding event handlers...")

			// Assign the keyboard to the locker
			l.keyboard = keyboard

			// Add handlers for key events
			l.keyboard.AddKeyHandler(l)
			l.keyboard.AddEnterHandler(l)
			l.keyboard.AddLeaveHandler(l)
			l.keyboard.AddKeymapHandler(l)
			l.keyboard.AddModifiersHandler(l)

			Debug("Keyboard handlers added successfully")
		}
	} else {
		// If the keyboard capability is removed, handle accordingly
		if l.keyboard != nil {
			Debug("Keyboard capability removed")
			l.keyboard = nil
		}
	}
}

func (l *WaylandLocker) Lock() error {
	Debug("\n=== LOCK FUNCTION STARTED ===")
	var err error

	// Run pre-lock command if configured
	if err := l.helper.RunPreLockCommand(); err != nil {
		Warn("Pre-lock command error: %v", err)
		// Continue with locking even if the pre-lock command fails
	}

	// 1. Connect to Wayland display
	l.display, err = wlclient.DisplayConnect(nil)
	if err != nil {
		Debug("ERROR: Failed to connect to Wayland display")
		return fmt.Errorf("connect to Wayland: %w", err)
	}
	Debug("OK: Connected to Wayland display")

	// 2. Get registry and set up registry handler
	l.registry, err = l.display.GetRegistry()
	if err != nil {
		Debug("ERROR: Failed to get registry")
		return fmt.Errorf("get registry: %w", err)
	}
	Debug("OK: Got registry")

	// Use a separate registry handler
	regHandler := &RegistryHandler{
		registry:         l.registry,
		outputs:          make(map[uint32]*wl.Output),
		outputGeometries: make(map[*wl.Output]outputInfo),
	}
	l.registryHandler = regHandler

	// Add the registry handler
	l.registry.AddGlobalHandler(regHandler)

	// Process registry events
	err = wlclient.DisplayRoundtrip(l.display)
	if err != nil {
		Debug("ERROR: Registry roundtrip failed")
		return fmt.Errorf("registry roundtrip: %w", err)
	}
	Debug("OK: Registry roundtrip completed")

	// Copy registry handler values to locker
	l.compositor = regHandler.compositor
	l.lockManager = regHandler.lockManager
	l.shm = regHandler.shm
	l.seat = regHandler.seat
	for id, output := range regHandler.outputs {
		l.outputs[id] = output
	}

	// Check required interfaces
	if l.compositor == nil {
		Debug("ERROR: No compositor found")
		return fmt.Errorf("no compositor found")
	}
	if l.shm == nil {
		Debug("ERROR: No shared memory manager found")
		return fmt.Errorf("no shared memory manager found")
	}
	if l.lockManager == nil {
		Debug("ERROR: No session lock manager found")
		return fmt.Errorf("no session lock manager found")
	}

	// 3. Create session lock with extra safety
	Debug("Creating session lock...")
	Debug("Lock manager details: %+v", l.lockManager)
	Debug("Lock manager ID: %d", l.lockManager.Id())

	// Use panic recovery to handle potential issues
	var lockErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				lockErr = fmt.Errorf("panic in lock creation: %v", r)
				Debug("Recovered from panic in lock creation: %v", r)
			}
		}()
		l.lock, err = l.lockManager.Lock()
		if err != nil {
			lockErr = err
		}
	}()

	if lockErr != nil {
		Debug("ERROR: Failed to create session lock: %v", lockErr)
		return fmt.Errorf("failed to create session lock: %v", lockErr)
	}

	Debug("OK: Created session lock with ID: %d", l.lock.Id())

	// 4. Add lock listener
	ext.SessionLockAddListener(l.lock, l)
	Debug("OK: Added session lock listener")

	err = wlclient.DisplayRoundtrip(l.display)
	if err != nil {
		Debug("ERROR: Lock roundtrip failed")
		return fmt.Errorf("lock roundtrip failed: %w", err)
	}

	// 5. Create surfaces for each output
	Info("Creating %d lock surfaces...\n", len(l.outputs))
	for id, output := range l.outputs {
		Info("Creating surface for output %d...\n", id)

		// Create surface with error handling
		s, aerr := l.compositor.CreateSurface()
		if aerr != nil {
			Debug("ERROR: Failed to create surface for output %d: %v", id, aerr)
			return fmt.Errorf("failed to create surface: %w", aerr)
		}
		Info("Surface created with ID: %d\n", s.Id())

		// Create lock surface with error handling
		var lockSurface *ext.SessionLockSurface
		var berr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					berr = fmt.Errorf("panic in lock surface creation: %v", r)
					Debug("Recovered from panic in lock surface creation: %v", r)
				}
			}()
			lockSurface, berr = l.lock.GetLockSurface(s, output)
		}()

		if berr != nil {
			Debug("ERROR: Failed to get lock surface for output %d: %v", id, berr)
			return fmt.Errorf("failed to get lock surface: %w", berr)
		}
		Info("Lock surface created with ID: %d\n", lockSurface.Id())

		// Add listener
		ext.SessionLockSurfaceAddListener(lockSurface, &surfaceHandler{
			client:      l,
			surface:     s,
			lockSurface: lockSurface,
		})

		l.surfaces[output] = struct {
			wlSurface   *wl.Surface
			lockSurface *ext.SessionLockSurface
		}{
			wlSurface:   s,
			lockSurface: lockSurface,
		}

		Info("OK: Created lock surface for output %d\n", id)
	}

	// 6. Process surfaces with a roundtrip
	err = wlclient.DisplayRoundtrip(l.display)
	if err != nil {
		Debug("ERROR: Surfaces roundtrip failed")
		return fmt.Errorf("surfaces roundtrip failed: %w", err)
	}

	// NOW set up monitors for media player AFTER lock is established
	Debug("Setting up media player after lock is established...")
	var monitors []Monitor
	for _, output := range l.registryHandler.outputs {
		info, ok := l.registryHandler.outputGeometries[output]
		if !ok {
			continue
		}
		monitors = append(monitors, Monitor{
			X:      info.x,
			Y:      info.y,
			Width:  info.width,
			Height: info.height,
		})
	}

	l.mediaPlayer.SetMonitors(monitors)
	if merr := l.mediaPlayer.Start(); merr != nil {
		Warn("Failed to start media player: %v", merr)
	}

	// 7. NOW set up keyboard handlers (after surfaces are set up)
	Debug("Setting up keyboard handlers...")
	if l.seat != nil {
		var keyboard *wl.Keyboard
		var kberr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					kberr = fmt.Errorf("panic in keyboard setup: %v", r)
					Debug("Recovered from panic in keyboard setup: %v", r)
				}
			}()
			keyboard, kberr = l.seat.GetKeyboard()
		}()

		if kberr != nil {
			Debug("WARNING: Failed to get keyboard: %v", kberr)
			// Continue without keyboard - not fatal
		} else if keyboard != nil {
			l.keyboard = keyboard
			l.keyboard.AddKeyHandler(l)
			l.keyboard.AddEnterHandler(l)
			l.keyboard.AddLeaveHandler(l)
			l.keyboard.AddKeymapHandler(l)
			l.keyboard.AddModifiersHandler(l)
			Debug("OK: Keyboard handlers added")
		}
	}

	// 8. Process keyboard with another roundtrip
	err = wlclient.DisplayRoundtrip(l.display)
	if err != nil {
		Debug("WARNING: Keyboard handlers roundtrip failed: %v", err)
		// Continue anyway - keyboard isn't critical
	}

	// Start password UI feedback handler
	go func() {
		var redrawTimer *time.Timer
		for length := range l.redrawCh {
			if redrawTimer != nil {
				redrawTimer.Stop()
			}
			redrawTimer = time.AfterFunc(50*time.Millisecond, func() {
				for _, entry := range l.surfaces {
					drawPasswordFeedback(l, entry.wlSurface, length, 0)
				}
			})
		}
	}()

	Debug("ALL SETUP COMPLETE - Entering event loop...")

	// Event loop
	go func() {
		Debug("Event loop started in goroutine")
		for {
			select {
			case <-l.done:
				Debug("Event loop received done signal")
				return
			default:
				dispErr := wlclient.DisplayDispatch(l.display)
				if dispErr != nil {
					Info("ERROR in event loop: %v\n", dispErr)
					close(l.done)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Wait for unlock signal
	Debug("Main function waiting for unlock...")
	<-l.done
	Debug("Received unlock signal, exiting")

	return nil
}

func (l *WaylandLocker) authenticate() {
	// Check if we're in a lockout period
	if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
		// Still in lockout period, don't even attempt authentication
		remainingTime := time.Until(l.lockoutUntil).Round(time.Second)
		Info("Authentication locked out for another %v", remainingTime)
		l.securePassword.Clear()
		return
	}

	// If we were in a lockout but it's expired, clear the lockout state
	if l.lockoutActive && time.Now().After(l.lockoutUntil) {
		Info("Lockout period has expired, clearing lockout state")
		l.lockoutActive = false
	}

	if l.helper == nil {
		l.helper = NewLockHelper(l.config)
		Debug("Created lock helper for PAM auth")
	}

	password := l.securePassword.String()
	result := l.helper.authenticator.Authenticate(password)
	Debug("PAM result: success=%v message=%s", result.Success, result.Message)

	if result.Success {
		Debug("Auth OK, unlocking session")

		go func() {
			if l.mediaPlayer != nil {
				Debug("Stopping media player")
				l.mediaPlayer.Stop()
			}

			time.Sleep(200 * time.Millisecond)

			if l.lock != nil {
				Debug("Safely unlocking session")
				func() {
					defer func() {
						if r := recover(); r != nil {
							Error("Recovered from panic in unlock: %v", r)
						}
					}()
					l.lock.UnlockAndDestroy()
				}()

				time.Sleep(100 * time.Millisecond)
			}

			// Run post-lock command before signaling completion
			if err := l.helper.RunPostLockCommand(); err != nil {
				Warn("Post-lock command error: %v", err)
			} else {
				if l.config.PostLockCommand == "" {
					// Add a small delay when no post-lock command is specified
					// to ensure proper cleanup of Wayland resources
					Debug("No post-lock command specified, adding small delay for cleanup")
					time.Sleep(200 * time.Millisecond)
				} else {
					Info("Post-lock command executed successfully")
				}
			}

			Debug("Signaling completion")
			close(l.done)
		}()
	} else {
		Debug("Auth failed: %s", result.Message)

		// Authentication failed, increment counter
		l.failedAttempts++
		l.lastFailureTime = time.Now()
		Info("Authentication failed (%d/3 attempts): %s", l.failedAttempts, result.Message)

		// Check if we should implement a lockout
		if l.failedAttempts >= 3 {
			// Determine lockout duration - start with 30 seconds
			lockoutDuration := 30 * time.Second

			// If this isn't the first lockout, increase the duration
			if l.lockoutActive {
				// Increase by 30 seconds each time
				lockoutDuration = 30 * time.Second * time.Duration(l.failedAttempts/3)
				// Cap at 5 minutes
				if lockoutDuration > 5*time.Minute {
					lockoutDuration = 5 * time.Minute
				}
			}

			// Set the lockout time
			l.lockoutUntil = time.Now().Add(lockoutDuration)
			l.lockoutActive = true

			Info("Failed %d attempts, locking out for %v", l.failedAttempts, lockoutDuration)

			// Show lockout message on screen
			l.StartCountdown("Account locked", int(lockoutDuration.Seconds()))

			// Reset counter after implementing lockout
			l.failedAttempts = 0
		}

		if strings.Contains(strings.ToLower(result.Message), "account is locked") {
			Debug("String contains 'account is locked'")
			l.StartCountdown("Account locked", 300)
		}

		l.shakePasswordDots()
	}

	l.securePassword.Clear()

	select {
	case l.redrawCh <- 0: // Send 0 to indicate no dots
	default:
	}
}

func (l *WaylandLocker) StartCountdown(message string, duration int) {
	Debug(">>> Starting countdown: %s (%ds)", message, duration)

	// Set countdown active flag
	l.countdownActive = true

	// Make sure the duration is reasonable
	if duration > 300 {
		Debug("Capping long duration to 300 seconds")
		duration = 300 // Cap at 5 minutes
	}

	go func() {
		Debug("Starting countdown on all surfaces")

		// Update every second
		for i := duration; i >= 0; i-- {
			// Loop through all surfaces to show the countdown on each
			for _, entry := range l.surfaces {
				if entry.wlSurface != nil {
					func(s *wl.Surface) {
						defer func() {
							if r := recover(); r != nil {
								Error("Recovered from panic in countdown: %v", r)
							}
						}()

						safeCenteredMessage(s, l, message, i)
					}(entry.wlSurface)
				}
			}

			Debug("Countdown: %d seconds remaining", i)

			// Check if we should continue
			if i > 0 {
				time.Sleep(1 * time.Second)
			}
		}

		Debug("Countdown finished")

		// Clear the countdown message after it's done
		for _, entry := range l.surfaces {
			if entry.wlSurface != nil {
				func(s *wl.Surface) {
					defer func() {
						if r := recover(); r != nil {
							Error("Recovered from panic when clearing countdown: %v", r)
						}
					}()

					clearMessage(s, l)
				}(entry.wlSurface)
			}
		}

		// Reset countdown active flag
		l.countdownActive = false
	}()
}

func clearMessage(surface *wl.Surface, l *WaylandLocker) {
	if surface == nil || l == nil {
		return
	}

	// Use safe defaults for width/height
	width := 3840
	height := 2160

	stride := width * 4
	size := stride * height

	fd, err := unix.MemfdCreate("clearbuffer", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd for clear: %v", err)
		return
	}
	defer unix.Close(fd)

	err = syscall.Ftruncate(fd, int64(size))
	if err != nil {
		Error("Failed to truncate memfd for clear: %v", err)
		return
	}

	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to mmap for clear: %v", err)
		return
	}
	defer syscall.Munmap(data)

	// Fill with completely transparent pixels
	for i := 0; i < size; i += 4 {
		data[i+0] = 0 // Blue
		data[i+1] = 0 // Green
		data[i+2] = 0 // Red
		data[i+3] = 0 // Completely transparent
	}

	// Create shared memory pool
	pool, err := l.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create pool for clear: %v", err)
		return
	}

	// Create buffer
	buffer, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer for clear: %v", err)
		return
	}

	// Attach and commit
	surface.Attach(buffer, 0, 0)
	surface.Damage(0, 0, int32(width), int32(height))
	surface.Commit()
}

func safeCenteredMessage(surface *wl.Surface, l *WaylandLocker, message string, secondsLeft int) {
	if surface == nil || l == nil {
		return
	}

	// Use safe defaults for width/height
	width := 3840
	height := 2160

	// Try to get actual dimensions instead
	for output, entry := range l.surfaces {
		if entry.wlSurface == surface {
			// Find this output in geometries
			if l.registryHandler != nil && l.registryHandler.outputGeometries != nil {
				if info, ok := l.registryHandler.outputGeometries[output]; ok {
					width = info.width
					height = info.height
				}
			}
			break
		}
	}

	// Format time in mm:ss format
	minutes := secondsLeft / 60
	seconds := secondsLeft % 60
	timeStr := fmt.Sprintf("%02d:%02d", minutes, seconds)

	// Create RGBA image
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Draw semi-transparent black background
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{0, 0, 0, 200}) // More opaque black
		}
	}

	// Draw "LOCKED" message at the center
	lockedMsg := "LOCKED"

	// Create a font drawer for basic text
	ttf, err := opentype.Parse(fontBytes)
	if err != nil {
		Error("Failed to parse embedded TTF font: %v", err)
		return
	}

	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{
		Size:    96, // Big font size
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		Error("Failed to create font face: %v", err)
		return
	}
	defer face.Close()

	lockedX := (width - font.MeasureString(face, lockedMsg).Round()) / 2
	lockedY := height/2 - 50

	d := &font.Drawer{
		Dst:  img,
		Src:  image.White,
		Face: face,
		Dot:  fixed.P(lockedX, lockedY),
	}
	d.DrawString(lockedMsg)

	// Timer below
	timerX := (width - font.MeasureString(face, timeStr).Round()) / 2
	timerY := height/2 + 50
	d.Dot = fixed.P(timerX, timerY)
	d.DrawString(timeStr)

	// Convert the image to a byte slice for Wayland
	stride := width * 4
	size := stride * height

	fd, err := unix.MemfdCreate("msgbuffer", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd for message: %v", err)
		return
	}
	defer unix.Close(fd)

	err = syscall.Ftruncate(fd, int64(size))
	if err != nil {
		Error("Failed to truncate memfd for message: %v", err)
		return
	}

	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to mmap for message: %v", err)
		return
	}
	defer syscall.Munmap(data)

	// Copy image data to the buffer
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			offset := (y*width + x) * 4
			data[offset+0] = byte(b >> 8)
			data[offset+1] = byte(g >> 8)
			data[offset+2] = byte(r >> 8)
			data[offset+3] = byte(a >> 8)
		}
	}

	// Create shared memory pool and rest of the function remains unchanged
	pool, err := l.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create pool for message: %v", err)
		return
	}

	// Create buffer
	buffer, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer for message: %v", err)
		pool.Destroy()
		return
	}

	// Attach and commit
	surface.Attach(buffer, 0, 0)
	surface.Damage(0, 0, int32(width), int32(height))
	surface.Commit()
}
