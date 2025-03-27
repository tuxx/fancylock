package main

import (
	"fmt"
	"log"
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
    var err error

    l.conn, err = xgb.NewConn()
    if err != nil {
        return fmt.Errorf("failed to connect to X server: %v", err)
    }

    if err := screensaver.Init(l.conn); err != nil {
        return fmt.Errorf("failed to initialize screensaver extension: %v", err)
    }
    if err := dpms.Init(l.conn); err != nil {
        return fmt.Errorf("failed to initialize DPMS extension: %v", err)
    }
    if err := xfixes.Init(l.conn); err != nil {
        return fmt.Errorf("failed to initialize XFixes extension: %v", err)
    }
    if err := xtest.Init(l.conn); err != nil {
        return fmt.Errorf("failed to initialize XTest extension: %v", err)
    }

    setup := xproto.Setup(l.conn)
    l.screen = setup.DefaultScreen(l.conn)
    l.width = l.screen.WidthInPixels
    l.height = l.screen.HeightInPixels

    wid, err := xproto.NewWindowId(l.conn)
    if err != nil {
        return fmt.Errorf("failed to allocate window ID: %v", err)
    }
    l.window = wid

    // Create an InputOnly window for capturing keyboard and mouse
    err = xproto.CreateWindowChecked(
        l.conn,
        0, // Copy depth from parent
        l.window,
        l.screen.Root,
        0, 0, l.width, l.height,
        0,
        xproto.WindowClassInputOnly, // InputOnly window - completely invisible
        0, // Copy visual from parent
        xproto.CwOverrideRedirect | xproto.CwEventMask,
        []uint32{
            1, // Override redirect
            uint32(xproto.EventMaskKeyPress | 
                  xproto.EventMaskStructureNotify |
                  xproto.EventMaskButtonPress),
        },
    ).Check()
    if err != nil {
        return fmt.Errorf("failed to create window: %v", err)
    }

    // Set WM_NAME property for identification
    wmName := "FancyLock"
    xproto.ChangeProperty(l.conn, xproto.PropModeReplace, l.window,
        xproto.AtomWmName, xproto.AtomString, 8, uint32(len(wmName)), []byte(wmName))

    // Initialize GC for drawing password dots
    gcid, err := xproto.NewGcontextId(l.conn)
    if err != nil {
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
        return fmt.Errorf("failed to create graphics context: %v", err)
    }
    
    // Initialize GC for drawing text with white color
    textGcid, err := xproto.NewGcontextId(l.conn)
    if err != nil {
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
        return fmt.Errorf("failed to create text graphics context: %v", err)
    }
    
    l.dotWindows = []xproto.Window{}
    l.messageWindow = 0 // Initialize to 0 (invalid window ID)
    
    return nil

}

// detectMonitors detects connected monitors
func (l *X11Locker) detectMonitors() ([]Monitor, error) {
	// Try to use xrandr to get monitor information
	cmd := exec.Command("xrandr", "--current")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run xrandr: %v", err)
	}
	
	// Parse xrandr output
	monitors := []Monitor{}
	lines := strings.Split(string(output), "\n")
	
	for _, line := range lines {
		// Look for connected monitors with resolutions
		if strings.Contains(line, " connected") && strings.Contains(line, "x") {
			// Extract monitor position and size
			posInfo := line[strings.Index(line, "connected")+10:]
			posInfo = strings.TrimSpace(posInfo)
			
			// Primary monitor might have "primary" keyword before resolution
			if strings.HasPrefix(posInfo, "primary ") {
				posInfo = posInfo[8:]
			}
			
			var x, y, width, height int
			
			// Parse monitor position and size
			if strings.Contains(posInfo, "+") {
				// Format might be like "1920x1080+0+0" or "1080x1920+1920+0"
				parts := strings.Split(posInfo, "+")
				if len(parts) >= 3 {
					resolution := strings.Split(parts[0], "x")
					if len(resolution) >= 2 {
						width, _ = strconv.Atoi(resolution[0])
						height, _ = strconv.Atoi(resolution[1])
						x, _ = strconv.Atoi(parts[1])
						y, _ = strconv.Atoi(parts[2])
						
						monitors = append(monitors, Monitor{
							X:      x,
							Y:      y,
							Width:  width,
							Height: height,
						})
					}
				}
			}
		}
	}
	
	// If no monitors detected, fall back to single monitor
	if len(monitors) == 0 {
		monitors = append(monitors, Monitor{
			X:      0,
			Y:      0,
			Width:  int(l.width),
			Height: int(l.height),
		})
	}
	
	return monitors, nil
}

// NewX11Locker creates a new X11-based screen locker
func NewX11Locker(config Configuration) *X11Locker {
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
    // Check if another instance is already running
    if err := l.helper.EnsureSingleInstance(); err != nil {
        return err
    }
    
    // Initialize X11 connection and resources
    if err := l.Init(); err != nil {
        return err
    }
    
    // Detect monitors and set up environment for the media player
    monitors, err := l.detectMonitors()
    if err != nil {
        log.Printf("Warning: failed to detect monitors: %v", err)
        monitors = []Monitor{{
            X:      0,
            Y:      0,
            Width:  int(l.width),
            Height: int(l.height),
        }}
    }
    
    log.Printf("Detected %d monitors", len(monitors))
    
    // Pass monitor information to media player
    l.mediaPlayer.SetMonitors(monitors)
    
    // Set the window ID as an environment variable for the media player to use
    // Note: This is less important with InputOnly window but keeping for compatibility
    windowIDStr := fmt.Sprintf("%d", l.window)
    os.Setenv("FANCYLOCK_WINDOW_ID", windowIDStr)
    log.Printf("Setting window ID for media player: %s", windowIDStr)
    
    // Start playing media in background before showing lock screen
    if err := l.mediaPlayer.Start(); err != nil {
        log.Printf("Warning: failed to start media player: %v", err)
    } else {
        log.Printf("Media player started successfully")
    }
    
    // Give media player time to start fully before showing lock screen
    time.Sleep(500 * time.Millisecond)
    
    // Now map the window (make it visible)
    if err := xproto.MapWindowChecked(l.conn, l.window).Check(); err != nil {
        return fmt.Errorf("failed to map window: %v", err)
    }
    
    // Raise the window to the top
    if err := xproto.ConfigureWindowChecked(
        l.conn,
        l.window,
        xproto.ConfigWindowStackMode,
        []uint32{xproto.StackModeAbove},
    ).Check(); err != nil {
        return fmt.Errorf("failed to raise window: %v", err)
    }
    
    // Hide the cursor
    if err := l.hideCursor(); err != nil {
        log.Printf("Warning: failed to hide cursor: %v", err)
    }
    
    // Grab keyboard to prevent keyboard shortcuts from working
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
        return fmt.Errorf("failed to grab keyboard: %v", err)
    }
    if keyboardReply.Status != xproto.GrabStatusSuccess {
        return fmt.Errorf("failed to grab keyboard: status %d", keyboardReply.Status)
    }
    
    // Grab pointer to prevent mouse actions
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
        return fmt.Errorf("failed to grab pointer: %v", err)
    }
    if pointerReply.Status != xproto.GrabStatusSuccess {
        return fmt.Errorf("failed to grab pointer: status %d", pointerReply.Status)
    }
    
    // Set is locked flag
    l.isLocked = true
    
    // Main event loop
    for l.isLocked {
        // If we're in lockout mode, we need to keep updating the timer display
        if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
            // Redraw UI to update the lockout timer
            l.drawUI()
            
            // Wait for a short time before checking again
            time.Sleep(1000 * time.Millisecond) // Update once per second
            
            // Check for any pending events
            ev, err := l.conn.PollForEvent()
            if err != nil {
                log.Printf("Error polling for event: %v", err)
                continue
            }
            
            // Process the event if there is one
            if ev != nil {
                switch e := ev.(type) {
                case xproto.KeyPressEvent:
                    l.handleKeyPress(e)
                }
            }
            
            continue // Continue the loop to update the timer
        }
        
        // Regular event handling for non-lockout mode
        ev, err := l.conn.WaitForEvent()
        if err != nil {
            if strings.Contains(err.Error(), "BadRequest") {
                // This is likely a harmless error related to X11 extensions
                log.Printf("Ignoring X11 BadRequest error (this is usually harmless)")
            } else {
                log.Printf("Error waiting for event: %v", err)
            }
            // Continue the loop even after an error
            continue
        }
        
        if ev == nil {
            // Just a safety check
            time.Sleep(50 * time.Millisecond)
            continue
        }
        
        switch e := ev.(type) {
        case xproto.KeyPressEvent:
            l.handleKeyPress(e)
            l.drawPasswordUI()
            
        case xproto.ExposeEvent:
            // Redraw UI when exposed
            l.drawUI()
            
        case xproto.MappingNotifyEvent:
            // Handle keyboard mapping changes
            log.Printf("Keyboard mapping changed")
        }
    }
    
    // Clean up
    l.cleanup()
    return nil
}

// hideCursor hides the mouse cursor
func (l *X11Locker) hideCursor() error {
    // Create an invisible cursor
    cursor, err := xproto.NewCursorId(l.conn)
    if err != nil {
        return fmt.Errorf("failed to allocate cursor ID: %v", err)
    }
    
    // Get the default Pixmap with depth 1 (bitmap)
    pixmap, err := xproto.NewPixmapId(l.conn)
    if err != nil {
        return fmt.Errorf("failed to allocate pixmap ID: %v", err)
    }
    
    // Create an empty 1x1 pixmap
    err = xproto.CreatePixmapChecked(
        l.conn,
        1, // Depth 1 for bitmap
        pixmap,
        xproto.Drawable(l.screen.Root),
        1, 1, // 1x1 pixel
    ).Check()
    if err != nil {
        return fmt.Errorf("failed to create pixmap: %v", err)
    }
    
    // Create an invisible cursor from this pixmap
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
        return fmt.Errorf("failed to create cursor: %v", err)
    }
    
    // Free the pixmap since we no longer need it
    xproto.FreePixmap(l.conn, pixmap)
    
    // Associate this cursor with our window
    err = xproto.ChangeWindowAttributesChecked(
        l.conn,
        l.window,
        xproto.CwCursor,
        []uint32{uint32(cursor)},
    ).Check()
    if err != nil {
        return fmt.Errorf("failed to set invisible cursor: %v", err)
    }
    
    // Additionally, we should hide the system cursor using XFixes
    xfixes.HideCursor(l.conn, l.screen.Root)
    
    return nil
}

// handleKeyPress processes keyboard input
func (l *X11Locker) handleKeyPress(e xproto.KeyPressEvent) {
	// Check if we're in a lockout period
	if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
		// During lockout, only allow Escape or Q for debug exit
		keySyms := xproto.GetKeyboardMapping(l.conn, e.Detail, 1)
		reply, err := keySyms.Reply()
		if err != nil {
			log.Printf("Error getting keyboard mapping: %v", err)
			return
		}
		
		// Check for debug exit keys
		if len(reply.Keysyms) > 0 {
			keySym := reply.Keysyms[0]
			// ESC key or Q key (lowercase or uppercase)
			if l.config.DebugExit && (keySym == 0xff1b || keySym == 0x71 || keySym == 0x51) {
				log.Printf("Debug exit triggered during lockout")
				l.isLocked = false
				return
			}
			
			// For regular Escape during lockout, just clear password
			if keySym == 0xff1b { // Escape key
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
		log.Printf("Error getting keyboard mapping: %v", err)
		return
	}
	
	// Process based on keysym
	if len(reply.Keysyms) > 0 {
		keySym := reply.Keysyms[0]
		
		// Check for debug exit key first
		if l.config.DebugExit && (keySym == 0xff1b || keySym == 0x71 || keySym == 0x51) { // ESC or Q/q
			log.Printf("Debug exit triggered")
			l.isLocked = false
			return
		}
		
		// Regular key handling
		switch keySym {
		case 0xff0d, 0xff8d: // Return, KP_Enter
			// Try to authenticate
			l.authenticate()
			
		case 0xff08: // BackSpace
			// Delete last character
			if len(l.passwordBuf) > 0 {
				l.passwordBuf = l.passwordBuf[:len(l.passwordBuf)-1]
				if len(l.passwordDots) > 0 {
					l.passwordDots = l.passwordDots[:len(l.passwordDots)-1]
				}
			}
			
		case 0xff1b: // Escape
			// Clear password
			l.passwordBuf = ""
			l.passwordDots = make([]bool, 0)
			
		default:
			// Only add printable characters
			if keySym >= 0x20 && keySym <= 0x7e {
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
    // Check if we're in a lockout period
    if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
        // Still in lockout period, don't even attempt authentication
        remainingTime := l.lockoutUntil.Sub(time.Now()).Round(time.Second)
        log.Printf("Authentication locked out for another %v", remainingTime)
        l.passwordBuf = ""
        
        // Keep the dots for shake animation
        // We'll clear them after the animation
        
        // Shake the password field to indicate lockout
        go l.shakePasswordField()
        return
    }
    
    // If we were in a lockout but it's expired, clear the lockout state
    if l.lockoutActive && time.Now().After(l.lockoutUntil) {
        log.Printf("Lockout period has expired, clearing lockout state")
        l.lockoutActive = false
    }
    
    // Add debug log for password attempt (don't log actual password)
    log.Printf("Attempting authentication with password of length: %d", len(l.passwordBuf))
    
    // Try to authenticate using PAM
    result := l.helper.authenticator.Authenticate(l.passwordBuf)
    
    // Detailed logging of authentication result
    log.Printf("Authentication result: success=%v, message=%s", result.Success, result.Message)
    
    if result.Success {
        // Authentication successful, unlock and reset counters
        l.isLocked = false
        l.failedAttempts = 0
        l.lockoutActive = false
        log.Printf("Authentication successful, unlocking screen")
    } else {
        // Authentication failed, increment counter
        l.failedAttempts++
        l.lastFailureTime = time.Now()
        log.Printf("Authentication failed (%d/3 attempts): %s", l.failedAttempts, result.Message)
        
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
                log.Printf("Multiple rapid failures detected, locking out for 5 minutes")
            } else {
                // Standard lockout of 1 minute
                lockoutDuration = 1 * time.Minute
                log.Printf("Failed 3 attempts, locking out for 1 minute")
            }
            
            // Set the lockout time
            l.lockoutUntil = time.Now().Add(lockoutDuration)
            l.lockoutActive = true
            l.failedAttempts = 0 // Reset counter after implementing lockout
            
            // Make sure the lockout message is displayed
            log.Printf("Lockout activated until: %v", l.lockoutUntil)
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
    
    // Calculate starting X position to center the dots
    startX := centerX - (int16(totalDots) * dotSpacing / 2)
    
    // Perform the shake animation
    for i := 0; i < iterations; i++ {
        // Move right
        for j, dotWid := range l.dotWindows {
            x := startX + int16(j)*dotSpacing - int16(dotRadius) + distance
            xproto.ConfigureWindow(l.conn, dotWid, xproto.ConfigWindowX, []uint32{uint32(x)})
        }
        time.Sleep(delay)
        
        // Move left
        for j, dotWid := range l.dotWindows {
            x := startX + int16(j)*dotSpacing - int16(dotRadius) - distance
            xproto.ConfigureWindow(l.conn, dotWid, xproto.ConfigWindowX, []uint32{uint32(x)})
        }
        time.Sleep(delay)
        
        // Move back to center
        for j, dotWid := range l.dotWindows {
            x := startX + int16(j)*dotSpacing - int16(dotRadius)
            xproto.ConfigureWindow(l.conn, dotWid, xproto.ConfigWindowX, []uint32{uint32(x)})
        }
        time.Sleep(delay)
    }
    
    // Clear password dots after animation
    l.passwordDots = make([]bool, 0)
    l.clearPasswordDots()
}

// drawUI draws the complete UI
func (l *X11Locker) drawUI() {
    // Check if we're in lockout mode
    if l.lockoutActive && time.Now().Before(l.lockoutUntil) {
        // Draw lockout message
        l.drawLockoutMessage()
    }
    
    // Draw the password UI
    l.drawPasswordUI()
}

// drawLockoutMessage displays a message indicating the system is locked out
func (l *X11Locker) drawLockoutMessage() {
    // If we don't already have a message window, create one
    if l.messageWindow == 0 {
        wid, err := xproto.NewWindowId(l.conn)
        if err != nil {
            log.Printf("Failed to create message window ID: %v", err)
            return
        }
        l.messageWindow = wid
        
        // Center the window
        width := uint16(400)
        height := uint16(120)
        x := int16((l.width - width) / 2)
        y := int16((l.height - height) / 2) 
        
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
            xproto.CwBackPixel | xproto.CwBorderPixel | xproto.CwOverrideRedirect,
            []uint32{
                0x00333333, // Dark gray background
                0x00444444, // Slightly lighter border
                1,          // Override redirect
            },
        ).Check()
        
        if err != nil {
            log.Printf("Failed to create message window: %v", err)
            l.messageWindow = 0
            return
        }
        
        // Set window properties to keep it on top
        atomName := "_NET_WM_STATE"
        atom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
        if err == nil && atom != nil {
            atomName = "_NET_WM_STATE_ABOVE"
            aboveAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
            if err == nil && aboveAtom != nil {
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
    
    // Clear the window with our background color
    xproto.PolyFillRectangle(l.conn, xproto.Drawable(l.messageWindow), l.gc, []xproto.Rectangle{
        {0, 0, 400, 120},
    })
    
    // Draw the title - simple, centered and larger
    title := "LOCKED OUT"
    titleX := (400 - uint16(len(title)*8)) / 2  // Approximate width of 8 pixels per character
    
    xproto.ImageText8(l.conn, uint8(len(title)), 
        xproto.Drawable(l.messageWindow), l.textGC, 
        int16(titleX), 50, title)
    
    // Draw the timer - centered below the title
    timerText := fmt.Sprintf("Try again in %s", timeString)
    timerX := (400 - uint16(len(timerText)*8)) / 2
    
    xproto.ImageText8(l.conn, uint8(len(timerText)), 
        xproto.Drawable(l.messageWindow), l.textGC, 
        int16(timerX), 80, timerText)
    
    // Force window to be visible and on top
    xproto.ConfigureWindow(
        l.conn,
        l.messageWindow,
        xproto.ConfigWindowStackMode,
        []uint32{xproto.StackModeAbove},
    )
    
    // Sync to ensure changes are sent to the X server
    l.conn.Sync()
    
    // Start a timer to update the countdown
    if !l.timerRunning {
        l.timerRunning = true
        go func() {
            ticker := time.NewTicker(1 * time.Second)
            defer ticker.Stop()
            
            for l.lockoutActive && time.Now().Before(l.lockoutUntil) && l.isLocked {
                select {
                case <-ticker.C:
                    // Only update the timer text, not recreate the whole window
                    l.drawLockoutMessage()
                }
            }
            
            l.timerRunning = false
            
            // When lockout is over, hide the window if we're still locked
            if l.isLocked {
                xproto.UnmapWindow(l.conn, l.messageWindow)
            }
        }()
    }
}

// updateLockoutTimerDisplay updates the countdown timer in the lockout message window
func (l *X11Locker) updateLockoutTimerDisplay() {
    if l.messageWindow == 0 {
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
    
    // Create a stronger, clearer message
    message := "TOO MANY FAILED PASSWORD ATTEMPTS"
    timeMessage := fmt.Sprintf("LOCKED OUT FOR: %s", timeString)
    
    // Clear the window by filling it with the background color
    xproto.PolyFillRectangle(l.conn, xproto.Drawable(l.messageWindow), l.gc, []xproto.Rectangle{
        {0, 0, 400, 100},
    })
    
    // Draw the main message with a specific font if available
    // We're using the text graphics context (textGC) which is white
    xproto.ImageText8(l.conn, uint8(len(message)), 
        xproto.Drawable(l.messageWindow), l.textGC, 
        50, 40, // X, Y position within the window - more centered
        message)
    
    // Draw the time message below it - make it larger/more visible
    xproto.ImageText8(l.conn, uint8(len(timeMessage)), 
        xproto.Drawable(l.messageWindow), l.textGC, 
        85, 70, // X, Y position within the window - centered
        timeMessage)
    
    // Force a redraw/refresh
    xproto.ClearArea(l.conn, true, l.messageWindow, 0, 0, 0, 0)
    
    // Sync the X connection to ensure changes are sent to the server
    l.conn.Sync()
    
    // Log for debugging
    log.Printf("Updated lockout timer: %s", timeString)
}

// drawPasswordUI draws the password entry UI
func (l *X11Locker) drawPasswordUI() {
	// Draw only the password dots
	l.drawPasswordDots()
}

// drawPasswordDots draws dots representing password characters
func (l *X11Locker) drawPasswordDots() {
    // Calculate dot positions
    centerX := int16(l.width / 2)
    centerY := int16(l.height / 2) + 70 // Below the center
    dotRadius := uint16(6)
    dotSpacing := int16(20)
    maxDots := l.maxDots
    
    // Calculate starting X position to center all potential dots
    startX := centerX - (int16(maxDots) * dotSpacing / 2)
    
    // Make sure we have enough dot windows pre-created
    if len(l.dotWindows) < maxDots {
        // We need to create more dot windows
        l.clearPasswordDots() // Clear existing ones first
        
        // Create all potential dot windows
        for i := 0; i < maxDots; i++ {
            x := startX + int16(i)*dotSpacing - int16(dotRadius)
            y := centerY - int16(dotRadius)
            
            // Create a new window ID for this dot
            dotWid, err := xproto.NewWindowId(l.conn)
            if err != nil {
                log.Printf("Failed to create dot window: %v", err)
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
                log.Printf("Failed to create dot window: %v", err)
                continue
            }
            
            // Try to make the window stay on top
            atomName := "_NET_WM_STATE"
            atom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
            if err == nil && atom != nil {
                atomName = "_NET_WM_STATE_ABOVE"
                aboveAtom, err := xproto.InternAtom(l.conn, false, uint16(len(atomName)), atomName).Reply()
                if err == nil && aboveAtom != nil {
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
    
    // Show/hide windows based on current password length
    for i, dotWid := range l.dotWindows {
        if i < currentDots {
            // This dot should be visible
            xproto.MapWindow(l.conn, dotWid)
            
            // Make sure it's raised to the top
            xproto.ConfigureWindow(
                l.conn,
                dotWid,
                xproto.ConfigWindowStackMode,
                []uint32{xproto.StackModeAbove},
            )
        } else {
            // This dot should be hidden
            xproto.UnmapWindow(l.conn, dotWid)
        }
    }
}

// clearPasswordDots removes any password dot windows
func (l *X11Locker) clearPasswordDots() {
	for _, dotWid := range l.dotWindows {
		// Destroy the dot window
		xproto.DestroyWindow(l.conn, dotWid)
	}
	
	// Clear the list
	l.dotWindows = []xproto.Window{}
}

// cleanup releases resources when unlocking
func (l *X11Locker) cleanup() {
	// Clear password dots
	l.clearPasswordDots()
	
	// Clear message window if it exists
	if l.messageWindow != 0 {
		xproto.DestroyWindow(l.conn, l.messageWindow)
		l.messageWindow = 0
	}
	
	// Stop media player
	l.mediaPlayer.Stop()
	
	// Ungrab keyboard and pointer
	xproto.UngrabPointer(l.conn, xproto.TimeCurrentTime)
	xproto.UngrabKeyboard(l.conn, xproto.TimeCurrentTime)
	
	// Destroy window
	xproto.DestroyWindow(l.conn, l.window)
	
	// Close X connection
	l.conn.Close()
}

// StartIdleMonitor implements idle monitoring functionality
func (l *X11Locker) StartIdleMonitor() error {
	// Initialize X11 connection
	conn, err := xgb.NewConn()
	if err != nil {
		return fmt.Errorf("failed to connect to X server: %v", err)
	}
	
	// Initialize screensaver extension
	if err := screensaver.Init(conn); err != nil {
		conn.Close()
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
	
	log.Printf("Idle monitor started (timeout: %d seconds)", l.config.IdleTimeout)
	return nil
}

// StopIdleMonitor stops the idle monitoring
func (l *X11Locker) StopIdleMonitor() {
	if l.idleWatcher != nil {
		close(l.idleWatcher.stopChan)
		l.idleWatcher.conn.Close()
		l.idleWatcher = nil
	}
}

// Watch monitors for user inactivity
func (w *IdleWatcher) Watch() {
	// Start a ticker to check idle time
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-w.stopChan:
			return
		case <-ticker.C:
			// Query the idle time
			info, err := screensaver.QueryInfo(w.conn, xproto.Drawable(xproto.Setup(w.conn).DefaultScreen(w.conn).Root)).Reply()
			if err != nil {
				log.Printf("Error querying idle time: %v", err)
				continue
			}
			
			// Convert to milliseconds to seconds
			idleSeconds := time.Duration(info.MsSinceUserInput) * time.Millisecond
			
			// Check if we've reached the timeout
			if idleSeconds >= w.timeout {
				log.Printf("Idle timeout reached (%v), locking screen", idleSeconds)
				
				// Stop the watcher
				close(w.stopChan)
				
				// Lock the screen in a new goroutine to avoid deadlock
				go func() {
					// Use a clean lock command to avoid X server conflicts
					cmd := exec.Command(os.Args[0], "--lock")
					cmd.Start()
				}()
				
				return
			}
		}
	}
}
