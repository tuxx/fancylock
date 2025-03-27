package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"
)

// LogLevel represents different logging levels
type LogLevel int

const (
	// LevelDebug for detailed debug information
	LevelDebug LogLevel = iota
	// LevelInfo for general operational information
	LevelInfo
	// LevelWarning for potentially problematic situations
	LevelWarning
	// LevelError for error conditions
	LevelError
	// LevelNone disables all logging
	LevelNone
)

var (
	// currentLevel is the current logging level
	currentLevel LogLevel = LevelInfo
	
	// logger is the standard logger instance
	logger = log.New(os.Stderr, "", log.LstdFlags)
	
	// debugMode controls whether debug logging is enabled
	debugMode = false
)

// InitLogger initializes the logger with specified options
func InitLogger(level LogLevel, debugEnabled bool) {
	currentLevel = level
	debugMode = debugEnabled
	
	// If in debug mode, include file and line number in log output
	if debugEnabled {
		logger.SetFlags(log.LstdFlags | log.Lshortfile)
	} else {
		// In release mode, only show errors by default
		if level == LevelInfo {
			currentLevel = LevelError // Only show errors in release mode by default
		}
		logger.SetFlags(log.LstdFlags)
	}
}

// SetLogLevel changes the current logging level
func SetLogLevel(level LogLevel) {
	currentLevel = level
}

// EnableDebug enables debug mode
func EnableDebug() {
	debugMode = true
	logger.SetFlags(log.LstdFlags | log.Lshortfile)
}

// DisableDebug disables debug mode
func DisableDebug() {
	debugMode = false
	logger.SetFlags(log.LstdFlags)
}

// getCallerInfo gets the caller's file and line number
func getCallerInfo() string {
	if !debugMode {
		return ""
	}
	
	_, file, line, ok := runtime.Caller(3) // Skip three frames to get to the actual caller
	if !ok {
		return ""
	}
	
	// Extract just the filename from the full path
	parts := strings.Split(file, "/")
	filename := parts[len(parts)-1]
	
	return fmt.Sprintf("[%s:%d] ", filename, line)
}

// formatLog formats a log message with timestamp, level and caller info
func formatLog(level string, format string, args ...interface{}) string {
	timestamp := time.Now().Format("2006/01/02 15:04:05")
	callerInfo := getCallerInfo()
	
	// Format the actual message
	var message string
	if len(args) > 0 {
		message = fmt.Sprintf(format, args...)
	} else {
		message = format
	}
	
	return fmt.Sprintf("%s %s%s: %s", timestamp, callerInfo, level, message)
}

// Debug logs debug level messages
func Debug(format string, args ...interface{}) {
	if !debugMode || currentLevel > LevelDebug {
		return
	}
	logger.Output(2, formatLog("DEBUG", format, args...))
}

// Info logs info level messages
func Info(format string, args ...interface{}) {
	if currentLevel > LevelInfo {
		return
	}
	logger.Output(2, formatLog("INFO", format, args...))
}

// Warn logs warning level messages
func Warn(format string, args ...interface{}) {
	if currentLevel > LevelWarning {
		return
	}
	logger.Output(2, formatLog("WARN", format, args...))
}

// Error logs error level messages
func Error(format string, args ...interface{}) {
	if currentLevel > LevelError {
		return
	}
	logger.Output(2, formatLog("ERROR", format, args...))
}

// Fatal logs a fatal error message and exits the program
func Fatal(format string, args ...interface{}) {
	logger.Output(2, formatLog("FATAL", format, args...))
	os.Exit(1)
}
