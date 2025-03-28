package internal

import (
	"fmt"
	"syscall"
	"time"

	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"
	ext "github.com/tuxx/wayland-ext-session-lock-go"
	"golang.org/x/sys/unix"
)

type WaylandLocker struct {
	display     *wl.Display
	registry    *wl.Registry
	compositor  *wl.Compositor
	lockManager *ext.SessionLockManager
	lock        *ext.SessionLock
	keyboard    *wl.Keyboard
	seat        *wl.Seat
	shm         *wl.Shm
	passwordBuf string
	surfaces    map[*wl.Output]*ext.SessionLockSurface
	outputs     map[uint32]*wl.Output
	done        chan struct{}
}

type surfaceHandler struct {
	client      *WaylandLocker
	surface     *wl.Surface
	lockSurface *ext.SessionLockSurface
}

func (h *surfaceHandler) HandleSessionLockSurfaceConfigure(ev ext.SessionLockSurfaceConfigureEvent) {
	h.lockSurface.AckConfigure(ev.Serial)
	createSolidColorBuffer(h.client, h.surface, ev.Width, ev.Height, 64, 0, 0)
}

func NewWaylandLocker(config Configuration) *WaylandLocker {
	InitLogger(LevelDebug, true)
	Debug("WaylandLocker logger initialized")
	return &WaylandLocker{
		surfaces: make(map[*wl.Output]*ext.SessionLockSurface),
		outputs:  make(map[uint32]*wl.Output),
		done:     make(chan struct{}),
	}
}

func (l *WaylandLocker) StartIdleMonitor() error {
	return nil
}

func (l *WaylandLocker) Lock() error {
	var err error
	l.display, err = wlclient.DisplayConnect(nil)
	if err != nil {
		return fmt.Errorf("connect to Wayland: %w", err)
	}

	l.registry, err = l.display.GetRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	l.registry.AddGlobalHandler(l)
	err = wlclient.DisplayRoundtrip(l.display)
	if err != nil {
		return fmt.Errorf("registry roundtrip: %w", err)
	}

	if l.lockManager == nil {
		return fmt.Errorf("no ext_session_lock_manager_v1 found")
	}

	l.lock, err = l.lockManager.Lock()
	if err != nil {
		return fmt.Errorf("lock session: %w", err)
	}
	ext.SessionLockAddListener(l.lock, l)

	for _, output := range l.outputs {
		surface, err := l.compositor.CreateSurface()
		if err != nil {
			return err
		}
		lockSurface, err := l.lock.GetLockSurface(surface, output)
		if err != nil {
			return err
		}
		ext.SessionLockSurfaceAddListener(lockSurface, &surfaceHandler{
			client:      l,
			surface:     surface,
			lockSurface: lockSurface,
		})
		l.surfaces[output] = lockSurface
	}

	go func() {
		time.Sleep(5 * time.Second)
		if l.lock != nil {
			Debug("Auto-unlocking after delay")
			l.lock.UnlockAndDestroy()
		}
	}()

	for {
		select {
		case <-l.done:
			return nil
		default:
			err := wlclient.DisplayDispatch(l.display)
			if err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func (l *WaylandLocker) HandleSessionLockLocked(ev ext.SessionLockLockedEvent) {}

func (l *WaylandLocker) HandleSessionLockFinished(ev ext.SessionLockFinishedEvent) {
	close(l.done)
}

func (l *WaylandLocker) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		l.compositor = wlclient.RegistryBindCompositorInterface(l.registry, ev.Name, 4)
	case "ext_session_lock_manager_v1":
		l.lockManager = ext.BindSessionLockManager(l.registry, ev.Name, 1)
	case "wl_output":
		output := wlclient.RegistryBindOutputInterface(l.registry, ev.Name, 3)
		l.outputs[ev.Name] = output
	case "wl_shm":
		l.shm = wlclient.RegistryBindShmInterface(l.registry, ev.Name, 1)
	case "wl_seat":
		l.seat = wlclient.RegistryBindSeatInterface(l.registry, ev.Name, 7)
		l.seat.AddCapabilitiesHandler(l)
		wlclient.DisplayRoundtrip(l.display)
	}
}

func (l *WaylandLocker) HandleSeatCapabilities(ev wl.SeatCapabilitiesEvent) {
	Debug("Seat capabilities: %d", ev.Capabilities)
	if ev.Capabilities&wl.SeatCapabilityKeyboard != 0 {
		keyboard, err := l.seat.GetKeyboard()
		if err == nil {
			l.keyboard = keyboard
			l.keyboard.AddKeyHandler(l)
			wlclient.DisplayRoundtrip(l.display)
		}
	}
}

func (l *WaylandLocker) HandleKeyboardKey(ev wl.KeyboardKeyEvent) {
	Debug("KEY EVENT: key=%d state=%d", ev.Key, ev.State)
	if ev.State != 1 {
		return
	}

	switch ev.Key {
	case 1:
		l.passwordBuf = ""
	case 28:
		Debug("ENTER pressed, authenticating...")
		l.authenticate()
	case 14:
		if len(l.passwordBuf) > 0 {
			l.passwordBuf = l.passwordBuf[:len(l.passwordBuf)-1]
		}
	default:
		if ev.Key >= 2 && ev.Key <= 13 {
			l.passwordBuf += string('1' + (ev.Key - 2))
		} else if ev.Key >= 16 && ev.Key <= 25 {
			l.passwordBuf += string('q' + (ev.Key - 16))
		} else if ev.Key == 30 {
			l.passwordBuf += "a"
		} else if ev.Key == 31 {
			l.passwordBuf += "s"
		}
	}
	Debug("Buffer: %s", l.passwordBuf)
}

func createSolidColorBuffer(l *WaylandLocker, surface *wl.Surface, width, height uint32, r, g, b uint8) {
	stride := int(width) * 4
	size := stride * int(height)

	fd, err := unix.MemfdCreate("buffer", unix.MFD_CLOEXEC)
	if err != nil {
		Debug("Failed to create memfd: %v", err)
		return
	}
	if err := syscall.Ftruncate(fd, int64(size)); err != nil {
		Debug("Failed to truncate memfd: %v", err)
		return
	}

	data, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		Debug("Failed to mmap: %v", err)
		return
	}
	for i := 0; i < size; i += 4 {
		data[i+0] = b
		data[i+1] = g
		data[i+2] = r
		data[i+3] = 0xff
	}
	pool, _ := l.shm.CreatePool(uintptr(fd), int32(size))
	buffer, _ := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatArgb8888)
	surface.Attach(buffer, 0, 0)
	surface.Damage(0, 0, int32(width), int32(height))
	surface.SetInputRegion(nil)
	surface.Commit()
	Debug("Attached real buffer %dx%d with color #%02x%02x%02x", width, height, r, g, b)
}

func (l *WaylandLocker) authenticate() {
	Debug("Authenticating with: %s", l.passwordBuf)
	if l.passwordBuf == "test" {
		Debug("Unlocked")
		l.lock.UnlockAndDestroy()
	} else {
		Debug("Wrong password")
	}
	l.passwordBuf = ""
}
