package internal

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// NewMediaPlayer creates a new media player instance
func NewMediaPlayer(config Configuration) *MediaPlayer {
	return &MediaPlayer{
		config:       config,
		stopChan:     make(chan struct{}),
		doneChan:     make(chan bool, 1),
		mediaFiles:   []MediaFile{},
		currentProcs: []*exec.Cmd{},
		currentProc:  nil, // Initialize the new field
		running:      false,
		monitors:     []Monitor{},
	}
}

// killAllMedia safely stops all currently playing media
func (mp *MediaPlayer) killAllMedia() {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	for i, proc := range mp.currentProcs {
		if proc != nil && proc.Process != nil {
			// Kill the process
			proc.Process.Kill()
			Info("Killed media player process %d", i)
		}
	}

	// Allow some time for the processes to terminate
	if len(mp.currentProcs) > 0 {
		time.Sleep(100 * time.Millisecond)
		mp.currentProcs = []*exec.Cmd{}
	}
}

// SetMonitors sets the monitor information for the media player
func (mp *MediaPlayer) SetMonitors(monitors []Monitor) {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	mp.monitors = monitors
	Info("Media player configured with %d monitors", len(monitors))

	// If no monitors specified, add a default one
	if len(mp.monitors) == 0 {
		mp.monitors = append(mp.monitors, Monitor{
			X:      0,
			Y:      0,
			Width:  1920,
			Height: 1080,
		})
	}
}

// Start begins playing media files using mpv's playlist feature
func (mp *MediaPlayer) Start() error {
	mp.mutex.Lock()
	if mp.running {
		mp.mutex.Unlock()
		return nil // Already running
	}
	mp.running = true
	mp.mutex.Unlock()

	// Scan for media files
	if err := mp.scanMediaFiles(); err != nil {
		return fmt.Errorf("failed to scan media files: %v", err)
	}

	// Check if we found any media files
	if len(mp.mediaFiles) == 0 {
		return fmt.Errorf("no media files found in %s", mp.config.MediaDir)
	}

	// Start playback on each monitor
	for monitorIdx, monitor := range mp.monitors {
		err := mp.startPlaylistOnMonitor(monitor, monitorIdx)
		if err != nil {
			Error("Failed to start playlist on monitor %d: %v", monitorIdx, err)
			// Continue with other monitors even if one fails
		}
	}

	return nil
}

// startPlaylistOnMonitor starts an mpv instance with a shuffled playlist on a specific monitor
func (mp *MediaPlayer) startPlaylistOnMonitor(monitor Monitor, monitorIdx int) error {
	// Create a temporary playlist file
	playlistFile, err := os.CreateTemp("", fmt.Sprintf("fancylock-playlist-%d-*.txt", monitorIdx))
	if err != nil {
		return fmt.Errorf("failed to create temporary playlist file: %v", err)
	}
	defer playlistFile.Close()

	// Get a shuffled copy of media files
	shuffledMedia := make([]MediaFile, len(mp.mediaFiles))
	copy(shuffledMedia, mp.mediaFiles)
	rand.Shuffle(len(shuffledMedia), func(i, j int) {
		shuffledMedia[i], shuffledMedia[j] = shuffledMedia[j], shuffledMedia[i]
	})

	// Write all files to the playlist
	for _, media := range shuffledMedia {
		_, err = playlistFile.WriteString(media.Path + "\n")
		if err != nil {
			return fmt.Errorf("failed to write to playlist file: %v", err)
		}
	}

	// Close the file to ensure it's written to disk
	playlistFile.Close()

	Info("Created playlist at %s with %d files for monitor %d",
		playlistFile.Name(), len(shuffledMedia), monitorIdx)

	// Build proper geometry string for this monitor
	geometry := fmt.Sprintf("%dx%d+%d+%d", monitor.Width, monitor.Height, monitor.X, monitor.Y)

	// Create mpv command with playlist
	playerCmd := mp.config.MediaPlayerCmd
	if playerCmd == "" {
		playerCmd = "mpv"
	}

	cmd := exec.Command(playerCmd,
		"--no-input-default-bindings", // Disable default key bindings
		"--really-quiet",              // No console output
		"--no-stop-screensaver",       // Don't interfere with screensaver
		"--no-osc",                    // No on-screen controls
		"--osd-level=0",               // Disable on-screen display
		"--no-terminal",               // Don't read from terminal
		"--loop-playlist=inf",         // Loop the entire playlist
		"--no-border",                 // No window decorations
		"--ontop",                     // Always on top
		"--fullscreen=yes",            // Fullscreen mode
		"--fs-screen="+strconv.Itoa(monitorIdx), // Use specific screen
		"--no-keepaspect",                       // Don't preserve aspect ratio
		"--no-keepaspect-window",                // Allow any window aspect ratio
		"--panscan=1.0",                         // Scale to fill screen
		"--hwdec=auto",                          // Hardware acceleration
		"--geometry="+geometry,                  // Position on correct monitor
		"--autofit="+fmt.Sprintf("%dx%d", monitor.Width, monitor.Height), // Fit to monitor size
		"--force-window=yes",              // Always create a window
		"--playlist="+playlistFile.Name(), // Use the playlist file
	)

	// Set process group for easier termination
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set a unique monitor index using environment variables
	cmd.Env = append(os.Environ(), fmt.Sprintf("FANCYLOCK_MONITOR_IDX=%d", monitorIdx))

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start media player: %v", err)
	}

	mp.mutex.Lock()
	// Add to our list of running processes
	mp.currentProcs = append(mp.currentProcs, cmd)
	mp.mutex.Unlock()

	Info("Started playlist playback on monitor %d with %d files", monitorIdx, len(shuffledMedia))

	// Start a goroutine to clean up the temp file when mpv exits
	go func() {
		// Wait for the process to complete
		err := cmd.Wait()
		if err != nil && !strings.Contains(err.Error(), "killed") {
			Error("Media player on monitor %d exited with error: %v", monitorIdx, err)
		}

		// Remove the temp playlist file
		os.Remove(playlistFile.Name())
		Info("Cleaned up playlist file for monitor %d", monitorIdx)
	}()

	return nil
}

// Stop stops media playback
func (mp *MediaPlayer) Stop() {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	if !mp.running {
		return // Not running
	}

	// Kill all current processes
	for i, proc := range mp.currentProcs {
		if proc != nil && proc.Process != nil {
			proc.Process.Kill()
			Info("Killed media player process %d on stop", i)
		}
	}

	mp.currentProcs = []*exec.Cmd{}
	mp.running = false

	// Create a new stop channel for next run
	close(mp.stopChan)
	mp.stopChan = make(chan struct{})
}

// scanMediaFiles scans the media directory for supported files
func (mp *MediaPlayer) scanMediaFiles() error {
	mp.mediaFiles = []MediaFile{} // Clear existing files

	Info("Scanning for media files in: %s", mp.config.MediaDir)
	Info("Supported extensions: %v", mp.config.SupportedExt)

	err := filepath.Walk(mp.config.MediaDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil // Skip directories
		}

		// Check file extension
		ext := strings.ToLower(filepath.Ext(path))

		// Check if it's a supported extension
		for _, supportedExt := range mp.config.SupportedExt {
			if ext == supportedExt {
				// Determine media type
				mediaType := mp.getMediaType(ext)

				// Skip images if not enabled
				if mediaType == MediaTypeImage && !mp.config.IncludeImages {
					continue
				}

				// Add to our list
				mp.mediaFiles = append(mp.mediaFiles, MediaFile{
					Path: path,
					Type: mediaType,
				})
				//Info("Found media file: %s (type: %v)", path, mediaType)
				break
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("error scanning media directory: %v", err)
	}

	Info("Found %d media files in %s", len(mp.mediaFiles), mp.config.MediaDir)
	return nil
}

// getMediaType determines the type of media based on file extension
var (
	videoExtMap = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
		".webm": true, ".wmv": true, ".flv": true, ".3gp": true,
	}
	imageExtMap = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".bmp": true, ".svg": true, ".webp": true,
	}
)

func (mp *MediaPlayer) getMediaType(ext string) MediaType {
	if videoExtMap[ext] {
		return MediaTypeVideo
	}
	if imageExtMap[ext] {
		return MediaTypeImage
	}
	return MediaTypeUnknown
}

// GetMediaCount returns the count of available media files
func (mp *MediaPlayer) GetMediaCount() int {
	return len(mp.mediaFiles)
}

// Rescan forces a rescan of the media directory
func (mp *MediaPlayer) Rescan() error {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	return mp.scanMediaFiles()
}
