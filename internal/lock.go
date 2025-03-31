package internal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
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

	// Use default PAM service from config
	serviceName := config.PamService
	if serviceName == "" {
		serviceName = "system-auth"
	}

	return &PamAuthenticator{
		serviceName: serviceName,
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

// LockHelper provides common functionality for screen lockers
type LockHelper struct {
	config        Configuration
	authenticator *PamAuthenticator
}

// NewLockHelper creates a new lock helper
func NewLockHelper(config Configuration) *LockHelper {
	return &LockHelper{
		config:        config,
		authenticator: NewPamAuthenticator(config),
	}
}

// RunPreLockCommand executes the configured pre-lock command
func (h *LockHelper) RunPreLockCommand() error {
	if h.config.PreLockCommand == "" {
		return nil // No command to run
	}

	Info("Running pre-lock command: %s", h.config.PreLockCommand)
	output, err := h.RunCommand("sh", "-c", h.config.PreLockCommand)
	if err != nil {
		Error("Pre-lock command failed: %v - %s", err, output)
		return fmt.Errorf("pre-lock command failed: %v", err)
	}

	Debug("Pre-lock command output: %s", output)
	return nil
}

// RunPostLockCommand executes the configured post-lock command
func (h *LockHelper) RunPostLockCommand() error {
	if h.config.PostLockCommand == "" {
		return nil // No command to run
	}

	Info("Running post-lock command: %s", h.config.PostLockCommand)
	output, err := h.RunCommand("sh", "-c", h.config.PostLockCommand)
	if err != nil {
		Error("Post-lock command failed: %v - %s", err, output)
		return fmt.Errorf("post-lock command failed: %v", err)
	}

	Debug("Post-lock command output: %s", output)
	return nil
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

// BlurBackground blurs the screen contents if enabled in config
func (h *LockHelper) BlurBackground() ([]byte, error) {
	if !h.config.BlurBackground {
		return nil, nil
	}

	// This would typically capture the screen and apply a blur
	// Returns the blurred screenshot data

	return nil, nil
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
