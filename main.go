package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("c", "", "Path to configuration file")
	flag.StringVar(configPath, "config", "", "Path to configuration file")

	lockScreen := flag.Bool("l", false, "Lock the screen immediately")
	flag.BoolVar(lockScreen, "lock", false, "Lock the screen immediately")

	helpFlag := flag.Bool("h", false, "Display help information")
	flag.BoolVar(helpFlag, "help", false, "Display help information")
	debugExit := flag.Bool("debug-exit", false, "Enable exit with ESC or Q key (for debugging)")

	// Add debug mode flag
	debugMode := flag.Bool("log", false, "Enable debug logging")

	// Set custom usage output
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "FancyLock: A media-playing screen locker\n\n")
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s -l                   # Lock screen immediately\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -c /path/to/config   # Use specific config file\n", os.Args[0])
	}

	flag.Parse()

	// Initialize the logger
	if *debugMode {
		InitLogger(LevelDebug, true)
		Debug("Debug logging enabled")
	} else {
		InitLogger(LevelError, false)
	}

	// Show help if explicitly requested or if no arguments provided and no action flags set
	if *helpFlag || (flag.NFlag() == 0 && !*lockScreen) {
		flag.Usage()
		return
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
				log.Printf("Using default config file: %s", defaultConfigPath)
				*configPath = defaultConfigPath
			}
		}
	}

	// If config file is provided or found, load it
	if *configPath != "" {
		err := LoadConfig(*configPath, &config)
		if err != nil {
			log.Printf("Error loading config: %v", err)
			// Continue with default config
		}
	}

	// Initialize display server detection
	displayServer := DetectDisplayServer()
	fmt.Printf("Detected display server: %s\n", displayServer)

	// Initialize the screen locker based on display server
	var locker ScreenLocker

	switch displayServer {
	case "wayland":
		// We'll implement Wayland support later
		log.Fatalf("Wayland support not yet implemented")
	case "x11":
		locker = NewX11Locker(config)
	default:
		log.Fatalf("Unsupported display server: %s", displayServer)
	}

	// If -l/--lock flag is set, lock immediately
	if config.LockScreen {
		if err := locker.Lock(); err != nil {
			log.Fatalf("Failed to lock screen: %v", err)
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
