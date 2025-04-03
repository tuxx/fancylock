package internal

import (
	"fmt"
	"time"
)

// LockoutManager handles authentication failures and lockout periods
type LockoutManager struct {
	failedAttempts  int           // Count of failed authentication attempts
	lockoutUntil    time.Time     // Time until which input is locked out
	lockoutActive   bool          // Whether a lockout is currently active
	lastFailureTime time.Time     // Time of the last failed attempt
	config          Configuration // Application configuration
	timerRunning    bool          // Track if the countdown timer is already running
}

// NewLockoutManager creates a new lockout manager with the given configuration
func NewLockoutManager(config Configuration) *LockoutManager {
	return &LockoutManager{
		failedAttempts:  0,
		lockoutActive:   false,
		timerRunning:    false,
		config:          config,
		lastFailureTime: time.Now().Add(-24 * time.Hour), // Set to past to avoid initial penalty
	}
}

// HandleFailedAttempt processes a failed authentication and returns lockout information
// Returns: lockoutActive (bool), lockoutDuration (time.Duration), remainingAttempts (int)
func (lm *LockoutManager) HandleFailedAttempt() (bool, time.Duration, int) {
	// Record the failure
	lm.failedAttempts++
	lm.lastFailureTime = time.Now()

	Info("Authentication failed (%d/3 attempts)", lm.failedAttempts)

	// If we've had 3+ failures, implement a lockout
	if lm.failedAttempts >= 3 {
		var lockoutDuration time.Duration

		// Check if we're in debug mode
		if lm.config.DebugExit {
			// Use a shorter lockout in debug mode
			lockoutDuration = 5 * time.Second
			Info("Debug mode: Using shorter lockout duration of 5 seconds")
		} else {
			// Determine the lockout duration based on recent failures
			// First lockout is 30 seconds, increases for subsequent lockouts
			if !lm.lockoutActive {
				// First lockout
				lockoutDuration = 30 * time.Second
			} else {
				// Subsequent lockouts - increase duration but cap at 10 minutes
				// Increase by 30 seconds each time, based on failed attempts / 3
				lockoutDuration = 30 * time.Second * time.Duration(lm.failedAttempts/3)
				if lockoutDuration > 10*time.Minute {
					lockoutDuration = 10 * time.Minute
				}
			}
		}

		// Set the lockout time
		lm.lockoutUntil = time.Now().Add(lockoutDuration)
		lm.lockoutActive = true

		Info("Failed %d attempts, locking out for %v", lm.failedAttempts, lockoutDuration)

		// Reset counter after implementing lockout
		remainingAttempts := 0
		lm.failedAttempts = 0

		return true, lockoutDuration, remainingAttempts
	}

	// Not locked out yet
	remainingAttempts := 3 - lm.failedAttempts
	return false, 0, remainingAttempts
}

// IsLockedOut checks if authentication is currently locked out
func (lm *LockoutManager) IsLockedOut() bool {
	if lm.lockoutActive && time.Now().Before(lm.lockoutUntil) {
		return true
	}

	// If we were in a lockout but it's expired, clear the lockout state
	if lm.lockoutActive && time.Now().After(lm.lockoutUntil) {
		Info("Lockout period has expired, clearing lockout state")
		lm.lockoutActive = false
	}

	return false
}

// GetRemainingTime returns how much time is left in the lockout
func (lm *LockoutManager) GetRemainingTime() time.Duration {
	if !lm.lockoutActive {
		return 0
	}

	remaining := lm.lockoutUntil.Sub(time.Now())
	if remaining < 0 {
		remaining = 0
		lm.lockoutActive = false
	}

	return remaining
}

// FormatRemainingTime returns a nicely formatted string of the remaining lockout time
func (lm *LockoutManager) FormatRemainingTime() string {
	remainingTime := lm.GetRemainingTime()
	minutes := int(remainingTime.Minutes())
	seconds := int(remainingTime.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

// ResetLockout resets the lockout state (e.g., after successful authentication)
func (lm *LockoutManager) ResetLockout() {
	lm.failedAttempts = 0
	lm.lockoutActive = false
	lm.timerRunning = false
}

// GetLockoutUntil returns the time when the lockout ends
func (lm *LockoutManager) GetLockoutUntil() time.Time {
	return lm.lockoutUntil
}

// IsTimerRunning returns whether the lockout timer is currently running
func (lm *LockoutManager) IsTimerRunning() bool {
	return lm.timerRunning
}

// SetTimerRunning sets the timer running state
func (lm *LockoutManager) SetTimerRunning(running bool) {
	lm.timerRunning = running
}
