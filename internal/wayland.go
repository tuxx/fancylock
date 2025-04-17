package internal

import (
	_ "embed"
	"fmt"
	"image"
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

var _ wl.KeyboardKeyHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardEnterHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardLeaveHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardKeymapHandler = (*WaylandLocker)(nil)
var _ wl.KeyboardModifiersHandler = (*WaylandLocker)(nil)

func (h *surfaceHandler) HandleSessionLockSurfaceConfigure(ev ext.SessionLockSurfaceConfigureEvent) {
	Info("Parent surface configure: serial=%d, width=%d, height=%d\n", ev.Serial, ev.Width, ev.Height)

	// Acknowledge the configure
	h.lockSurface.AckConfigure(ev.Serial)
	Debug("Acknowledged parent surface configure")

	// Create a shared memory buffer for the parent surface
	stride := int(ev.Width) * 4
	size := stride * int(ev.Height)

	// Create memory-backed file descriptor
	fd, err := unix.MemfdCreate("parent-buffer", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd for parent surface: %v", err)
		return
	}
	defer unix.Close(fd) // Ensure fd is closed on all exit paths

	// Set the size of the file
	if err = syscall.Ftruncate(fd, int64(size)); err != nil {
		Error("Failed to truncate memfd for parent surface: %v", err)
		return
	}

	// Map the file into memory
	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to mmap parent surface buffer: %v", err)
		return
	}
	defer syscall.Munmap(data)

	// Fill with fully transparent black color (RGBA format) for the parent lock surface
	for i := 0; i < size; i += 4 {
		data[i+0] = 0 // Blue
		data[i+1] = 0 // Green
		data[i+2] = 0 // Red
		data[i+3] = 0 // Alpha (fully transparent)
	}

	pool, err := h.client.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create pool for parent surface: %v", err)
		return
	}
	defer pool.Destroy() // Ensure pool is destroyed

	// Create a buffer from the pool
	buffer, err := pool.CreateBuffer(0, int32(ev.Width), int32(ev.Height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer for parent surface: %v", err)
		// Pool is destroyed by defer above
		return
	}
	// No need to explicitly destroy the buffer? Wayland protocol usually handles this.

	// Attach buffer to the parent surface and commit
	h.parentSurface.Attach(buffer, 0, 0)
	h.parentSurface.Damage(0, 0, int32(ev.Width), int32(ev.Height))
	// Set input region to nil to allow input passthrough (mouse clicks, etc.)
	// The actual input handling (keyboard) is done via the wl_seat/wl_keyboard global objects.
	h.parentSurface.SetInputRegion(nil)
	h.parentSurface.Commit()

	// Now configure the child surface. We can make it opaque black initially.
	// This surface will be used by MPV.
	// We can reuse the dimensions.
	childStride := stride
	childSize := size
	childFd, err := unix.MemfdCreate("child-buffer", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd for child surface: %v", err)
		return
	}
	defer unix.Close(childFd)

	if err = syscall.Ftruncate(childFd, int64(childSize)); err != nil {
		Error("Failed to truncate memfd for child surface: %v", err)
		return
	}

	childData, err := syscall.Mmap(childFd, 0, childSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to mmap child surface buffer: %v", err)
		return
	}
	defer syscall.Munmap(childData)

	// Fill child surface with opaque black initially
	for i := 0; i < childSize; i += 4 {
		childData[i+0] = 0   // Blue
		childData[i+1] = 0   // Green
		childData[i+2] = 0   // Red
		childData[i+3] = 255 // Alpha (fully opaque)
	}

	childPool, err := h.client.shm.CreatePool(uintptr(childFd), int32(childSize))
	if err != nil {
		Error("Failed to create pool for child surface: %v", err)
		return
	}
	defer childPool.Destroy()

	childBuffer, err := childPool.CreateBuffer(0, int32(ev.Width), int32(ev.Height), int32(childStride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer for child surface: %v", err)
		return
	}

	// Attach buffer to child surface and commit
	h.childSurface.Attach(childBuffer, 0, 0)
	h.childSurface.Damage(0, 0, int32(ev.Width), int32(ev.Height))
	h.childSurface.Commit() // Commit the child surface independently

	// No need to explicitly destroy childBuffer?

	Info("Configured parent (%dx%d, transparent) and child (%dx%d, black) surfaces", ev.Width, ev.Height, ev.Width, ev.Height)
}

func NewWaylandLocker(config Configuration) *WaylandLocker {
	Debug("WaylandLocker logger initialized")

	return &WaylandLocker{
		display: nil,
		surfaces: make(map[*wl.Output]struct {
			parentSurface *wl.Surface
			childSurface  *wl.Surface
			subsurface    *wl.Subsurface
			lockSurface   *ext.SessionLockSurface
		}),
		outputs:         make(map[uint32]*wl.Output),
		done:            make(chan struct{}),
		redrawCh:        make(chan int, 1),
		config:          config,
		helper:          NewLockHelper(config),
		lockActive:      false,
		mediaPlayer:     NewMediaPlayer(config),
		lockoutManager:  NewLockoutManager(config),
		countdownActive: false,
		securePassword:  NewSecurePassword(),
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

	// Store keymap information
	l.keymapFormat = ev.Format
	l.keymapSize = ev.Size

	// Read the keymap data
	if ev.Fd != 0 {
		data, err := syscall.Mmap(int(ev.Fd), 0, int(ev.Size), syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			Error("Failed to mmap keymap: %v", err)
			return
		}
		defer syscall.Munmap(data)

		// Copy the keymap data
		l.keymapData = make([]byte, ev.Size)
		copy(l.keymapData, data)

		// Initialize XKB context if not already done
		if l.xkbContext == 0 {
			l.xkbContext = XkbContextNew(ContextNoFlags)
			if l.xkbContext == 0 {
				Error("Failed to create XKB context")
				return
			}
		}

		// Create XKB keymap from the keymap data
		if l.xkbKeymap != 0 {
			XkbKeymapUnref(l.xkbKeymap)
		}
		l.xkbKeymap = XkbKeymapNewFromString(l.xkbContext, string(l.keymapData), KeymapFormatTextV1, ContextNoFlags)
		if l.xkbKeymap == 0 {
			Error("Failed to create XKB keymap")
			return
		}

		// Create XKB state
		if l.xkbState != 0 {
			XkbStateUnref(l.xkbState)
		}
		l.xkbState = XkbStateNew(l.xkbKeymap)
		if l.xkbState == 0 {
			Error("Failed to create XKB state")
			return
		}
	}
}

// Handle keyboard modifier events
func (l *WaylandLocker) HandleKeyboardModifiers(ev wl.KeyboardModifiersEvent) {
	Info("Keyboard modifiers event received: mods=%d,%d,%d\n",
		ev.ModsDepressed, ev.ModsLatched, ev.ModsLocked)

	// Update XKB state with the new modifiers
	if l.xkbState != 0 {
		// Convert Wayland modifier masks to XKB modifier masks
		mods := ev.ModsDepressed | ev.ModsLatched | ev.ModsLocked
		XkbStateUpdateMask(l.xkbState, mods, 0, 0, 0, 0, 0)
	}
}

func drawPasswordFeedback(l *WaylandLocker, parentSurface *wl.Surface, count int, offsetX int) {
	width, height := l.getSurfaceDimensions(parentSurface)
	if width <= 0 || height <= 0 {
		Warn("Invalid surface dimensions (%dx%d) for password feedback, skipping draw", width, height)
		return
	}
	stride := width * 4     // Use fetched width
	size := stride * height // Use fetched dimensions

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

	// Fill with transparent color first
	for i := 0; i < size; i += 4 {
		data[i+0] = 0 // Blue
		data[i+1] = 0 // Green
		data[i+2] = 0 // Red
		data[i+3] = 0 // Alpha (fully transparent)
	}

	// Much larger dots
	dotSpacing := 40 // Increased spacing for larger dots
	dotRadius := 12  // 4x the original size (was 3)
	totalWidth := count * dotSpacing
	startX := (width-totalWidth)/2 + offsetX // Use fetched width
	y := height - 100                        // Use fetched height

	for i := 0; i < count && i < 32; i++ {
		x := startX + i*dotSpacing
		// Draw larger circular dots
		for dy := -dotRadius; dy <= dotRadius; dy++ {
			for dx := -dotRadius; dx <= dotRadius; dx++ {
				// Make sure we're within the circle
				if dx*dx+dy*dy <= dotRadius*dotRadius {
					px := x + dx
					py := y + dy
					if px >= 0 && py >= 0 && px < width && py < height { // Use fetched dimensions
						offset := (py*width + px) * 4 // Use fetched width
						data[offset+0] = 0xff         // Blue
						data[offset+1] = 0xff         // Green
						data[offset+2] = 0xff         // Red
						data[offset+3] = 0xff         // Alpha
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
	defer pool.Destroy() // Ensure pool is destroyed

	buffer, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer: %v", err)
		return
	}
	// Buffer will be destroyed when parentSurface is destroyed?

	// Set input region to nil to allow input through the transparent parts
	parentSurface.SetInputRegion(nil)

	// Attach buffer to surface and commit
	parentSurface.Attach(buffer, 0, 0)
	parentSurface.Damage(0, 0, int32(width), int32(height))
	parentSurface.Commit()

	Debug("Drew password feedback dots on parent surface: count=%d, offsetX=%d", count, offsetX)
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
			if entry.parentSurface != nil { // Target parentSurface
				drawPasswordFeedback(l, entry.parentSurface, dotCount, distance)
			}
		}
		time.Sleep(delay)

		// Move left
		for _, entry := range l.surfaces {
			if entry.parentSurface != nil { // Target parentSurface
				drawPasswordFeedback(l, entry.parentSurface, dotCount, -distance)
			}
		}
		time.Sleep(delay)

		// Back to center
		for _, entry := range l.surfaces {
			if entry.parentSurface != nil { // Target parentSurface
				drawPasswordFeedback(l, entry.parentSurface, dotCount, 0)
			}
		}
		time.Sleep(delay)
	}

	// Final redraw with no dots
	for _, entry := range l.surfaces {
		if entry.parentSurface != nil { // Target parentSurface
			drawPasswordFeedback(l, entry.parentSurface, 0, 0)
		}
	}
}

// Handle keyboard key events
func (l *WaylandLocker) HandleKeyboardKey(ev wl.KeyboardKeyEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Only handle key press events
	if ev.State != wl.KeyboardKeyStatePressed {
		return
	}

	// If countdown is active, ignore all keys except Escape
	if l.countdownActive {
		if ev.Key == 1 { // Escape key
			l.countdownActive = false
			l.countdownTimer.Stop()
			l.securePassword.Clear()
			if l.config.DebugExit {
				Info("ESC pressed during countdown, triggering debug exit\n")
				if l.lock != nil {
					l.lock.UnlockAndDestroy()
				}
				close(l.done)
			}
		}
		return
	}

	// Handle special keys
	switch ev.Key {
	case 1: // Escape key
		l.handleEscape()
		return
	case 28: // Enter key
		l.handleEnter()
		return
	case 14: // Backspace key
		l.handleBackspace()
		return
	}

	// Convert key code to character using XKB state
	if l.xkbState != 0 && l.xkbKeymap != 0 {
		// Get the key symbol
		sym := XkbStateKeyGetSym(l.xkbState, ev.Key+8) // Add 8 to convert from evdev to XKB keycode
		if sym != 0 {
			// Convert the key symbol to a character
			utf32 := XkbKeysymToUtf32(sym)
			if utf32 != 0 {
				// Convert UTF-32 to UTF-8
				r := rune(utf32)
				if r >= 0x20 && r <= 0x7e { // Printable ASCII range
					l.handleChar(r)
					return
				}
			}
		}
	}

	// Log unhandled keys
	Debug("Unhandled key: code=%d, state=%d", ev.Key, ev.State)
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

func (f handlerFunc) HandleOutputGeometry(ev wl.OutputGeometryEvent) { f(ev) }

func (f outputModeHandlerFunc) HandleOutputMode(ev wl.OutputModeEvent) {
	f(ev)
}

// HandleRegistryGlobalRemove handles registry global remove events
func (h *RegistryHandler) HandleRegistryGlobalRemove(ev wl.RegistryGlobalRemoveEvent) {
	// Remove the output from our map if it exists
	if output, ok := h.outputs[ev.Name]; ok {
		delete(h.outputs, ev.Name)
		delete(h.outputGeometries, output)
	}
}

// HandleKeyboardRepeatInfo handles keyboard repeat info events
func (l *WaylandLocker) HandleKeyboardRepeatInfo(ev wl.KeyboardRepeatInfoEvent) {
	// We don't need to handle keyboard repeat info for our use case
}

// HandleRegistryGlobal handles registry global events
func (h *RegistryHandler) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		h.compositor = wlclient.RegistryBindCompositorInterface(h.registry, ev.Name, ev.Version)
		Debug("Bound wl_compositor")
	case "wl_subcompositor": // Add case for subcompositor
		Debug("Found wl_subcompositor interface")
		// Create the proxy object using the context from the registry
		if h.registry == nil || h.registry.Context() == nil {
			Error("Cannot bind wl_subcompositor: registry or context is nil")
			return
		}
		subComp := wl.NewSubcompositor(h.registry.Context()) // Use wl.NewSubcompositor
		// Bind the global using the correct string name and the new proxy
		err := h.registry.Bind(ev.Name, "wl_subcompositor", ev.Version, subComp)
		if err != nil {
			Error("Failed to bind wl_subcompositor: %v", err)
		} else {
			h.subcompositor = subComp
			Debug("Bound wl_subcompositor")
		}
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
		// Use the version reported by the event, minimum 4?
		l.compositor = wlclient.RegistryBindCompositorInterface(l.registry, ev.Name, 4)
		Debug("Bound wl_compositor interface")
	case "wl_subcompositor": // Add binding for subcompositor here as well
		Debug("Found wl_subcompositor interface")
		if l.registry == nil || l.registry.Context() == nil {
			Error("Cannot bind wl_subcompositor in locker: registry or context is nil")
			return
		}
		// Create the proxy object using the context from the registry
		subComp := wl.NewSubcompositor(l.registry.Context()) // Use wl.NewSubcompositor
		// Bind the global using the correct string name and the new proxy, version 1
		err := l.registry.Bind(ev.Name, "wl_subcompositor", 1, subComp)
		if err != nil {
			Error("Failed to bind wl_subcompositor in locker handler: %v", err)
		} else {
			l.subcompositor = subComp
			Debug("Bound wl_subcompositor interface")
		}
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

// Lock implements the screen locking functionality
func (l *WaylandLocker) Lock() error {
	Info("Locking screen")
	l.lockActive = true

	// Run pre-lock command if configured
	if err := l.helper.RunPreLockCommand(); err != nil {
		Error("Failed to run pre-lock command: %v", err)
		// Continue with locking despite the error
	}

	// Pause media if enabled
	if err := l.helper.PauseMediaIfEnabled(); err != nil {
		Error("Failed to pause media: %v", err)
		// Continue with locking despite the error
	}

	// Initialize Wayland connection
	if err := l.initWayland(); err != nil {
		Error("Failed to initialize Wayland: %v", err)
		return err
	}

	// Start the media player if configured
	if l.mediaPlayer != nil {
		if err := l.mediaPlayer.Start(); err != nil {
			Error("Failed to start media player: %v", err)
			// Continue with locking despite the error
		}
	}

	// Start redraw goroutine for password dots
	go func() {
		for {
			select {
			case <-l.done:
				return
			case count := <-l.redrawCh:
				Debug("Redrawing password dots: count=%d", count)
				for _, entry := range l.surfaces {
					if entry.parentSurface != nil { // Draw on parent surface
						drawPasswordFeedback(l, entry.parentSurface, count, 0)
					}
				}
			}
		}
	}()

	// Wait for lock to complete
	<-l.done

	return nil
}

func (l *WaylandLocker) authenticate() {
	// Check if we're in a lockout period using the lockout manager
	if l.lockoutManager.IsLockedOut() {
		// Still in lockout period, don't even attempt authentication
		remainingTime := l.lockoutManager.GetRemainingTime().Round(time.Second)
		Info("Authentication locked out for another %v", remainingTime)
		l.securePassword.Clear()
		return
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

		// Reset lockout on successful authentication
		l.lockoutManager.ResetLockout()

		go func() {
			if l.mediaPlayer != nil {
				Debug("Stopping media player")
				l.mediaPlayer.Stop()
			}

			// Unpause media if enabled
			if err := l.helper.UnpauseMediaIfEnabled(); err != nil {
				Warn("Failed to unpause media: %v", err)
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

		// Authentication failed, use the lockout manager to handle the failed attempt
		lockoutActive, lockoutDuration, _ := l.lockoutManager.HandleFailedAttempt()

		// First, do the password shake animation
		l.shakePasswordDots()

		// If lockout was activated, show the lockout message
		if lockoutActive {
			Info("Lockout activated until: %v", l.lockoutManager.GetLockoutUntil())

			// Show lockout message on screen AFTER the shake animation is complete
			l.StartCountdown("Account locked", int(lockoutDuration.Seconds()))
		}
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
	if duration > 600 {
		Debug("Capping long duration to 600 seconds")
		duration = 600 // Cap at 10 minutes
	}

	go func() {
		Debug("Starting countdown on all parent surfaces")

		// Update every second
		for i := duration; i >= 0; i-- {
			// Loop through all surfaces to show the countdown on each
			for _, entry := range l.surfaces {
				if entry.parentSurface != nil { // Use parentSurface
					func(s *wl.Surface) {
						defer func() {
							if r := recover(); r != nil {
								Error("Recovered from panic in countdown: %v", r)
							}
						}()

						safeCenteredMessage(s, l, message, i) // Pass parentSurface
					}(entry.parentSurface)
				}
			}

			Debug("Countdown: %d seconds remaining", i)

			// Check if we should continue
			if i > 0 {
				// Use a timer to avoid drift and allow cancellation
				timer := time.NewTimer(1 * time.Second)
				select {
				case <-timer.C:
					// Continue loop
				case <-l.done:
					// Stop countdown if lock is finished
					timer.Stop()
					Debug("Countdown interrupted by lock finish")
					return
				}
			}
		}

		Debug("Countdown finished")

		// Clear the countdown message after it's done
		for _, entry := range l.surfaces {
			if entry.parentSurface != nil { // Use parentSurface
				func(s *wl.Surface) {
					defer func() {
						if r := recover(); r != nil {
							Error("Recovered from panic clearing message: %v", r)
						}
					}()
					clearMessage(s, l) // Pass parentSurface
				}(entry.parentSurface)
			}
		}

		// Reset countdown active flag
		l.mu.Lock()
		l.countdownActive = false
		l.mu.Unlock()
	}()
}

func clearMessage(parentSurface *wl.Surface, l *WaylandLocker) {
	width, height := l.getSurfaceDimensions(parentSurface)
	if width <= 0 || height <= 0 {
		Warn("Invalid surface dimensions (%dx%d) for clearing message, skipping draw", width, height)
		return
	}
	stride := width * 4
	size := stride * height

	fd, err := unix.MemfdCreate("clearmsg", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd for clearing message: %v", err)
		return
	}
	defer unix.Close(fd)

	err = syscall.Ftruncate(fd, int64(size))
	if err != nil {
		Error("Failed to truncate memfd for clearing message: %v", err)
		return
	}

	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to map memory for clearing message: %v", err)
		return
	}
	defer syscall.Munmap(data)

	// Fill with fully transparent
	for i := 0; i < size; i += 4 {
		data[i+3] = 0 // Alpha
	}

	pool, err := l.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create shm pool for clearing message: %v", err)
		return
	}
	defer pool.Destroy()

	buffer, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer for clearing message: %v", err)
		return
	}

	parentSurface.Attach(buffer, 0, 0)
	parentSurface.Damage(0, 0, int32(width), int32(height))
	parentSurface.SetInputRegion(nil) // Ensure input region is still nil
	parentSurface.Commit()

	Debug("Cleared message on parent surface")
}

func safeCenteredMessage(parentSurface *wl.Surface, l *WaylandLocker, message string, secondsLeft int) {
	width, height := l.getSurfaceDimensions(parentSurface)
	if width <= 0 || height <= 0 {
		Warn("Invalid surface dimensions (%dx%d) for drawing message, skipping", width, height)
		return
	}
	stride := width * 4
	size := stride * height

	fd, err := unix.MemfdCreate("msgbuffer", unix.MFD_CLOEXEC)
	if err != nil {
		Error("Failed to create memfd for message buffer: %v", err)
		return
	}
	defer unix.Close(fd)

	if err := syscall.Ftruncate(fd, int64(size)); err != nil {
		Error("Failed to truncate memfd for message buffer: %v", err)
		return
	}

	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Error("Failed to map memory for message buffer: %v", err)
		return
	}
	defer syscall.Munmap(data)

	// Fill with transparent black initially
	for i := 0; i < size; i += 4 {
		data[i+3] = 0 // Alpha
	}

	// Create an RGBA image wrapping the shared memory
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Pix = data // Point Pix to our mmapped data

	// Parse the font
	f, err := opentype.Parse(fontBytes)
	if err != nil {
		Error("Failed to parse font: %v", err)
		return // Cannot draw text without font
	}

	// Create a font face
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    48, // Increased font size
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		Error("Failed to create font face: %v", err)
		return
	}
	defer face.Close()

	// Prepare text drawer
	d := &font.Drawer{
		Dst:  img,
		Src:  image.White, // Text color
		Face: face,
	}

	// Format the message with remaining time
	fullMessage := fmt.Sprintf("%s (%d)", message, secondsLeft)

	// Calculate text bounds to center it
	bounds, _ := d.BoundString(fullMessage)
	textWidth := (bounds.Max.X - bounds.Min.X).Ceil()
	textHeight := (bounds.Max.Y - bounds.Min.Y).Ceil() // Get height for vertical centering

	// Calculate the starting point for the text to be centered
	startX := (width - textWidth) / 2
	startY := (height / 2) + (textHeight / 2) // Center vertically

	// Set the drawing position
	d.Dot = fixed.Point26_6{
		X: fixed.I(startX),
		Y: fixed.I(startY),
	}

	// Draw the string
	d.DrawString(fullMessage)

	// Create SHM pool and buffer
	pool, err := l.shm.CreatePool(uintptr(fd), int32(size))
	if err != nil {
		Error("Failed to create SHM pool for message: %v", err)
		return
	}
	defer pool.Destroy()

	buffer, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	if err != nil {
		Error("Failed to create buffer for message: %v", err)
		return
	}

	// Attach, damage, set input region, and commit
	parentSurface.Attach(buffer, 0, 0)
	parentSurface.Damage(0, 0, int32(width), int32(height))
	parentSurface.SetInputRegion(nil) // Ensure input region is nil
	parentSurface.Commit()

	Debug("Drew centered message '%s' on parent surface %d", fullMessage, parentSurface.Id())
}

// getSurfaceDimensions retrieves the dimensions associated with a Wayland surface.
// It looks up the output associated with the surface and returns its geometry.
func (l *WaylandLocker) getSurfaceDimensions(surface *wl.Surface) (width, height int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Find the output associated with this surface (assuming parentSurface for now)
	for output, entry := range l.surfaces {
		if entry.parentSurface == surface {
			// Now find the geometry for this output in the registry handler
			if geom, ok := l.registryHandler.outputGeometries[output]; ok {
				Debug("Found dimensions %dx%d for surface %d via output %d", geom.width, geom.height, surface.Id(), output.Id())
				return geom.width, geom.height
			} else {
				Warn("Geometry not found for output %d associated with surface %d", output.Id(), surface.Id())
				return 0, 0
			}
		}
		// Add check for childSurface if needed later
	}
	Warn("Could not find output associated with surface %d to get dimensions", surface.Id())
	return 0, 0 // Return 0, 0 if no dimensions found
}

// initWayland initializes the Wayland connection and resources
func (l *WaylandLocker) initWayland() error {
	var err error
	l.display, err = wlclient.DisplayConnect(nil)
	if err != nil {
		return fmt.Errorf("failed to connect to Wayland display: %v", err)
	}
	Info("Connected to Wayland display")

	registry, err := l.display.GetRegistry() // Get registry and error
	if err != nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("failed to get Wayland registry: %v", err)
	}
	l.registry = registry // Assign registry
	l.registryHandler = &RegistryHandler{
		registry:         l.registry,
		outputs:          make(map[uint32]*wl.Output),
		outputGeometries: make(map[*wl.Output]outputInfo),
		locker:           l,
	}
	// Add specific handlers
	l.registry.AddGlobalHandler(l.registryHandler)
	l.registry.AddGlobalRemoveHandler(l.registryHandler)

	// First roundtrip to get initial globals (compositor, shm, seat, lock_manager, outputs)
	if err := wlclient.DisplayRoundtrip(l.display); err != nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("first display roundtrip failed: %v", err)
	}
	Info("First roundtrip complete")

	// Check required interfaces
	if l.registryHandler.compositor == nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("wl_compositor not available")
	}
	if l.registryHandler.subcompositor == nil { // Check for subcompositor
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("wl_subcompositor not available")
	}
	if l.registryHandler.shm == nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("wl_shm not available")
	}
	if l.registryHandler.seat == nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("wl_seat not available")
	}
	if l.registryHandler.lockManager == nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("ext_session_lock_manager_v1 not available")
	}
	if len(l.registryHandler.outputs) == 0 {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("no wl_output found")
	}
	Info("Required Wayland interfaces found: compositor, subcompositor, shm, seat, lock_manager, output(s)")

	// Assign globals to locker struct
	l.compositor = l.registryHandler.compositor
	l.subcompositor = l.registryHandler.subcompositor // Assign subcompositor
	l.shm = l.registryHandler.shm
	l.seat = l.registryHandler.seat
	l.lockManager = l.registryHandler.lockManager
	l.outputs = l.registryHandler.outputs // Copy the map

	// Setup seat listener for capabilities (keyboard, pointer)
	l.seat.AddCapabilitiesHandler(l)

	// Second roundtrip to get seat capabilities and output geometries/modes
	if err := wlclient.DisplayRoundtrip(l.display); err != nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		return fmt.Errorf("second display roundtrip failed: %v", err)
	}
	Info("Second roundtrip complete, seat capabilities and output info received")

	// Check if keyboard was acquired
	if l.keyboard == nil {
		Warn("Failed to acquire keyboard input")
		// This might not be fatal depending on requirements, but likely needed.
	} else {
		Info("Keyboard input acquired")
	}

	// Create the session lock
	lock, err := l.lockManager.Lock() // Handle two return values
	if err != nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close()
		}
		return fmt.Errorf("failed to create session lock: %v", err)
	}
	l.lock = lock // Assign lock if no error
	l.lock.AddLockedHandler(l)
	l.lock.AddFinishedHandler(l)
	Info("Session lock created")

	// Create surfaces for each output
	for _, output := range l.outputs {
		parentSurface, err := l.compositor.CreateSurface() // Handle two return values
		if err != nil {
			Error("Failed to create parent surface for output %d: %v", output.Id(), err)
			continue // Skip this output if surface creation fails
		}
		childSurface, err := l.compositor.CreateSurface() // Handle two return values
		if err != nil {
			Error("Failed to create child surface for output %d: %v", output.Id(), err)
			parentSurface.Destroy() // Clean up parent surface
			continue
		}

		subsurface, err := l.subcompositor.GetSubsurface(childSurface, parentSurface) // Handle two return values
		if err != nil {
			Error("Failed to get subsurface for output %d: %v", output.Id(), err)
			childSurface.Destroy()  // Clean up child surface
			parentSurface.Destroy() // Clean up parent surface
			continue
		}
		subsurface.SetSync() // Synchronize parent and child commits

		lockSurface, err := l.lock.GetLockSurface(parentSurface, output) // Handle two return values
		if err != nil {
			Error("Failed to get lock surface for output %d: %v", output.Id(), err)
			subsurface.Destroy()    // Clean up subsurface
			childSurface.Destroy()  // Clean up child surface
			parentSurface.Destroy() // Clean up parent surface
			continue
		}

		handler := &surfaceHandler{
			client:        l,
			parentSurface: parentSurface,
			childSurface:  childSurface,
			subsurface:    subsurface,
			lockSurface:   lockSurface,
		}
		lockSurface.AddConfigureHandler(handler) // Use AddConfigureHandler based on implemented handler name

		l.mu.Lock()
		l.surfaces[output] = struct {
			parentSurface *wl.Surface
			childSurface  *wl.Surface
			subsurface    *wl.Subsurface
			lockSurface   *ext.SessionLockSurface
		}{
			parentSurface: parentSurface,
			childSurface:  childSurface,
			subsurface:    subsurface,
			lockSurface:   lockSurface,
		}
		l.mu.Unlock()
		Info("Created parent, child, subsurface, and lock surface for output %d", output.Id())
	}

	// Final roundtrip to ensure all surfaces are created and handlers attached
	if err := wlclient.DisplayRoundtrip(l.display); err != nil {
		if l.display != nil && l.display.Context() != nil {
			l.display.Context().Close() // Use Context().Close()
		}
		// TODO: Need to destroy created surfaces/lock?
		return fmt.Errorf("final display roundtrip failed: %v", err)
	}
	Info("Wayland initialization complete")

	// Set up monitor information for media player, including child surface IDs
	var monitors []Monitor
	monitorIndex := 0
	for output, entry := range l.surfaces {
		if geom, ok := l.registryHandler.outputGeometries[output]; ok {
			if entry.childSurface == nil {
				Warn("Child surface is nil for output %d, cannot provide SurfaceID to MediaPlayer", output.Id())
				monitors = append(monitors, Monitor{
					X:         geom.x,
					Y:         geom.y,
					Width:     geom.width,
					Height:    geom.height,
					SurfaceID: 0, // Indicate invalid surface ID
				})
			} else {
				monitors = append(monitors, Monitor{
					X:         geom.x,
					Y:         geom.y,
					Width:     geom.width,
					Height:    geom.height,
					SurfaceID: uint32(entry.childSurface.Id()), // Cast wl.ProxyId to uint32
				})
				Debug("Prepared monitor %d info: %dx%d @ (%d,%d), SurfaceID: %d",
					monitorIndex, geom.width, geom.height, geom.x, geom.y, entry.childSurface.Id())
			}
		} else {
			Warn("Could not find geometry for output %d when setting up MediaPlayer", output.Id())
			// Optionally add a default monitor here if needed, but it might lack a surface ID
		}
		monitorIndex++
	}
	if l.mediaPlayer != nil {
		l.mediaPlayer.SetMonitors(monitors)
	} else {
		Warn("MediaPlayer is nil during initWayland, cannot set monitors")
	}

	// Start event dispatch loop in a separate goroutine
	go func() {
		err := wlclient.DisplayDispatch(l.display)
		if err != nil {
			// If dispatch fails (e.g., connection closed), signal done
			Error("Wayland display dispatch error: %v", err)
			// Check if done channel is already closed
			select {
			case <-l.done:
				// Already closed, do nothing
			default:
				close(l.done)
			}
		}
	}()

	return nil
}

func (h *RegistryHandler) HandleKeyboardModifiers(keyboard *wl.Keyboard, serial uint32, modsDepressed, modsLatched, modsLocked, groupDepressed, groupLatched, groupLocked uint32) {
	h.locker.mu.Lock()
	defer h.locker.mu.Unlock()

	if h.locker.xkbState != 0 {
		XkbStateUpdateMask(h.locker.xkbState, modsDepressed, modsLatched, modsLocked, groupDepressed, groupLatched, groupLocked)
	}
}

func (h *RegistryHandler) HandleKeyboardKey(keyboard *wl.Keyboard, serial uint32, time uint32, key uint32, state uint32) {
	h.locker.mu.Lock()
	defer h.locker.mu.Unlock()

	// Ignore key release events
	if state != 1 {
		return
	}

	// Handle special keys
	switch key {
	case 1: // Escape
		h.locker.handleEscape()
		return
	case 28: // Enter
		h.locker.handleEnter()
		return
	case 14: // Backspace
		h.locker.handleBackspace()
		return
	}

	// Convert key code to character using XKB state
	if h.locker.xkbState != 0 {
		sym := XkbStateKeyGetSym(h.locker.xkbState, key)
		if sym != 0 {
			char := XkbKeysymToUtf32(sym)
			if char != 0 {
				h.locker.handleChar(rune(char))
				return
			}
		}
	}

	// Log unhandled keys
	fmt.Printf("Unhandled key: %d\n", key)
}

// handleEscape handles the Escape key press
func (l *WaylandLocker) handleEscape() {
	Info("ESC pressed, clearing password\n")
	l.securePassword.Clear()
	if l.config.DebugExit {
		Info("Debug exit triggered by ESC key\n")
		if l.lock != nil {
			l.lock.UnlockAndDestroy()
		}
		close(l.done)
	}
}

// handleEnter handles the Enter key press
func (l *WaylandLocker) handleEnter() {
	Info("ENTER key detected, authenticating\n")
	l.authenticate()
}

// handleBackspace handles the Backspace key press
func (l *WaylandLocker) handleBackspace() {
	Info("BACKSPACE pressed, removing last character\n")
	l.securePassword.RemoveLast()
	select {
	case l.redrawCh <- l.securePassword.Length():
	default:
	}
}

// handleChar handles a character key press
func (l *WaylandLocker) handleChar(r rune) {
	// Accept a wider range of characters, including those generated by Alt+key combinations
	// This includes most Unicode characters that might be used in passwords
	if r >= 0x20 && r <= 0x10FFFF { // Accept most Unicode characters
		l.securePassword.Append(byte(r))
		select {
		case l.redrawCh <- l.securePassword.Length():
		default:
		}
		//Debug("Character '%c' (U+%04X) added to password", r, r) // Let's not output password characters to any log.
	}
}
