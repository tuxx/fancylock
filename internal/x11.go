package internal

import (
	_ "embed"
	"fmt"
	"image"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/dpms"
	"github.com/BurntSushi/xgb/randr"
	"github.com/BurntSushi/xgb/screensaver"
	"github.com/BurntSushi/xgb/xfixes"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed fonts/DejaVuSans-Bold.ttf
var x11FontBytes []byte

// Init initializes the X11 connection and resources
func (l *X11Locker) Init() error {
	Info("Initializing X11 connection and resources")
	var err error

	Info("Attempting to connect to X server")
	l.conn, err = xgb.NewConn()
	if err != nil {
		Error("Failed to connect to X server: %v", err)
		return fmt.Errorf("failed to connect to X server: %v", err)
	}
	Info("Successfully connected to X server")

	Info("Initializing screensaver extension")
	if err := screensaver.Init(l.conn); err != nil {
		Error("Failed to initialize screensaver extension: %v", err)
		return fmt.Errorf("failed to initialize screensaver extension: %v", err)
	}
	Info("Successfully initialized screensaver extension")

	Info("Initializing DPMS extension")
	if err := dpms.Init(l.conn); err != nil {
		Error("Failed to initialize DPMS extension: %v", err)
		return fmt.Errorf("failed to initialize DPMS extension: %v", err)
	}
	Info("Successfully initialized DPMS extension")

	Info("Initializing XFixes extension")
	if err := xfixes.Init(l.conn); err != nil {
		Error("Failed to initialize XFixes extension: %v", err)
		return fmt.Errorf("failed to initialize XFixes extension: %v", err)
	}
	Info("Successfully initialized XFixes extension")

	Info("Initializing XTest extension")
	if err := xtest.Init(l.conn); err != nil {
		Error("Failed to initialize XTest extension: %v", err)
		return fmt.Errorf("failed to initialize XTest extension: %v", err)
	}
	Info("Successfully initialized XTest extension")

	setup := xproto.Setup(l.conn)
	l.screen = setup.DefaultScreen(l.conn)
	l.width = l.screen.WidthInPixels
	l.height = l.screen.HeightInPixels
	Info("Screen dimensions: %dx%d", l.width, l.height)

	Info("Allocating window ID")
	wid, err := xproto.NewWindowId(l.conn)
	if err != nil {
		Error("Failed to allocate window ID: %v", err)
		return fmt.Errorf("failed to allocate window ID: %v", err)
	}
	l.window = wid
	Info("Window ID allocated: %d", l.window)

	// Create an InputOnly window for capturing keyboard and mouse
	Info("Creating InputOnly window for capturing keyboard and mouse")
	err = xproto.CreateWindowChecked(
		l.conn,
		0, // Copy depth from parent
		l.window,
		l.screen.Root,
		0, 0, l.width, l.height,
		0,
		xproto.WindowClassInputOnly, // InputOnly window - completely invisible
		0,                           // Copy visual from parent
		xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{
			1, // Override redirect
			uint32(xproto.EventMaskKeyPress |
				xproto.EventMaskStructureNotify |
				xproto.EventMaskButtonPress),
		},
	).Check()
	if err != nil {
		Error("Failed to create window: %v", err)
		return fmt.Errorf("failed to create window: %v", err)
	}
	Info("InputOnly window created successfully")

	// Set WM_NAME property for identification
	wmName := "FancyLock"
	Info("Setting window name to: %s", wmName)
	xproto.ChangeProperty(l.conn, xproto.PropModeReplace, l.window,
		xproto.AtomWmName, xproto.AtomString, 8, uint32(len(wmName)), []byte(wmName))

	// Initialize GC for drawing password dots
	Info("Initializing graphics context for drawing")
	gcid, err := xproto.NewGcontextId(l.conn)
	if err != nil {
		Error("Failed to allocate graphics context ID: %v", err)
		return fmt.Errorf("failed to allocate graphics context ID: %v", err)
	}
	l.gc = gcid

	err = xproto.CreateGCChecked(
		l.conn,
		l.gc,
		xproto.Drawable(l.screen.Root),
		xproto.GcForeground,
		[]uint32{l.screen.WhitePixel},
	).Check()
	if err != nil {
		Error("Failed to create graphics context: %v", err)
		return fmt.Errorf("failed to create graphics context: %v", err)
	}
	Info("Graphics context initialized successfully")

	// Create a graphics context for text with the embedded font
	l.textGC, err = xproto.NewGcontextId(l.conn)
	if err != nil {
		return fmt.Errorf("failed to create text graphics context: %v", err)
	}

	// Set up the text graphics context with white foreground
	err = xproto.CreateGCChecked(
		l.conn,
		l.textGC,
		xproto.Drawable(l.screen.Root),
		xproto.GcForeground|xproto.GcBackground,
		[]uint32{l.screen.WhitePixel, l.screen.BlackPixel},
	).Check()
	if err != nil {
		Error("Failed to create text graphics context: %v", err)
		return fmt.Errorf("failed to create text graphics context: %v", err)
	}

	// Set font properties for larger text
	fontName := "fixed" // Use the built-in fixed font which is guaranteed to exist

	// Allocate a new font ID
	fontID, err := xproto.NewFontId(l.conn)
	if err != nil {
		Error("Failed to allocate font ID: %v", err)
		return fmt.Errorf("failed to allocate font ID: %v", err)
	}

	// Try to load the font
	err = xproto.OpenFontChecked(
		l.conn,
		fontID,
		uint16(len(fontName)),
		fontName,
	).Check()
	if err != nil {
		Error("Failed to open font: %v", err)
		return fmt.Errorf("failed to open font: %v", err)
	}
	defer xproto.CloseFont(l.conn, fontID)

	err = xproto.ChangeGCChecked(
		l.conn,
		l.textGC,
		xproto.GcFont,
		[]uint32{uint32(fontID)},
	).Check()
	if err != nil {
		Error("Failed to set font: %v", err)
		return fmt.Errorf("failed to set font: %v", err)
	}

	l.dotWindows = []xproto.Window{}
	l.messageWindows = []xproto.Window{}

	Info("X11 initialization completed successfully")
	return nil
}

// detectMonitors detects connected monitors using XRandR extension
func (l *X11Locker) detectMonitors() ([]Monitor, error) {
	Info("Detecting monitors using native XRandR extension")

	// Initialize the RandR extension
	if err := randr.Init(l.conn); err != nil {
		Error("Failed to initialize RandR extension: %v", err)
		return nil, fmt.Errorf("failed to initialize RandR extension: %v", err)
	}

	// Get the X screen resources
	root := l.screen.Root
	resources, err := randr.GetScreenResources(l.conn, root).Reply()
	if err != nil {
		Error("Failed to get screen resources: %v", err)
		return nil, fmt.Errorf("failed to get screen resources: %v", err)
	}

	monitors := []Monitor{}

	// Iterate through all outputs (monitors)
	for _, output := range resources.Outputs {
		// Get output info
		outputInfo, err := randr.GetOutputInfo(l.conn, output, 0).Reply()
		if err != nil {
			Warn("Failed to get output info: %v", err)
			continue
		}

		// Skip disconnected outputs
		if outputInfo.Connection != randr.ConnectionConnected {
			Debug("Skipping disconnected output %d", output)
			continue
		}

		// Skip outputs without CRTC (not actively used)
		if outputInfo.Crtc == 0 {
			Debug("Skipping output without CRTC %d", output)
			continue
		}

		// Get CRTC info to determine position and dimensions
		crtcInfo, err := randr.GetCrtcInfo(l.conn, outputInfo.Crtc, 0).Reply()
		if err != nil {
			Warn("Failed to get CRTC info: %v", err)
			continue
		}

		Debug("Found connected monitor: x=%d, y=%d, width=%d, height=%d",
			crtcInfo.X, crtcInfo.Y, crtcInfo.Width, crtcInfo.Height)

		// Add to the list of monitors
		monitors = append(monitors, Monitor{
			X:      int(crtcInfo.X),
			Y:      int(crtcInfo.Y),
			Width:  int(crtcInfo.Width),
			Height: int(crtcInfo.Height),
		})

		Info("Added monitor: width=%d, height=%d, x=%d, y=%d",
			crtcInfo.Width, crtcInfo.Height, crtcInfo.X, crtcInfo.Y)
	}

	// If no monitors detected, fall back to single monitor
	if len(monitors) == 0 {
		Info("No monitors detected via XRandR, falling back to single monitor with dimensions %dx%d",
			int(l.width), int(l.height))
		monitors = append(monitors, Monitor{
			X:      0,
			Y:      0,
			Width:  int(l.width),
			Height: int(l.height),
		})
	}

	Info("Detected %d monitors in total", len(monitors))
	for i, m := range monitors {
		Info("Monitor %d: x=%d, y=%d, width=%d, height=%d", i, m.X, m.Y, m.Width, m.Height)
	}

	return monitors, nil
}

// NewX11Locker creates a new X11-based screen locker
func NewX11Locker(config Configuration) *X11Locker {
	Info("Creating new X11Locker with config: %+v", config)
	return &X11Locker{
		config:         config,
		helper:         NewLockHelper(config),
		mediaPlayer:    NewMediaPlayer(config),
		passwordBuf:    "",
		isLocked:       false,
		passwordDots:   make([]bool, 0),
		maxDots:        20, // Maximum number of password dots to display
		lockoutManager: NewLockoutManager(config),
	}
}

// Lock immediately locks the screen
func (l *X11Locker) Lock() error {
	// Check if already locked
	if l.isLocked {
		return nil
	}

	// Run pre-lock command if configured
	if err := l.helper.RunPreLockCommand(); err != nil {
		Error("Failed to run pre-lock command: %v", err)
	}

	// Pause media if enabled
	if err := l.helper.PauseMediaIfEnabled(); err != nil {
		Error("Failed to pause media: %v", err)
	}

	// Set locked state
	l.isLocked = true

	// Start media playback if configured
	if l.mediaPlayer != nil {
		if err := l.mediaPlayer.Start(); err != nil {
			Error("Failed to start media playback: %v", err)
		}
	}

	// Run post-lock command if configured
	if err := l.helper.RunPostLockCommand(); err != nil {
		Error("Failed to run post-lock command: %v", err)
	}

	return nil
}

// hideCursor hides the mouse cursor
func (l *X11Locker) hideCursor() error {
	Info("Hiding mouse cursor")
	// Create an invisible cursor
	cursor, err := xproto.NewCursorId(l.conn)
	if err != nil {
		Error("Failed to allocate cursor ID: %v", err)
		return fmt.Errorf("failed to allocate cursor ID: %v", err)
	}
	Debug("Cursor ID allocated: %d", cursor)

	// Get the default Pixmap with depth 1 (bitmap)
	pixmap, err := xproto.NewPixmapId(l.conn)
	if err != nil {
		Error("Failed to allocate pixmap ID: %v", err)
		return fmt.Errorf("failed to allocate pixmap ID: %v", err)
	}
	Debug("Pixmap ID allocated: %d", pixmap)

	// Create an empty 1x1 pixmap
	Debug("Creating 1x1 transparent pixmap")
	err = xproto.CreatePixmapChecked(
		l.conn,
		1, // Depth 1 for bitmap
		pixmap,
		xproto.Drawable(l.screen.Root),
		1, 1, // 1x1 pixel
	).Check()
	if err != nil {
		Error("Failed to create pixmap: %v", err)
		return fmt.Errorf("failed to create pixmap: %v", err)
	}
	Debug("Pixmap created successfully")

	// Create an invisible cursor from this pixmap
	Debug("Creating invisible cursor from pixmap")
	err = xproto.CreateCursorChecked(
		l.conn,
		cursor,
		pixmap,
		pixmap,
		0, 0, 0, // Black foreground
		0, 0, 0, // Black background
		0, 0, // Hotspot at 0,0
	).Check()
	if err != nil {
		xproto.FreePixmap(l.conn, pixmap)
		Error("Failed to create cursor: %v", err)
		return fmt.Errorf("failed to create cursor: %v", err)
	}
	Debug("Invisible cursor created successfully")

	// Free the pixmap since we no longer need it
	Debug("Freeing pixmap")
	xproto.FreePixmap(l.conn, pixmap)

	// Associate this cursor with our window
	Debug("Associating invisible cursor with window")
	err = xproto.ChangeWindowAttributesChecked(
		l.conn,
		l.window,
		xproto.CwCursor,
		[]uint32{uint32(cursor)},
	).Check()
	if err != nil {
		Error("Failed to set invisible cursor: %v", err)
		return fmt.Errorf("failed to set invisible cursor: %v", err)
	}
	Debug("Cursor set for window")

	// Additionally, we should hide the system cursor using XFixes
	Debug("Using XFixes to hide system cursor")
	xfixes.HideCursor(l.conn, l.screen.Root)
	Info("Cursor hidden successfully")

	return nil
}

// handleKeyPress handles key press events
func (l *X11Locker) handleKeyPress(e xproto.KeyPressEvent) {
	Debug("Handling key press event: keycode=%d", e.Detail)
	// Check if we're in a lockout period using the lockout manager
	if l.lockoutManager.IsLockedOut() {
		Debug("In lockout mode, limited key handling")
		// During lockout, only allow Escape or Q for debug exit
		keySyms := xproto.GetKeyboardMapping(l.conn, e.Detail, 1)
		reply, err := keySyms.Reply()
		if err != nil {
			Error("Error getting keyboard mapping: %v", err)
			return
		}

		// Check for debug exit keys
		if len(reply.Keysyms) > 0 {
			keySym := reply.Keysyms[0]
			Debug("Keysym during lockout: 0x%x", keySym)
			// ESC key or Q key (lowercase or uppercase)
			if l.config.DebugExit && (keySym == 0xff1b || keySym == 0x71 || keySym == 0x51) {
				Info("Debug exit triggered during lockout")
				l.isLocked = false
				return
			}

			// For regular Escape during lockout, just clear password
			if keySym == 0xff1b { // Escape key
				Debug("Escape pressed during lockout, clearing password")
				l.passwordBuf = ""
				l.passwordDots = make([]bool, 0)
			}
		}

		// For all other keys, do nothing during lockout
		return
	}

	// Get the keysym for this keycode
	keySyms := xproto.GetKeyboardMapping(l.conn, e.Detail, 1)
	reply, err := keySyms.Reply()
	if err != nil {
		Error("Error getting keyboard mapping: %v", err)
		return
	}

	// Process based on keysym
	if len(reply.Keysyms) > 0 {
		keySym := reply.Keysyms[0]
		Debug("Keysym: 0x%x", keySym)

		// Check for debug exit key first
		if l.config.DebugExit && (keySym == 0xff1b || keySym == 0x71 || keySym == 0x51) { // ESC or Q/q
			Info("Debug exit triggered")
			l.isLocked = false
			return
		}

		// Regular key handling
		switch keySym {
		case 0xff0d, 0xff8d: // Return, KP_Enter
			Debug("Enter key pressed, attempting authentication")
			// Try to authenticate
			l.authenticate()

		case 0xff08: // BackSpace
			Debug("Backspace pressed, removing last character")
			// Delete last character
			if len(l.passwordBuf) > 0 {
				l.passwordBuf = l.passwordBuf[:len(l.passwordBuf)-1]
				if len(l.passwordDots) > 0 {
					l.passwordDots = l.passwordDots[:len(l.passwordDots)-1]
				}
			}

		case 0xff1b: // Escape
			Debug("Escape pressed, clearing password")
			// Clear password
			l.passwordBuf = ""
			l.passwordDots = make([]bool, 0)

		default:
			// Only add printable characters
			if keySym >= 0x20 && keySym <= 0x7e {
				Debug("Adding character to password (keysym: 0x%x)", keySym)
				// Regular ASCII character
				l.passwordBuf += string(rune(keySym))

				// Add a new dot
				if len(l.passwordDots) < l.maxDots {
					l.passwordDots = append(l.passwordDots, true)
				}
			}
		}
	}
}

// authenticate attempts to validate the entered password
func (l *X11Locker) authenticate() {
	Info("Attempting authentication")
	// Check if we're in a lockout period using the lockout manager
	if l.lockoutManager.IsLockedOut() {
		// Still in lockout period, don't even attempt authentication
		remainingTime := l.lockoutManager.GetRemainingTime().Round(time.Second)
		Info("Authentication locked out for another %v", remainingTime)
		l.passwordBuf = ""

		// Keep the dots for shake animation
		// We'll clear them after the animation

		// Shake the password field to indicate lockout
		go l.shakePasswordField()
		return
	}

	// Add debug log for password attempt (don't log actual password)
	Info("Attempting authentication with password of length: %d", len(l.passwordBuf))

	// Try to authenticate using PAM
	result := l.helper.authenticator.Authenticate(l.passwordBuf)

	// Detailed logging of authentication result
	Info("Authentication result: success=%v, message=%s", result.Success, result.Message)

	if result.Success {
		// Authentication successful, unlock and reset counters
		l.isLocked = false
		l.lockoutManager.ResetLockout()

		// Unpause media if enabled
		if err := l.helper.UnpauseMediaIfEnabled(); err != nil {
			Warn("Failed to unpause media: %v", err)
		}

		Info("Authentication successful, unlocking screen")
	} else {
		// Authentication failed, use the lockout manager to handle the failed attempt
		lockoutActive, lockoutDuration, _ := l.lockoutManager.HandleFailedAttempt()
		Info("lockOutDuration: %d", lockoutDuration)

		// Clear password
		l.passwordBuf = ""

		// If lockout was activated, update the UI
		if lockoutActive {
			// Make sure the lockout message is displayed
			Info("Lockout activated until: %v", l.lockoutManager.GetLockoutUntil())
			l.drawLockoutMessage()
		}

		// Shake the password field to indicate failure
		go l.shakePasswordField()
	}
}

// shakePasswordField animates the password field to indicate failed authentication
func (l *X11Locker) shakePasswordField() {
	Debug("Starting password field shake animation")
	// Number of shake iterations
	iterations := 5
	// Shake distance in pixels
	distance := int16(10)
	// Time between movements in milliseconds
	delay := 50 * time.Millisecond

	// Get current position info for dots
	centerX := int16(l.width / 2)
	dotSpacing := int16(20)
	dotRadius := uint16(6)
	totalDots := len(l.passwordDots)

	Debug("Shake animation parameters: centerX=%d, totalDots=%d, dotSpacing=%d", centerX, totalDots, dotSpacing)

	// Calculate starting X position to center the dots
	startX := centerX - (int16(totalDots) * dotSpacing / 2)

	// Perform the shake animation
	for i := 0; i < iterations; i++ {
		Debug("Shake iteration %d of %d", i+1, iterations)
		// Move right
		for j, dotWid := range l.dotWindows {
			x := startX + int16(j)*dotSpacing - int16(dotRadius) + distance
			Debug("Moving dot %d right to x=%d", j, x)
			xproto.ConfigureWindow(l.conn, dotWid, xproto.ConfigWindowX, []uint32{uint32(x)})
		}
		time.Sleep(delay)

		// Move left
		for j, dotWid := range l.dotWindows {
			x := startX + int16(j)*dotSpacing - int16(dotRadius) - distance
			Debug("Moving dot %d left to x=%d", j, x)
			xproto.ConfigureWindow(l.conn, dotWid, xproto.ConfigWindowX, []uint32{uint32(x)})
		}
		time.Sleep(delay)

		// Move back to center
		for j, dotWid := range l.dotWindows {
			x := startX + int16(j)*dotSpacing - int16(dotRadius)
			Debug("Moving dot %d back to center x=%d", j, x)
			xproto.ConfigureWindow(l.conn, dotWid, xproto.ConfigWindowX, []uint32{uint32(x)})
		}
		time.Sleep(delay)
	}

	// Clear password dots after animation
	Debug("Shake animation complete, clearing password dots")
	l.passwordDots = make([]bool, 0)
	l.clearPasswordDots()
}

// drawUI draws the user interface
func (l *X11Locker) drawUI() {
	Debug("Drawing UI")
	// Check if we're in lockout mode using the lockout manager
	if l.lockoutManager.IsLockedOut() {
		Debug("In lockout mode, drawing lockout message")
		// Draw lockout message
		l.drawLockoutMessage()
	}

	// Draw the password UI
	Debug("Drawing password UI")
	l.drawPasswordUI()
}

// drawLockoutMessage displays a message indicating the system is locked out
func (l *X11Locker) drawLockoutMessage() {
	Info("Drawing lockout message")
	// Get the list of monitors
	monitors, err := l.detectMonitors()
	if err != nil {
		Error("Failed to detect monitors: %v", err)
		return
	}

	// If we don't already have message windows, create them
	if len(l.messageWindows) == 0 {
		Debug("Creating new message windows for lockout")
		// Create a window for each monitor
		for _, monitor := range monitors {
			wid, err := xproto.NewWindowId(l.conn)
			if err != nil {
				Error("Failed to create message window ID: %v", err)
				continue
			}
			l.messageWindows = append(l.messageWindows, wid)
			Debug("Allocated message window ID: %d", wid)

			// Create a window for this monitor with a semi-transparent background
			err = xproto.CreateWindowChecked(
				l.conn,
				l.screen.RootDepth,
				wid,
				l.screen.Root,
				int16(monitor.X), int16(monitor.Y),
				uint16(monitor.Width), uint16(monitor.Height),
				0, // No border
				xproto.WindowClassInputOutput,
				l.screen.RootVisual,
				xproto.CwOverrideRedirect|xproto.CwBackingStore|xproto.CwEventMask,
				[]uint32{
					1, // Override redirect
					1, // Backing store
					xproto.EventMaskExposure | xproto.EventMaskVisibilityChange,
				},
			).Check()

			if err != nil {
				Error("Failed to create message window: %v", err)
				continue
			}

			// Set window properties to keep it on top and make it fullscreen
			// First, set the window type to be a dialog
			atomName := "_NET_WM_WINDOW_TYPE"
			Debug("Interning atom: %s", atomName)
			windowTypeAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
			if err == nil && windowTypeAtom != nil {
				atomName = "_NET_WM_WINDOW_TYPE_DIALOG"
				Debug("Interning atom: %s", atomName)
				dialogAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
				if err == nil && dialogAtom != nil {
					Debug("Setting window type to dialog")
					xproto.ChangeProperty(l.conn, xproto.PropModeReplace, wid,
						windowTypeAtom.Atom, xproto.AtomAtom, 32, 1, []byte{
							byte(dialogAtom.Atom),
							byte(dialogAtom.Atom >> 8),
							byte(dialogAtom.Atom >> 16),
							byte(dialogAtom.Atom >> 24),
						})
				}
			}

			// Set the window state to be always on top
			atomName = "_NET_WM_STATE"
			Debug("Interning atom: %s", atomName)
			stateAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
			if err == nil && stateAtom != nil {
				atomName = "_NET_WM_STATE_ABOVE"
				Debug("Interning atom: %s", atomName)
				aboveAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
				if err == nil && aboveAtom != nil {
					Debug("Setting window state to always on top")
					xproto.ChangeProperty(l.conn, xproto.PropModeReplace, wid,
						stateAtom.Atom, xproto.AtomAtom, 32, 1, []byte{
							byte(aboveAtom.Atom),
							byte(aboveAtom.Atom >> 8),
							byte(aboveAtom.Atom >> 16),
							byte(aboveAtom.Atom >> 24),
						})
				}
			}
		}
	}

	// Get remaining lockout time using the lockout manager
	timeString := l.lockoutManager.FormatRemainingTime()
	Debug("Lockout remaining time: %s", timeString)

	// Parse the embedded font
	ttf, err := opentype.Parse(x11FontBytes)
	if err != nil {
		Error("Failed to parse embedded font: %v", err)
		return
	}

	// Create a large font face for the title
	titleFace, err := opentype.NewFace(ttf, &opentype.FaceOptions{
		Size:    96,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		Error("Failed to create title font face: %v", err)
		return
	}
	defer titleFace.Close()

	// Create a medium font face for the subtitle
	subtitleFace, err := opentype.NewFace(ttf, &opentype.FaceOptions{
		Size:    36,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		Error("Failed to create subtitle font face: %v", err)
		return
	}
	defer subtitleFace.Close()

	// Draw on each monitor
	for i, monitor := range monitors {
		if i >= len(l.messageWindows) {
			Error("No message window for monitor %d", i)
			continue
		}

		messageWindow := l.messageWindows[i]
		Debug("Drawing on monitor %d: x=%d, y=%d, width=%d, height=%d", i, monitor.X, monitor.Y, monitor.Width, monitor.Height)

		// Show the window
		Debug("Mapping lockout message window")
		xproto.MapWindow(l.conn, messageWindow)

		// Create an image to render the text
		img := image.NewRGBA(image.Rect(0, 0, monitor.Width, monitor.Height))

		// Draw "INTRUDER ALERT" at the center with large font
		title := "INTRUDER ALERT"
		titleBounds := font.MeasureString(titleFace, title)
		titleX := (monitor.Width - titleBounds.Round()) / 2
		titleY := monitor.Height/2 - 100

		d := &font.Drawer{
			Dst:  img,
			Src:  image.White,
			Face: titleFace,
			Dot:  fixed.P(titleX, titleY),
		}
		d.DrawString(title)

		// Draw "Security cooldown engaged" below with medium font
		subtitle := "Security cooldown engaged"
		subtitleBounds := font.MeasureString(subtitleFace, subtitle)
		subtitleX := (monitor.Width - subtitleBounds.Round()) / 2
		subtitleY := monitor.Height / 2

		d.Face = subtitleFace
		d.Dot = fixed.P(subtitleX, subtitleY)
		d.DrawString(subtitle)

		// Draw the timer below with large font
		timerBounds := font.MeasureString(titleFace, timeString)
		timerX := (monitor.Width - timerBounds.Round()) / 2
		timerY := monitor.Height/2 + 100

		d.Face = titleFace
		d.Dot = fixed.P(timerX, timerY)
		d.DrawString(timeString)

		// Create a pixmap to hold the rendered text
		pixmap, err := xproto.NewPixmapId(l.conn)
		if err != nil {
			Error("Failed to create pixmap: %v", err)
			continue
		}

		err = xproto.CreatePixmapChecked(
			l.conn,
			l.screen.RootDepth,
			pixmap,
			xproto.Drawable(messageWindow),
			uint16(monitor.Width), uint16(monitor.Height),
		).Check()
		if err != nil {
			Error("Failed to create pixmap: %v", err)
			continue
		}

		// Create a graphics context for the pixmap
		gc, err := xproto.NewGcontextId(l.conn)
		if err != nil {
			Error("Failed to create graphics context: %v", err)
			continue
		}

		err = xproto.CreateGCChecked(
			l.conn,
			gc,
			xproto.Drawable(pixmap),
			xproto.GcForeground|xproto.GcBackground,
			[]uint32{l.screen.WhitePixel, l.screen.BlackPixel},
		).Check()
		if err != nil {
			Error("Failed to create graphics context: %v", err)
			continue
		}

		// Copy the rendered image to the pixmap
		for y := 0; y < monitor.Height; y++ {
			for x := 0; x < monitor.Width; x++ {
				_, _, _, a := img.At(x, y).RGBA()
				if a > 0 {
					// Only draw non-transparent pixels
					xproto.PolyPoint(
						l.conn,
						xproto.CoordModeOrigin,
						xproto.Drawable(pixmap),
						gc,
						[]xproto.Point{{X: int16(x), Y: int16(y)}},
					)
				}
			}
		}

		// Copy the pixmap to the window
		xproto.CopyArea(
			l.conn,
			xproto.Drawable(pixmap),
			xproto.Drawable(messageWindow),
			l.gc,
			0, 0, 0, 0,
			uint16(monitor.Width), uint16(monitor.Height),
		)

		// Clean up
		xproto.FreePixmap(l.conn, pixmap)
		xproto.FreeGC(l.conn, gc)

		// Force window to be visible and on top
		Debug("Raising message window to top")
		xproto.ConfigureWindow(
			l.conn,
			messageWindow,
			xproto.ConfigWindowStackMode,
			[]uint32{xproto.StackModeAbove},
		)
	}

	// Start a timer to update the countdown if not already running
	if !l.lockoutManager.IsTimerRunning() {
		Debug("Starting timer for lockout countdown")
		l.lockoutManager.SetTimerRunning(true)

		go func() {
			Info("Starting lockout countdown timer")
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for l.lockoutManager.IsLockedOut() && l.isLocked {
				select {
				case <-ticker.C:
					Debug("Updating lockout timer display")
					// Only update the timer text, not recreate the whole window
					l.drawLockoutMessage()
				}
			}

			l.lockoutManager.SetTimerRunning(false)
			Info("Lockout countdown timer stopped")

			// When lockout is over, hide the windows if we're still locked
			if l.isLocked {
				Debug("Lockout ended, hiding message windows")
				for _, window := range l.messageWindows {
					xproto.UnmapWindow(l.conn, window)
				}
			}
		}()
	}
}

// drawPasswordUI draws the password entry UI
func (l *X11Locker) drawPasswordUI() {
	Debug("Drawing password entry UI")
	// Draw only the password dots
	l.drawPasswordDots()
}

// drawPasswordDots draws dots representing password characters
func (l *X11Locker) drawPasswordDots() {
	Debug("Drawing password dots: %d dots", len(l.passwordDots))
	// Calculate dot positions
	centerX := int16(l.width / 2)
	centerY := int16(l.height/2) + 70 // Below the center
	dotRadius := uint16(6)
	dotSpacing := int16(20)
	maxDots := l.maxDots

	// Calculate starting X position to center all potential dots
	startX := centerX - (int16(maxDots) * dotSpacing / 2)
	Debug("Dot layout: centerX=%d, centerY=%d, startX=%d, spacing=%d", centerX, centerY, startX, dotSpacing)

	// Make sure we have enough dot windows pre-created
	if len(l.dotWindows) < maxDots {
		Debug("Not enough dot windows, creating %d windows", maxDots)
		// We need to create more dot windows
		l.clearPasswordDots() // Clear existing ones first

		// Create all potential dot windows
		for i := 0; i < maxDots; i++ {
			x := startX + int16(i)*dotSpacing - int16(dotRadius)
			y := centerY - int16(dotRadius)
			Debug("Creating dot window %d at position x=%d, y=%d", i, x, y)

			// Create a new window ID for this dot
			dotWid, err := xproto.NewWindowId(l.conn)
			if err != nil {
				Error("Failed to create dot window: %v", err)
				continue
			}

			// Store the dot window ID
			l.dotWindows = append(l.dotWindows, dotWid)

			// Create a small window with white background for the dot
			mask := uint32(
				xproto.CwBackPixel |
					xproto.CwBorderPixel |
					xproto.CwOverrideRedirect)

			values := []uint32{
				l.screen.WhitePixel, // White background for the dot
				0x00000000,          // Black border
				1,                   // override redirect
			}

			err = xproto.CreateWindowChecked(
				l.conn,
				l.screen.RootDepth,
				dotWid,
				l.screen.Root,
				x, y,
				dotRadius*2, dotRadius*2,
				0, // No border width
				xproto.WindowClassInputOutput,
				l.screen.RootVisual,
				mask,
				values,
			).Check()

			if err != nil {
				Error("Failed to create dot window %d: %v", i, err)
				continue
			}
			Debug("Dot window %d created successfully", i)

			// Try to make the window stay on top
			atomName := "_NET_WM_STATE"
			atom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
			if err == nil && atom != nil {
				atomName = "_NET_WM_STATE_ABOVE"
				aboveAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
				if err == nil && aboveAtom != nil {
					Debug("Setting dot window %d to stay on top", i)
					xproto.ChangeProperty(l.conn, xproto.PropModeReplace, dotWid,
						atom.Atom, xproto.AtomAtom, 32, 1, []byte{
							byte(aboveAtom.Atom),
							byte(aboveAtom.Atom >> 8),
							byte(aboveAtom.Atom >> 16),
							byte(aboveAtom.Atom >> 24),
						})
				}
			}
		}
	}

	// Now just show/hide the appropriate windows rather than recreating them
	currentDots := len(l.passwordDots)
	Debug("Showing %d of %d dot windows", currentDots, len(l.dotWindows))

	// Show/hide windows based on current password length
	for i, dotWid := range l.dotWindows {
		if i < currentDots {
			// This dot should be visible
			Debug("Mapping dot window %d", i)
			xproto.MapWindow(l.conn, dotWid)

			// Make sure it's raised to the top
			Debug("Raising dot window %d to top", i)
			xproto.ConfigureWindow(
				l.conn,
				dotWid,
				xproto.ConfigWindowStackMode,
				[]uint32{xproto.StackModeAbove},
			)
		} else {
			// This dot should be hidden
			Debug("Unmapping dot window %d", i)
			xproto.UnmapWindow(l.conn, dotWid)
		}
	}
}

// clearPasswordDots removes any password dot windows
func (l *X11Locker) clearPasswordDots() {
	Info("Clearing all password dot windows: %d windows", len(l.dotWindows))
	for i, dotWid := range l.dotWindows {
		// Destroy the dot window
		Debug("Destroying dot window %d (ID: %d)", i, dotWid)
		xproto.DestroyWindow(l.conn, dotWid)
	}

	// Clear the list
	l.dotWindows = []xproto.Window{}
	Debug("All dot windows cleared")
}

// cleanup releases resources when unlocking
func (l *X11Locker) cleanup() {
	Info("Cleaning up resources")
	// Clear password dots
	Debug("Clearing password dots")
	l.clearPasswordDots()

	// Clear message windows if they exist
	if len(l.messageWindows) > 0 {
		Debug("Destroying message windows")
		for _, window := range l.messageWindows {
			xproto.DestroyWindow(l.conn, window)
		}
		l.messageWindows = []xproto.Window{}
	}

	// Stop media player
	Debug("Stopping media player")
	l.mediaPlayer.Stop()

	// Ungrab keyboard and pointer
	Debug("Ungrabbing keyboard")
	xproto.UngrabKeyboard(l.conn, xproto.TimeCurrentTime)
	Debug("Ungrabbing pointer")
	xproto.UngrabPointer(l.conn, xproto.TimeCurrentTime)

	// Destroy window
	Debug("Destroying main window")
	xproto.DestroyWindow(l.conn, l.window)

	// Close X connection
	Debug("Closing X connection")
	l.conn.Close()

	// Run post-lock command if configured
	if err := l.helper.RunPostLockCommand(); err != nil {
		Warn("Post-lock command error: %v", err)
	}

	Info("Cleanup completed")
}
