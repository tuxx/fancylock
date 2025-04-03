package internal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"

	"github.com/msteinert/pam"
)

// NewPamAuthenticator creates a new PAM authenticator
func NewPamAuthenticator(config Configuration) *PamAuthenticator {
	// Get the current username
	currentUser, err := user.Current()
	username := "nobody"
	if err == nil {
		username = currentUser.Username
	}

	return &PamAuthenticator{
		serviceName: config.PamService,
		username:    username,
	}
}

// Authenticate attempts to authenticate with the given password
func (a *PamAuthenticator) Authenticate(password string) AuthResult {
	// Define the conversation function that provides the password
	conv := func(style pam.Style, msg string) (string, error) {
		switch style {
		case pam.PromptEchoOff:
			// Return the password for authentication prompt
			return password, nil
		case pam.PromptEchoOn:
			// Ignore username prompts as we already provided it
			return "", nil
		case pam.ErrorMsg:
			// Log error messages but keep going
			Info("PAM error: %s", msg)
			return "", nil
		case pam.TextInfo:
			// Log informational messages but keep going
			Info("PAM info: %s", msg)
			return "", nil
		default:
			return "", errors.New("unexpected conversation style")
		}
	}

	// Start PAM transaction
	t, err := pam.StartFunc(a.serviceName, a.username, conv)
	if err != nil {
		return AuthResult{
			Success: false,
			Message: fmt.Sprintf("Failed to start PAM transaction: %v", err),
		}
	}

	// Attempt authentication
	err = t.Authenticate(0)
	if err != nil {
		return AuthResult{
			Success: false,
			Message: fmt.Sprintf("Authentication failed: %v", err),
		}
	}

	// Check account validity
	err = t.AcctMgmt(0)
	if err != nil {
		return AuthResult{
			Success: false,
			Message: fmt.Sprintf("Account validation failed: %v", err),
		}
	}

	// PAM transaction doesn't have an End() method in this library
	// It will be automatically ended when the transaction goes out of scope

	return AuthResult{
		Success: true,
		Message: "Authentication successful",
	}
}

// LockHelper handles screen locking operations
type LockHelper struct {
	authenticator *PamAuthenticator
	config        Configuration
	mediaCtrl     *MediaController
}

// NewLockHelper creates a new helper instance with the given configuration
func NewLockHelper(config Configuration) *LockHelper {
	auth := &PamAuthenticator{
		serviceName: config.PamService,
		username:    os.Getenv("USER"),
	}

	var mediaCtrl *MediaController
	if config.LockPauseMedia || config.UnlockUnpauseMedia {
		Debug("Media control is enabled, initializing media controller")
		var err error
		mediaCtrl, err = NewMediaController()
		if err != nil {
			// Log error but continue without media control
			Error("Failed to initialize media controller: %v", err)
		} else {
			Debug("Media controller initialized successfully")
		}
	} else {
		Debug("Media control is disabled, skipping media controller initialization")
	}

	return &LockHelper{
		authenticator: auth,
		config:        config,
		mediaCtrl:     mediaCtrl,
	}
}

// RunPreLockCommand runs the configured pre-lock command (if any)
func (h *LockHelper) RunPreLockCommand() error {
	if h.config.PreLockCommand == "" {
		return nil
	}
	Debug("Running pre-lock command: %s", h.config.PreLockCommand)
	return runShellCommand(h.config.PreLockCommand)
}

// RunPostLockCommand runs the configured post-lock command (if any)
func (h *LockHelper) RunPostLockCommand() error {
	if h.config.PostLockCommand == "" {
		return nil
	}
	Debug("Running post-lock command: %s", h.config.PostLockCommand)
	return runShellCommand(h.config.PostLockCommand)
}

// CheckUserPermissions verifies that the user has the necessary permissions
func (h *LockHelper) CheckUserPermissions() error {
	// Check if we're running as root (which we shouldn't be for security reasons)
	if os.Geteuid() == 0 {
		return errors.New("fancylock should not be run as root for security reasons")
	}

	return nil
}

// TakeDisplayServerLock attempts to acquire any necessary system-level locks
func (h *LockHelper) TakeDisplayServerLock() error {
	// This is a placeholder - actual implementation will depend on the display server
	return nil
}

// ReleaseDisplayServerLock releases any system-level locks that were acquired
func (h *LockHelper) ReleaseDisplayServerLock() error {
	// This is a placeholder - actual implementation will depend on the display server
	return nil
}

// PreventSuspendDuringUnlock prevents the system from suspending while unlocking
func (h *LockHelper) PreventSuspendDuringUnlock() func() {
	// This is a simplified implementation - in practice you might want to use logind
	// or other system-specific APIs to inhibit suspend

	// Return a cleanup function
	return func() {
		// Cleanup code here
	}
}

// DisableVTs disables switching to other virtual terminals
func (h *LockHelper) DisableVTs() func() {
	// In a real implementation, you'd use the appropriate API to disable VT switching
	// For example, on Linux you might use an ioctl on the console

	// Return a cleanup function
	return func() {
		// Re-enable VTs
	}
}

// EnsureSingleInstance makes sure only one instance of the locker is running
func (h *LockHelper) EnsureSingleInstance() error {
	// Try to get an exclusive lock on a file
	lockFile := "/tmp/fancylock.lock"
	file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return fmt.Errorf("failed to open lock file: %v", err)
	}

	// Try to get an exclusive lock
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		file.Close()
		return errors.New("another instance of fancylock is already running")
	}

	// Keep the file open to maintain the lock
	// In a real implementation, you'd store the file handle somewhere and close it on exit

	return nil
}

// RunCommand runs an external command and returns its output
func (h *LockHelper) RunCommand(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command failed: %v - %s", err, output)
	}
	return string(output), nil
}

// NewSecurePassword creates a new secure password container
func NewSecurePassword() *SecurePassword {
	return &SecurePassword{
		data: make([]byte, 0, 64),
	}
}

// Append adds a character to the password
func (p *SecurePassword) Append(char byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.data = append(p.data, char)
}

// RemoveLast removes the last character from the password
func (p *SecurePassword) RemoveLast() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.data) > 0 {
		// Zero out the last byte before removing it
		p.data[len(p.data)-1] = 0
		p.data = p.data[:len(p.data)-1]
	}
}

// Clear securely wipes the password data
func (p *SecurePassword) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Zero out the memory before resetting
	for i := range p.data {
		p.data[i] = 0
	}
	p.data = p.data[:0]
}

// String returns the password as a string (use carefully)
func (p *SecurePassword) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Create a temporary string for authentication
	return string(p.data)
}

// Length returns the password length
func (p *SecurePassword) Length() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.data)
}

// runShellCommand executes a shell command string
func runShellCommand(cmd string) error {
	return exec.Command("sh", "-c", strings.TrimSpace(cmd)).Run()
}

// PauseMediaIfEnabled pauses all media if enabled in config
func (h *LockHelper) PauseMediaIfEnabled() error {
	if !h.config.LockPauseMedia {
		Debug("LockPauseMedia is disabled in config, skipping media pause")
		return nil
	}

	if h.mediaCtrl == nil {
		Error("LockPauseMedia is enabled but media controller is not initialized")
		return nil
	}

	Debug("Pausing all media players")
	return h.mediaCtrl.PauseAllMedia()
}

// UnpauseMediaIfEnabled unpauses all media if enabled in config
func (h *LockHelper) UnpauseMediaIfEnabled() error {
	if !h.config.UnlockUnpauseMedia {
		Debug("UnlockUnpauseMedia is disabled in config, skipping media unpause")
		return nil
	}

	if h.mediaCtrl == nil {
		Error("UnlockUnpauseMedia is enabled but media controller is not initialized")
		return nil
	}

	Debug("Unpausing all media players")
	return h.mediaCtrl.UnpauseAllMedia()
}

// Close cleans up resources
func (h *LockHelper) Close() {
	if h.mediaCtrl != nil {
		h.mediaCtrl.Close()
	}
}
