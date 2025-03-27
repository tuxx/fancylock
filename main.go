package main

import (
	"flag"
	"os"
	"path/filepath"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("c", "", "Path to configuration file")
	flag.StringVar(configPath, "config", "", "Path to configuration file")
	
	lockScreen := flag.Bool("l", false, "Lock the screen immediately")
	flag.BoolVar(lockScreen, "lock", false, "Lock the screen immediately")

	debugExit := flag.Bool("debug-exit", false, "Enable exit with ESC or Q key (for debugging)")

	// Add debug mode flag
	debugMode := flag.Bool("log", false, "Enable debug logging")
	
	flag.Parse()

	// Initialize the logger
	if *debugMode {
		InitLogger(LevelDebug, true)
		Debug("Debug logging enabled")
	} else {
		InitLogger(LevelError, false)
	}

	// Load default configuration
	config := DefaultConfig()
	config.LockScreen = *lockScreen
	config.DebugExit = *debugExit
	
	// Try to find and load config file
	if *configPath == "" {
		// Try default locations
		homeDir, err := os.UserHomeDir()
		if err == nil {
			defaultConfigPath := filepath.Join(homeDir, ".config", "fancylock", "config.json")
			if _, err := os.Stat(defaultConfigPath); err == nil {
				// Default config exists, use it
				Info("Using default config file: %s", defaultConfigPath)
				*configPath = defaultConfigPath
			}
		}
	}
	
	// If config file is provided or found, load it
	if *configPath != "" {
		err := LoadConfig(*configPath, &config)
		if err != nil {
			Error("loading config: %v", err)
			// Continue with default config
		}
	}

	// Initialize display server detection
	displayServer := DetectDisplayServer()
	Info("Detected display server: %s\n", displayServer)

	// Initialize the screen locker based on display server
	var locker ScreenLocker

	switch displayServer {
	case "wayland":
		// We'll implement Wayland support later
		Fatal("Wayland support not yet implemented")
	case "x11":
		locker = NewX11Locker(config)
	default:
		Fatal("Unsupported display server: %s", displayServer)
	}

	// If -l/--lock flag is set, lock immediately
	if config.LockScreen {
		if err := locker.Lock(); err != nil {
			Fatal("Failed to lock screen: %v", err)
		}
	} else {
		// Otherwise start in screensaver/idle monitor mode
		if err := locker.StartIdleMonitor(); err != nil {
			Fatal("Failed to start idle monitor: %v", err)
		}
	}
}

// DetectDisplayServer detects whether X11 or Wayland is being used
func DetectDisplayServer() string {
	// Check for Wayland session
	waylandDisplay := os.Getenv("WAYLAND_DISPLAY")
	if waylandDisplay != "" {
		return "wayland"
	}
	
	// Check for X11 session
	xdgSession := os.Getenv("XDG_SESSION_TYPE")
	if xdgSession == "x11" {
		return "x11"
	}
	
	// Default to X11 if can't determine
	return "x11"
}
