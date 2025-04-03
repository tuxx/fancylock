package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() Configuration {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "/tmp"
	}

	pamPath := "/etc/pam.d/fancylock"
	PamService := "system-auth"
	if _, err := os.Stat(pamPath); err == nil {
		PamService = "fancylock"
	}

	return Configuration{
		MediaDir:           filepath.Join(homeDir, "Videos"),
		LockScreen:         false,
		SupportedExt:       []string{".mov", ".mkv", ".mp4", ".avi", ".webm"},
		PamService:         PamService,
		IncludeImages:      true,
		ImageDisplayTime:   30,
		DebugExit:          false, // Disabled by default for security
		PreLockCommand:     "",    // No default pre-lock command
		PostLockCommand:    "",    // No default post-lock command
		LockPauseMedia:     false, // Disabled by default
		UnlockUnpauseMedia: false, // Disabled by default
	}
}

// LoadConfig loads configuration from the specified file path
func LoadConfig(path string, config *Configuration) error {
	// Read the config file
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}

	// Parse the JSON data
	err = json.Unmarshal(data, config)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %v", err)
	}

	// Validate the configuration
	if err := validateConfig(config); err != nil {
		return fmt.Errorf("invalid configuration: %v", err)
	}

	return nil
}

// SaveConfig saves the current configuration to the specified file path
func SaveConfig(path string, config Configuration) error {
	// Validate the configuration before saving
	if err := validateConfig(&config); err != nil {
		return fmt.Errorf("invalid configuration: %v", err)
	}

	// Convert config to JSON
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config to JSON: %v", err)
	}

	// Write to file
	err = os.WriteFile(path, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}

	return nil
}

// validateConfig checks if the configuration is valid
func validateConfig(config *Configuration) error {
	// Check if media directory exists
	_, err := os.Stat(config.MediaDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create the directory
			err = os.MkdirAll(config.MediaDir, 0755)
			if err != nil {
				return fmt.Errorf("media directory does not exist and could not be created: %v", err)
			}
		} else {
			return fmt.Errorf("error accessing media directory: %v", err)
		}
	}

	// Ensure we have at least one supported extension
	if len(config.SupportedExt) == 0 {
		return fmt.Errorf("no supported media extensions specified")
	}

	// Ensure image display time is reasonable
	if config.ImageDisplayTime <= 0 {
		return fmt.Errorf("image display time must be positive")
	}

	return nil
}

// GenerateDefaultConfigFile creates a default configuration file if it doesn't exist
func GenerateDefaultConfigFile() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %v", err)
	}

	configDir := filepath.Join(homeDir, ".config", "fancylock")

	// Create config directory if it doesn't exist
	err = os.MkdirAll(configDir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create config directory: %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")

	// Check if config file already exists
	_, err = os.Stat(configPath)
	if err == nil {
		// Config file already exists, no need to create it
		return nil
	}

	// Create default config
	config := DefaultConfig()

	// Save default config
	err = SaveConfig(configPath, config)
	if err != nil {
		return fmt.Errorf("failed to save default config: %v", err)
	}

	return nil
}
