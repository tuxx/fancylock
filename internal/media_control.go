package internal

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// MediaController handles controlling media playback through D-Bus
type MediaController struct {
	conn *dbus.Conn
}

// NewMediaController creates a new MediaController instance
func NewMediaController() (*MediaController, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return &MediaController{conn: conn}, nil
}

// Close closes the D-Bus connection
func (mc *MediaController) Close() {
	if mc.conn != nil {
		mc.conn.Close()
	}
}

// PauseAllMedia pauses all media players that support MPRIS
func (mc *MediaController) PauseAllMedia() error {
	// Get all D-Bus names
	var names []string
	err := mc.conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		return fmt.Errorf("failed to list D-Bus names: %v", err)
	}

	Debug("Found %d D-Bus names", len(names))
	pausedCount := 0

	// Look for any MPRIS players
	for _, name := range names {
		// Skip system D-Bus names
		if strings.HasPrefix(name, ":") || name == "org.freedesktop.DBus" {
			continue
		}

		// Check if this is an MPRIS player
		if strings.Contains(name, "org.mpris.MediaPlayer2") {
			Debug("Found MPRIS player: %s", name)
			if err := mc.pausePlayer(name); err == nil {
				pausedCount++
			}
		}
	}

	if pausedCount == 0 {
		Debug("No media players found to pause")
	} else {
		Debug("Successfully paused %d media players", pausedCount)
	}

	return nil
}

// UnpauseAllMedia unpauses all media players that support MPRIS
func (mc *MediaController) UnpauseAllMedia() error {
	// Get all D-Bus names
	var names []string
	err := mc.conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		return fmt.Errorf("failed to list D-Bus names: %v", err)
	}

	Debug("Found %d D-Bus names", len(names))
	unpausedCount := 0

	// Look for any MPRIS players
	for _, name := range names {
		// Skip system D-Bus names
		if strings.HasPrefix(name, ":") || name == "org.freedesktop.DBus" {
			continue
		}

		// Check if this is an MPRIS player
		if strings.Contains(name, "org.mpris.MediaPlayer2") {
			Debug("Found MPRIS player: %s", name)
			if err := mc.unpausePlayer(name); err == nil {
				unpausedCount++
			}
		}
	}

	if unpausedCount == 0 {
		Debug("No media players found to unpause")
	} else {
		Debug("Successfully unpaused %d media players", unpausedCount)
	}

	return nil
}

// pausePlayer attempts to pause a specific MPRIS player
func (mc *MediaController) pausePlayer(name string) error {
	// Get the player object
	obj := mc.conn.Object(name, dbus.ObjectPath("/org/mpris/MediaPlayer2"))

	// First check if the player is actually playing
	var playbackStatus string
	err := obj.Call("org.freedesktop.DBus.Properties.Get", 0, "org.mpris.MediaPlayer2.Player", "PlaybackStatus").Store(&playbackStatus)
	if err != nil {
		Debug("Failed to get playback status for %s: %v", name, err)
		return err
	}

	// Only try to pause if it's actually playing
	if playbackStatus == "Playing" {
		Debug("Player %s is currently playing, attempting to pause", name)
		// Call PlaybackControl.Pause
		call := obj.Call("org.mpris.MediaPlayer2.Player.Pause", 0)
		if call.Err != nil {
			Error("Failed to pause %s: %v", name, call.Err)
			return call.Err
		}
		Debug("Successfully paused %s", name)
	} else {
		Debug("Player %s is not playing (status: %s), skipping pause", name, playbackStatus)
	}

	return nil
}

// unpausePlayer attempts to unpause a specific MPRIS player
func (mc *MediaController) unpausePlayer(name string) error {
	// Get the player object
	obj := mc.conn.Object(name, dbus.ObjectPath("/org/mpris/MediaPlayer2"))

	// First check if the player is paused
	var playbackStatus string
	err := obj.Call("org.freedesktop.DBus.Properties.Get", 0, "org.mpris.MediaPlayer2.Player", "PlaybackStatus").Store(&playbackStatus)
	if err != nil {
		Debug("Failed to get playback status for %s: %v", name, err)
		return err
	}

	// Only try to unpause if it's paused
	if playbackStatus == "Paused" {
		Debug("Player %s is currently paused, attempting to play", name)
		// Call PlaybackControl.Play
		call := obj.Call("org.mpris.MediaPlayer2.Player.Play", 0)
		if call.Err != nil {
			Error("Failed to unpause %s: %v", name, call.Err)
			return call.Err
		}
		Debug("Successfully unpaused %s", name)
	} else {
		Debug("Player %s is not paused (status: %s), skipping unpause", name, playbackStatus)
	}

	return nil
}
