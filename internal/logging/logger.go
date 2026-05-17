package logging

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Level represents a log level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ringBufferSize is the max number of messages stored in TUI mode.
const ringBufferSize = 200

// Message represents a log message stored in the ring buffer for TUI rendering.
type Message struct {
	Time    time.Time
	Level   string
	Content string
}

// Logger provides structured logging with level filtering, timestamps,
// optional file output, and a TUI ring buffer mode.
type Logger struct {
	mu       sync.Mutex
	level    Level
	tuiMode  bool
	ring     []Message
	debugLog *os.File
}

// NewLogger creates a new Logger with the specified level and optional debug log path.
// If debugLogPath is empty, no file logging is performed.
func NewLogger(level Level, debugLogPath string) (*Logger, error) {
	l := &Logger{
		level: level,
		ring:  make([]Message, 0, ringBufferSize),
	}

	if debugLogPath != "" {
		f, err := os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open debug log %s: %w", debugLogPath, err)
		}
		l.debugLog = f
	}

	return l, nil
}

// SetTUIMode enables or disables TUI mode.
// In TUI mode, messages are stored in a ring buffer instead of printed to stdout.
func (l *Logger) SetTUIMode(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tuiMode = enabled
}

// Messages returns a copy of the ring buffer messages for TUI rendering.
func (l *Logger) Messages() []Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]Message, len(l.ring))
	copy(result, l.ring)
	return result
}

// Close closes the debug log file if open.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.debugLog != nil {
		l.debugLog.Close()
		l.debugLog = nil
	}
}

// Info logs an informational message.
func (l *Logger) Info(format string, args ...any) {
	l.log(LevelInfo, "INFO", format, args...)
}

// Warn logs a warning message.
func (l *Logger) Warn(format string, args ...any) {
	l.log(LevelWarn, "WARN", format, args...)
}

// Error logs an error message.
func (l *Logger) Error(format string, args ...any) {
	l.log(LevelError, "ERROR", format, args...)
}

// Debug logs a debug message.
func (l *Logger) Debug(format string, args ...any) {
	l.log(LevelDebug, "DEBUG", format, args...)
}

// Task logs a task-related message.
func (l *Logger) Task(format string, args ...any) {
	l.log(LevelInfo, "TASK", format, args...)
}

// Wave logs a wave-related message.
func (l *Logger) Wave(format string, args ...any) {
	l.log(LevelInfo, "WAVE", format, args...)
}

func (l *Logger) log(level Level, label, format string, args ...any) {
	if level < l.level {
		return
	}

	now := time.Now()
	msg := fmt.Sprintf(format, args...)
	timestamp := now.Format("15:04:05")

	l.mu.Lock()
	defer l.mu.Unlock()

	// Always write to debug log file if available
	if l.debugLog != nil {
		fullTimestamp := now.Format("2006-01-02T15:04:05")
		fmt.Fprintf(l.debugLog, "[%s] %s: %s\n", fullTimestamp, label, msg)
	}

	if l.tuiMode {
		// Store in ring buffer for TUI
		m := Message{
			Time:    now,
			Level:   label,
			Content: msg,
		}
		if len(l.ring) >= ringBufferSize {
			// Shift ring buffer: drop oldest
			l.ring = append(l.ring[1:], m)
		} else {
			l.ring = append(l.ring, m)
		}
	} else {
		// Print directly to stdout
		prefix := l.prefix(label)
		fmt.Printf("  %s  %s %s\n", prefix, timestamp, msg)
	}
}

func (l *Logger) prefix(label string) string {
	switch label {
	case "INFO":
		return "\033[90m\xc2\xb7\033[0m"
	case "WARN":
		return "\033[93m\xe2\x9a\xa0\033[0m"
	case "ERROR":
		return "\033[91m\xe2\x9c\x97\033[0m"
	case "DEBUG":
		return "\033[90m?\033[0m"
	case "TASK":
		return "\033[96m\xe2\x86\x92\033[0m"
	case "WAVE":
		return "\033[1m\033[97m\xe2\x95\x90\033[0m"
	default:
		return " "
	}
}

// ParseLevel converts a string level name to a Level value.
func ParseLevel(s string) Level {
	switch s {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}
