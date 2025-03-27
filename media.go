package main

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

// startMediaOnMonitor starts playing a media file on a specific monitor
func (mp *MediaPlayer) startMediaOnMonitor(media MediaFile, monitor Monitor, monitorIdx int) error {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	// Build the command based on media type
	var cmd *exec.Cmd

	switch media.Type {
	case MediaTypeVideo:
		playerCmd := mp.config.MediaPlayerCmd
		if playerCmd == "" {
			playerCmd = "mpv"
		}

		// Build proper geometry string for this monitor
		geometry := fmt.Sprintf("%dx%d+%d+%d", monitor.Width, monitor.Height, monitor.X, monitor.Y)

		Info("Starting video on monitor %d with geometry: %s", monitorIdx, geometry)

		cmd = exec.Command(playerCmd,
			"--no-input-default-bindings", // Disable default key bindings
			"--really-quiet",              // No console output
			"--no-stop-screensaver",       // Don't interfere with screensaver
			"--no-osc",                    // No on-screen controls
			"--osd-level=0",               // Disable on-screen display
			"--no-terminal",               // Don't read from terminal
			"--loop=inf",                  // Loop the video
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
			"--force-window=yes", // Always create a window
			media.Path)

	case MediaTypeImage:
		playerCmd := mp.config.MediaPlayerCmd
		if playerCmd == "" {
			playerCmd = "mpv"
		}

		// Calculate duration in milliseconds
		durationMs := mp.config.ImageDisplayTime * 1000

		// Build proper geometry string for this monitor
		geometry := fmt.Sprintf("%dx%d+%d+%d", monitor.Width, monitor.Height, monitor.X, monitor.Y)

		Info("Starting image on monitor %d with geometry: %s", monitorIdx, geometry)

		cmd = exec.Command(playerCmd,
			"--no-input-default-bindings", // Disable default key bindings
			"--really-quiet",              // No console output
			"--no-stop-screensaver",       // Don't interfere with screensaver
			"--no-osc",                    // No on-screen controls
			"--osd-level=0",               // Disable on-screen display
			"--no-terminal",               // Don't read from terminal
			"--loop=inf",                  // Loop the image display
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
			"--force-window=yes", // Always create a window
			"--image-display-duration="+fmt.Sprintf("%d", durationMs),
			media.Path)

	default:
		return fmt.Errorf("unsupported media type for file: %s", media.Path)
	}

	// Set process group for easier termination
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Set a unique monitor index using environment variables
	cmd.Env = append(os.Environ(), fmt.Sprintf("FANCYLOCK_MONITOR_IDX=%d", monitorIdx))

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start media player: %v", err)
	}

	// Add to our list of running processes
	mp.currentProcs = append(mp.currentProcs, cmd)
	Info("Started playback on monitor %d: %s", monitorIdx, media.Path)

	return nil
}

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

// Start begins playing media files in random order
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

	// Start playback in a goroutine
	go mp.playbackLoop()

	return nil
}

// Stop stops media playback
func (mp *MediaPlayer) Stop() {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	if !mp.running {
		return // Not running
	}

	// Signal the playback loop to stop
	close(mp.stopChan)
	mp.stopChan = make(chan struct{}) // Reset for next run

	// Kill all current processes
	for i, proc := range mp.currentProcs {
		if proc != nil && proc.Process != nil {
			proc.Process.Kill()
			Info("Killed media player process %d on stop", i)
		}
	}

	mp.currentProcs = []*exec.Cmd{}
	mp.running = false
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
func (mp *MediaPlayer) getMediaType(ext string) MediaType {
	// Common video extensions
	videoExts := []string{".mp4", ".mkv", ".avi", ".mov", ".webm", ".wmv", ".flv", ".3gp"}
	for _, videoExt := range videoExts {
		if ext == videoExt {
			return MediaTypeVideo
		}
	}

	// Common image extensions
	imageExts := []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".svg", ".webp"}
	for _, imageExt := range imageExts {
		if ext == imageExt {
			return MediaTypeImage
		}
	}

	return MediaTypeUnknown
}

// playbackLoop continuously plays media files in random order
func (mp *MediaPlayer) playbackLoop() {
	// Initialize random number generator
	rand.Seed(time.Now().UnixNano())

	// Create a shuffled list of indices for each monitor
	monitorIndices := make([][]int, len(mp.monitors))
	for i := range mp.monitors {
		monitorIndices[i] = rand.Perm(len(mp.mediaFiles))
	}

	// Current index for each monitor
	currentIndices := make([]int, len(mp.monitors))

	for mp.running {
		// Check if we should stop
		select {
		case <-mp.stopChan:
			mp.killAllMedia()
			return
		default:
			// Continue playing
		}

		// Kill any previously playing media
		mp.killAllMedia()

		// Start a new video on each monitor
		for monitorIdx, monitor := range mp.monitors {
			// Get the next file to play for this monitor
			mediaIdx := monitorIndices[monitorIdx][currentIndices[monitorIdx]]
			mediaFile := mp.mediaFiles[mediaIdx]

			// Move to next index for this monitor
			currentIndices[monitorIdx] = (currentIndices[monitorIdx] + 1) % len(mp.mediaFiles)

			// Reshuffle if we've used all files for this monitor
			if currentIndices[monitorIdx] == 0 {
				monitorIndices[monitorIdx] = rand.Perm(len(mp.mediaFiles))
			}

			// Play the media file on this monitor
			Info("Starting playback on monitor %d: %s", monitorIdx, mediaFile.Path)
			if err := mp.startMediaOnMonitor(mediaFile, monitor, monitorIdx); err != nil {
				Error("Failed to play media on monitor %d: %v", monitorIdx, err)
			}
		}

		// Wait for some time before transitioning to the next video
		// We don't wait for the videos to finish; instead we play each set for a fixed duration
		videoPlayTime := 30 * time.Second

		select {
		case <-time.After(videoPlayTime):
			// Time to change videos on all monitors
		case <-mp.stopChan:
			// We were asked to stop
			mp.killAllMedia()
			return
		}

		// Add a brief transition delay
		time.Sleep(500 * time.Millisecond)
	}
}

// playMediaAndWait plays a media file and waits for it to complete
func (mp *MediaPlayer) playMediaAndWait(media MediaFile) error {
	// Start the media playing
	if err := mp.startMedia(media); err != nil {
		return err
	}

	// Wait for the process to complete or be killed
	mp.mutex.Lock()
	proc := mp.currentProc
	mp.mutex.Unlock()

	if proc != nil {
		waitChan := make(chan error, 1)

		// Wait for the process in a goroutine
		go func() {
			waitChan <- proc.Wait()
		}()

		// Wait for either completion or stop signal
		select {
		case err := <-waitChan:
			if err != nil && !strings.Contains(err.Error(), "killed") {
				Info("Media player exited with error: %v", err)
			} else {
				Info("Media playback of %s completed", media.Path)
			}
		case <-mp.stopChan:
			mp.killCurrentMedia()
			return fmt.Errorf("playback interrupted")
		}
	}

	return nil
}

// startMedia starts playing a media file without waiting for completion
func (mp *MediaPlayer) startMedia(media MediaFile) error {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	// Build the command based on media type
	var cmd *exec.Cmd

	switch media.Type {
	case MediaTypeVideo:
		// Use mpv or specified player for videos
		playerCmd := mp.config.MediaPlayerCmd
		if playerCmd == "" {
			playerCmd = "mpv"
		}

		cmd = exec.Command(playerCmd,
			"--no-input-default-bindings",
			"--really-quiet",
			"--no-stop-screensaver",
			"--no-osc",
			"--osd-level=0", // Disable on-screen display
			"--no-terminal",
			"--loop=inf",             // Loop video continuously
			"--no-border",            // No window decorations
			"--hwdec=auto",           // Enable hardware decoding
			"--fullscreen",           // Use fullscreen mode
			"--no-keepaspect",        // Don't preserve aspect ratio, stretch to fill
			"--no-keepaspect-window", // Allow window to have any aspect ratio
			"--panscan=1.0",          // Enable panscan to fill screen
			"--video-unscaled=no",    // Ensure video scaling is enabled
			"--ontop=yes",            // Keep on top of other windows
			"--force-window=yes",     // Always create a window
			media.Path)

	case MediaTypeImage:
		// For images, use the player with appropriate options for still images
		playerCmd := mp.config.MediaPlayerCmd
		if playerCmd == "" {
			playerCmd = "mpv"
		}

		// Calculate duration in milliseconds
		durationMs := mp.config.ImageDisplayTime * 1000

		cmd = exec.Command(playerCmd,
			"--no-input-default-bindings",
			"--really-quiet",
			"--no-stop-screensaver",
			"--no-osc",
			"--osd-level=0", // Disable on-screen display
			"--no-terminal",
			"--no-border",            // No window decorations
			"--loop=inf",             // Loop continuously
			"--hwdec=auto",           // Enable hardware decoding
			"--fullscreen",           // Use fullscreen mode
			"--no-keepaspect",        // Don't preserve aspect ratio, stretch to fill
			"--no-keepaspect-window", // Allow window to have any aspect ratio
			"--panscan=1.0",          // Enable panscan to fill screen
			"--ontop=yes",            // Keep on top of other windows
			"--force-window=yes",     // Always create a window
			"--image-display-duration="+fmt.Sprintf("%d", durationMs),
			media.Path)

	default:
		return fmt.Errorf("unsupported media type for file: %s", media.Path)
	}

	// Set process group for easier termination
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start media player: %v", err)
	}

	mp.currentProc = cmd
	Info("Started playback of: %s with args: %v", media.Path, cmd.Args)

	return nil
}

// killCurrentMedia safely stops any currently playing media
func (mp *MediaPlayer) killCurrentMedia() {
	mp.mutex.Lock()
	defer mp.mutex.Unlock()

	if mp.currentProc != nil && mp.currentProc.Process != nil {
		// Kill the process
		mp.currentProc.Process.Kill()
		// Allow some time for the process to terminate
		time.Sleep(100 * time.Millisecond)
		mp.currentProc = nil
	}
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
