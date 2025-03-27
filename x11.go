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

    // Let's not use _NET_WM_FULLSCREEN_MONITORS as it's causing issues
    // We'll ensure coverage using other mpv options instead

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
    
    l.dotWindows = []xproto.Window{}
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
        ev, err := l.conn.WaitForEvent()
        if err != nil {
            if strings.Contains(err.Error(), "BadRequest") {
                // This is likely a harmless error related to X11 extensions
                // Log it but don't spam the log
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
		
		// Handle special keys
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
    // Try to authenticate using PAM
    result := l.helper.authenticator.Authenticate(l.passwordBuf)
    
    if result.Success {
        // Authentication successful, unlock
        l.isLocked = false
        log.Printf("Authentication successful, unlocking screen")
    } else {
        // Authentication failed, clear password
        log.Printf("Authentication failed: %s", result.Message)
        l.passwordBuf = ""
        
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
	// Draw the password UI
	l.drawPasswordUI()
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
