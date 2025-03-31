package internal

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/dpms"
	"github.com/BurntSushi/xgb/screensaver"
	"github.com/BurntSushi/xgb/xfixes"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
)

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

	// Initialize GC for drawing text with white color
	Info("Initializing graphics context for text")
	textGcid, err := xproto.NewGcontextId(l.conn)
	if err != nil {
		Error("Failed to allocate text graphics context ID: %v", err)
		return fmt.Errorf("failed to allocate text graphics context ID: %v", err)
	}
	l.textGC = textGcid

	err = xproto.CreateGCChecked(
		l.conn,
		l.textGC,
		xproto.Drawable(l.screen.Root),
		xproto.GcForeground,
		[]uint32{l.screen.WhitePixel},
	).Check()
	if err != nil {
		Error("Failed to create text graphics context: %v", err)
		return fmt.Errorf("failed to create text graphics context: %v", err)
	}
	Info("Text graphics context initialized successfully")

	l.dotWindows = []xproto.Window{}
	l.messageWindow = 0 // Initialize to 0 (invalid window ID)

	Info("X11 initialization completed successfully")
	return nil
}

// detectMonitors detects connected monitors
func (l *X11Locker) detectMonitors() ([]Monitor, error) {
	Info("Attempting to detect monitors using xrandr")
	// Try to use xrandr to get monitor information
	cmd := exec.Command("xrandr", "--current")
	Debug("Executing command: %s %s", cmd.Path, strings.Join(cmd.Args, " "))

	output, err := cmd.CombinedOutput()
	if err != nil {
		Error("Failed to run xrandr: %v, output: %s", err, string(output))
		return nil, fmt.Errorf("failed to run xrandr: %v", err)
	}

	// Log the full xrandr output for debugging
	Debug("xrandr output:\n%s", string(output))

	// Parse xrandr output
	monitors := []Monitor{}
	lines := strings.Split(string(output), "\n")
	Info("Found %d lines in xrandr output", len(lines))

	for i, line := range lines {
		Debug("Processing line %d: %s", i, line)
		// Look for connected monitors with resolutions
		if strings.Contains(line, " connected") && strings.Contains(line, "x") {
			Debug("Found connected monitor line: %s", line)
			// Extract monitor position and size
			posInfo := line[strings.Index(line, "connected")+10:]
			posInfo = strings.TrimSpace(posInfo)
			Debug("Position info extracted: %s", posInfo)

			// Primary monitor might have "primary" keyword before resolution
			if strings.HasPrefix(posInfo, "primary ") {
				Debug("Detected primary monitor")
				posInfo = posInfo[8:]
				Debug("After removing 'primary' prefix: %s", posInfo)
			}

			var x, y, width, height int

			// Parse monitor position and size
			if strings.Contains(posInfo, "+") {
				Debug("Parsing position info: %s", posInfo)
				// Format might be like "1920x1080+0+0" or "1080x1920+1920+0"
				parts := strings.Split(posInfo, "+")
				if len(parts) >= 3 {
					Debug("Split into parts: %v", parts)
					resolution := strings.Split(parts[0], "x")
					if len(resolution) >= 2 {
						width, _ = strconv.Atoi(resolution[0])
						height, _ = strconv.Atoi(resolution[1])
						x, _ = strconv.Atoi(parts[1])
						y, _ = strconv.Atoi(parts[2])

						Debug("Parsed monitor: width=%d, height=%d, x=%d, y=%d", width, height, x, y)
						monitors = append(monitors, Monitor{
							X:      x,
							Y:      y,
							Width:  width,
							Height: height,
						})
						Info("Added monitor: width=%d, height=%d, x=%d, y=%d", width, height, x, y)
					} else {
						Debug("Failed to parse resolution part: %s", parts[0])
					}
				} else {
					Debug("Not enough parts after splitting by '+': %v", parts)
				}
			} else {
				Debug("No '+' found in position info: %s", posInfo)
			}
		}
	}

	// If no monitors detected, fall back to single monitor
	if len(monitors) == 0 {
		Info("No monitors detected via xrandr, falling back to single monitor with dimensions %dx%d", int(l.width), int(l.height))
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
		config:       config,
		helper:       NewLockHelper(config),
		mediaPlayer:  NewMediaPlayer(config),
		passwordBuf:  "",
		isLocked:     false,
		passwordDots: make([]bool, 0),
		maxDots:      20, // Maximum number of password dots to display
	}
}

// Lock implements the screen locking functionality
func (l *X11Locker) Lock() error {
	Info("Starting lock procedure")
	// Check if another instance is already running
	Info("Checking for other instances")
	if err := l.helper.EnsureSingleInstance(); err != nil {
		Error("Another instance is already running: %v", err)
		return err
	}
	Info("No other instances found")

	// Run pre-lock command if configured
	if err := l.helper.RunPreLockCommand(); err != nil {
		Warn("Pre-lock command error: %v", err)
		// Continue with locking even if the pre-lock command fails
	}

	// Initialize X11 connection and resources
	Info("Initializing X11 resources")
	if err := l.Init(); err != nil {
		Error("Failed to initialize X11: %v", err)
		return err
	}
	Info("X11 resources initialized successfully")

	// Detect monitors and set up environment for the media player
	Info("Detecting monitors")
	monitors, err := l.detectMonitors()
	if err != nil {
		Warn("Failed to detect monitors: %v", err)
		Info("Using fallback monitor configuration")
		monitors = []Monitor{{
			X:      0,
			Y:      0,
			Width:  int(l.width),
			Height: int(l.height),
		}}
	}

	Info("Detected %d monitors", len(monitors))

	// Pass monitor information to media player
	Info("Setting monitor information for media player")
	l.mediaPlayer.SetMonitors(monitors)

	// Set the window ID as an environment variable for the media player to use
	// Note: This is less important with InputOnly window but keeping for compatibility
	windowIDStr := fmt.Sprintf("%d", l.window)
	os.Setenv("FANCYLOCK_WINDOW_ID", windowIDStr)
	Info("Setting window ID for media player: %s", windowIDStr)

	// Start playing media in background before showing lock screen
	Info("Starting media player")
	if err := l.mediaPlayer.Start(); err != nil {
		Warn("Failed to start media player: %v", err)
	} else {
		Info("Media player started successfully")
	}

	// Give media player time to start fully before showing lock screen
	Info("Waiting for media player to initialize")
	time.Sleep(500 * time.Millisecond)

	// Now map the window (make it visible)
	Info("Mapping window (making it visible)")
	if err := xproto.MapWindowChecked(l.conn, l.window).Check(); err != nil {
		Error("Failed to map window: %v", err)
		return fmt.Errorf("failed to map window: %v", err)
	}
	Info("Window mapped successfully")

	// Raise the window to the top
	Info("Raising window to top")
	if err := xproto.ConfigureWindowChecked(
		l.conn,
		l.window,
		xproto.ConfigWindowStackMode,
		[]uint32{xproto.StackModeAbove},
	).Check(); err != nil {
		Error("Failed to raise window: %v", err)
		return fmt.Errorf("failed to raise window: %v", err)
	}
	Info("Window raised successfully")

	// Hide the cursor
	Info("Attempting to hide cursor")
	if err := l.hideCursor(); err != nil {
		Warn("Failed to hide cursor: %v", err)
	} else {
		Info("Cursor hidden successfully")
	}

	// Grab keyboard to prevent keyboard shortcuts from working
	Info("Grabbing keyboard")
	keyboard := xproto.GrabKeyboard(
		l.conn,
		true,
		l.window,
		xproto.TimeCurrentTime,
		xproto.GrabModeAsync,
		xproto.GrabModeAsync,
	)
	keyboardReply, err := keyboard.Reply()
	if err != nil {
		Error("Failed to grab keyboard: %v", err)
		return fmt.Errorf("failed to grab keyboard: %v", err)
	}
	if keyboardReply.Status != xproto.GrabStatusSuccess {
		Error("Failed to grab keyboard: status %d", keyboardReply.Status)
		return fmt.Errorf("failed to grab keyboard: status %d", keyboardReply.Status)
	}
	Info("Keyboard grabbed successfully")

	// Grab pointer to prevent mouse actions
	Info("Grabbing pointer (mouse)")
	pointer := xproto.GrabPointer(
		l.conn,
		true,
		l.window,
		xproto.EventMaskButtonPress,
		xproto.GrabModeAsync,
		xproto.GrabModeAsync,
		l.window,
		xproto.CursorNone,
		xproto.TimeCurrentTime,
	)
	pointerReply, err := pointer.Reply()
	if err != nil {
		Error("Failed to grab pointer: %v", err)
		return fmt.Errorf("failed to grab pointer: %v", err)
	}
	if pointerReply.Status != xproto.GrabStatusSuccess {
		Error("Failed to grab pointer: status %d", pointerReply.Status)
		return fmt.Errorf("failed to grab pointer: status %d", pointerReply.Status)
	}
	Info("Pointer grabbed successfully")

	// Set is locked flag
	l.isLocked = true
	Info("Screen lock activated")

	// Main event loop
	Info("Entering main event loop")
	for l.isLocked {
		// If we're in lockout mode, we need to keep updating the timer display
		if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
			Debug("In lockout mode, updating timer")
			// Redraw UI to update the lockout timer
			l.drawUI()

			// Wait for a short time before checking again
			time.Sleep(1000 * time.Millisecond) // Update once per second

			// Check for any pending events
			ev, err := l.conn.PollForEvent()
			if err != nil {
				Error("Error polling for event: %v", err)
				continue
			}

			// Process the event if there is one
			if ev != nil {
				Debug("Received event during lockout: %T", ev)
				switch e := ev.(type) {
				case xproto.KeyPressEvent:
					Debug("KeyPress event during lockout: keycode=%d", e.Detail)
					l.handleKeyPress(e)
				}
			}

			continue // Continue the loop to update the timer
		}

		// Regular event handling for non-lockout mode
		Debug("Waiting for X11 event")
		ev, err := l.conn.WaitForEvent()
		if err != nil {
			if strings.Contains(err.Error(), "BadRequest") {
				// This is likely a harmless error related to X11 extensions
				Info("Ignoring X11 BadRequest error (this is usually harmless)")
			} else {
				Error("Error waiting for event: %v", err)
			}
			// Continue the loop even after an error
			continue
		}

		if ev == nil {
			// Just a safety check
			Debug("Received nil event, sleeping briefly")
			time.Sleep(50 * time.Millisecond)
			continue
		}

		Debug("Received event: %T", ev)
		switch e := ev.(type) {
		case xproto.KeyPressEvent:
			Debug("KeyPress event: keycode=%d", e.Detail)
			l.handleKeyPress(e)
			l.drawPasswordUI()

		case xproto.ExposeEvent:
			Debug("Expose event: x=%d, y=%d, width=%d, height=%d", e.X, e.Y, e.Width, e.Height)
			// Redraw UI when exposed
			l.drawUI()

		case xproto.MappingNotifyEvent:
			// Handle keyboard mapping changes
			Debug("MappingNotify event: request=%d", e.Request)
			Info("Keyboard mapping changed")
		}
	}

	// Clean up
	Info("Lock deactivated, cleaning up resources")
	l.cleanup()
	Info("Cleanup completed")
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

// handleKeyPress processes keyboard input
func (l *X11Locker) handleKeyPress(e xproto.KeyPressEvent) {
	Debug("Handling key press event: keycode=%d", e.Detail)
	// Check if we're in a lockout period
	if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
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

// authenticate attempts to verify the password
func (l *X11Locker) authenticate() {
	Info("Attempting authentication")
	// Check if we're in a lockout period
	if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
		// Still in lockout period, don't even attempt authentication
		remainingTime := l.lockoutUntil.Sub(time.Now()).Round(time.Second)
		Info("Authentication locked out for another %v", remainingTime)
		l.passwordBuf = ""

		// Keep the dots for shake animation
		// We'll clear them after the animation

		// Shake the password field to indicate lockout
		go l.shakePasswordField()
		return
	}

	// If we were in a lockout but it's expired, clear the lockout state
	if l.lockoutActive && time.Now().After(l.lockoutUntil) {
		Info("Lockout period has expired, clearing lockout state")
		l.lockoutActive = false
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
		l.failedAttempts = 0
		l.lockoutActive = false
		Info("Authentication successful, unlocking screen")
	} else {
		// Authentication failed, increment counter
		l.failedAttempts++
		l.lastFailureTime = time.Now()
		Info("Authentication failed (%d/3 attempts): %s", l.failedAttempts, result.Message)

		// Clear password
		l.passwordBuf = ""

		// Check if we should implement a lockout
		if l.failedAttempts >= 3 {
			// Determine the lockout duration based on recent failures
			var lockoutDuration time.Duration

			// Check if the 3 failures happened within a short period (e.g., 5 minutes)
			if time.Since(l.lastFailureTime) < 5*time.Minute {
				// Repeated quick failures, implement the longer 5-minute lockout
				lockoutDuration = 5 * time.Minute
				Info("Multiple rapid failures detected, locking out for 5 minutes")
			} else {
				// Standard lockout of 1 minute
				lockoutDuration = 1 * time.Minute
				Info("Failed 3 attempts, locking out for 1 minute")
			}

			// Set the lockout time
			l.lockoutUntil = time.Now().Add(lockoutDuration)
			l.lockoutActive = true
			l.failedAttempts = 0 // Reset counter after implementing lockout

			// Make sure the lockout message is displayed
			Info("Lockout activated until: %v", l.lockoutUntil)
			l.drawLockoutMessage()
		}

		// Keep the dots for shake animation
		// We'll clear them after the animation

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

// drawUI draws the complete UI
func (l *X11Locker) drawUI() {
	Debug("Drawing UI")
	// Check if we're in lockout mode
	if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
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
	// If we don't already have a message window, create one
	if l.messageWindow == 0 {
		Debug("Creating new message window for lockout")
		wid, err := xproto.NewWindowId(l.conn)
		if err != nil {
			Error("Failed to create message window ID: %v", err)
			return
		}
		l.messageWindow = wid
		Debug("Allocated message window ID: %d", l.messageWindow)

		// Center the window
		width := uint16(400)
		height := uint16(120)
		x := int16((l.width - width) / 2)
		y := int16((l.height - height) / 2)
		Debug("Message window position: x=%d, y=%d, width=%d, height=%d", x, y, width, height)

		// Create the window with a dark gray background
		err = xproto.CreateWindowChecked(
			l.conn,
			l.screen.RootDepth,
			l.messageWindow,
			l.screen.Root,
			x, y, width, height,
			2, // Thin border for a cleaner look
			xproto.WindowClassInputOutput,
			l.screen.RootVisual,
			xproto.CwBackPixel|xproto.CwBorderPixel|xproto.CwOverrideRedirect,
			[]uint32{
				0x00333333, // Dark gray background
				0x00444444, // Slightly lighter border
				1,          // Override redirect
			},
		).Check()

		if err != nil {
			Error("Failed to create message window: %v", err)
			l.messageWindow = 0
			return
		}
		Info("Message window created successfully")

		// Set window properties to keep it on top
		atomName := "_NET_WM_STATE"
		Debug("Interning atom: %s", atomName)
		atom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
		if err == nil && atom != nil {
			atomName = "_NET_WM_STATE_ABOVE"
			Debug("Interning atom: %s", atomName)
			aboveAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
			if err == nil && aboveAtom != nil {
				Debug("Setting window state to always on top")
				xproto.ChangeProperty(l.conn, xproto.PropModeReplace, l.messageWindow,
					atom.Atom, xproto.AtomAtom, 32, 1, []byte{
						byte(aboveAtom.Atom),
						byte(aboveAtom.Atom >> 8),
						byte(aboveAtom.Atom >> 16),
						byte(aboveAtom.Atom >> 24),
					})
			}
		}
	}

	// Show the window
	Debug("Mapping lockout message window")
	xproto.MapWindow(l.conn, l.messageWindow)

	// Get remaining lockout time
	remainingTime := l.lockoutUntil.Sub(time.Now())
	if remainingTime < 0 {
		remainingTime = 0
	}

	// Format the time nicely
	minutes := int(remainingTime.Minutes())
	seconds := int(remainingTime.Seconds()) % 60
	timeString := fmt.Sprintf("%02d:%02d", minutes, seconds)
	Debug("Lockout remaining time: %s", timeString)

	// Clear the window with our background color
	Debug("Clearing message window")
	xproto.PolyFillRectangle(l.conn, xproto.Drawable(l.messageWindow), l.gc, []xproto.Rectangle{
		{0, 0, 400, 120},
	})

	// Draw the title - simple, centered and larger
	title := "LOCKED OUT"
	titleX := (400 - uint16(len(title)*8)) / 2 // Approximate width of 8 pixels per character
	Debug("Drawing title '%s' at x=%d", title, titleX)

	xproto.ImageText8(l.conn, uint8(len(title)),
		xproto.Drawable(l.messageWindow), l.textGC,
		int16(titleX), 50, title)

	// Draw the timer - centered below the title
	timerText := fmt.Sprintf("Try again in %s", timeString)
	timerX := (400 - uint16(len(timerText)*8)) / 2
	Debug("Drawing timer text '%s' at x=%d", timerText, timerX)

	xproto.ImageText8(l.conn, uint8(len(timerText)),
		xproto.Drawable(l.messageWindow), l.textGC,
		int16(timerX), 80, timerText)

	// Force window to be visible and on top
	Debug("Raising message window to top")
	xproto.ConfigureWindow(
		l.conn,
		l.messageWindow,
		xproto.ConfigWindowStackMode,
		[]uint32{xproto.StackModeAbove},
	)

	// Sync to ensure changes are sent to the X server
	Debug("Syncing X connection")
	l.conn.Sync()

	// Start a timer to update the countdown
	if !l.timerRunning {
		Debug("Starting timer for lockout countdown")
		l.timerRunning = true
		go func() {
			Info("Starting lockout countdown timer")
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for l.lockoutActive && time.Now().Before(l.lockoutUntil) && l.isLocked {
				select {
				case <-ticker.C:
					Debug("Updating lockout timer display")
					// Only update the timer text, not recreate the whole window
					l.drawLockoutMessage()
				}
			}

			l.timerRunning = false
			Info("Lockout countdown timer stopped")

			// When lockout is over, hide the window if we're still locked
			if l.isLocked {
				Debug("Lockout ended, hiding message window")
				xproto.UnmapWindow(l.conn, l.messageWindow)
			}
		}()
	}
}

// updateLockoutTimerDisplay updates the countdown timer in the lockout message window
func (l *X11Locker) updateLockoutTimerDisplay() {
	Debug("Updating lockout timer display")
	if l.messageWindow == 0 {
		Debug("No message window exists, can't update timer")
		return
	}

	// Get remaining lockout time
	remainingTime := l.lockoutUntil.Sub(time.Now())
	if remainingTime < 0 {
		remainingTime = 0
	}

	// Format the time nicely - show minutes and seconds
	minutes := int(remainingTime.Minutes())
	seconds := int(remainingTime.Seconds()) % 60
	timeString := fmt.Sprintf("%02d:%02d", minutes, seconds)
	Debug("Updated lockout remaining time: %s", timeString)

	// Create a stronger, clearer message
	message := "TOO MANY FAILED PASSWORD ATTEMPTS"
	timeMessage := fmt.Sprintf("LOCKED OUT FOR: %s", timeString)

	// Clear the window by filling it with the background color
	Debug("Clearing message window")
	xproto.PolyFillRectangle(l.conn, xproto.Drawable(l.messageWindow), l.gc, []xproto.Rectangle{
		{0, 0, 400, 100},
	})

	// Draw the main message with a specific font if available
	// We're using the text graphics context (textGC) which is white
	Debug("Drawing main message: %s", message)
	xproto.ImageText8(l.conn, uint8(len(message)),
		xproto.Drawable(l.messageWindow), l.textGC,
		50, 40, // X, Y position within the window - more centered
		message)

	// Draw the time message below it - make it larger/more visible
	Debug("Drawing time message: %s", timeMessage)
	xproto.ImageText8(l.conn, uint8(len(timeMessage)),
		xproto.Drawable(l.messageWindow), l.textGC,
		85, 70, // X, Y position within the window - centered
		timeMessage)

	// Force a redraw/refresh
	Debug("Clearing message window area to force refresh")
	xproto.ClearArea(l.conn, true, l.messageWindow, 0, 0, 0, 0)

	// Sync the X connection to ensure changes are sent to the server
	Debug("Syncing X connection")
	l.conn.Sync()

	// Log for debugging
	Info("Updated lockout timer: %s", timeString)
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

	// Clear message window if it exists
	if l.messageWindow != 0 {
		Debug("Destroying message window")
		xproto.DestroyWindow(l.conn, l.messageWindow)
		l.messageWindow = 0
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

// StartIdleMonitor implements idle monitoring functionality
func (l *X11Locker) StartIdleMonitor() error {
	Info("Starting idle monitor")
	// Initialize X11 connection
	Info("Creating new X connection for idle monitor")
	conn, err := xgb.NewConn()
	if err != nil {
		Error("Failed to connect to X server for idle monitor: %v", err)
		return fmt.Errorf("failed to connect to X server: %v", err)
	}

	// Initialize screensaver extension
	Info("Initializing screensaver extension for idle monitor")
	if err := screensaver.Init(conn); err != nil {
		conn.Close()
		Error("Failed to initialize screensaver extension: %v", err)
		return fmt.Errorf("failed to initialize screensaver extension: %v", err)
	}

	// Create idle watcher
	l.idleWatcher = &IdleWatcher{
		conn:         conn,
		timeout:      time.Duration(l.config.IdleTimeout) * time.Second,
		stopChan:     make(chan struct{}),
		parentLocker: l,
	}

	// Start watching in a goroutine
	go l.idleWatcher.Watch()

	Info("Idle monitor started (timeout: %d seconds)", l.config.IdleTimeout)
	return nil
}

// StopIdleMonitor stops the idle monitoring
func (l *X11Locker) StopIdleMonitor() {
	Info("Stopping idle monitor")
	if l.idleWatcher != nil {
		Debug("Closing idle watcher stop channel")
		close(l.idleWatcher.stopChan)
		Debug("Closing idle watcher X connection")
		l.idleWatcher.conn.Close()
		l.idleWatcher = nil
		Info("Idle monitor stopped")
	} else {
		Debug("No idle watcher to stop")
	}
}

// Watch monitors for user inactivity
func (w *IdleWatcher) Watch() {
	Info("Idle watcher started, timeout: %v", w.timeout)
	// Start a ticker to check idle time
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopChan:
			Info("Idle watcher received stop signal")
			return
		case <-ticker.C:
			// Query the idle time
			Debug("Querying idle time")
			info, err := screensaver.QueryInfo(w.conn, xproto.Drawable(xproto.Setup(w.conn).DefaultScreen(w.conn).Root)).Reply()
			if err != nil {
				Error("Error querying idle time: %v", err)
				continue
			}

			// Convert to milliseconds to seconds
			idleSeconds := time.Duration(info.MsSinceUserInput) * time.Millisecond
			Debug("Current idle time: %v", idleSeconds)

			// Check if we've reached the timeout
			if idleSeconds >= w.timeout {
				Info("Idle timeout reached (%v), locking screen", idleSeconds)

				// Stop the watcher
				close(w.stopChan)

				// Lock the screen in a new goroutine to avoid deadlock
				go func() {
					Debug("Starting lock command in separate process")
					// Use a clean lock command to avoid X server conflicts
					cmd := exec.Command(os.Args[0], "--lock")
					err := cmd.Start()
					if err != nil {
						Error("Failed to start lock command: %v", err)
					}
				}()

				return
			}
		}
	}
}
