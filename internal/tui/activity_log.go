package tui

import (
	"fmt"
	"sync"
	"time"
)

// ActivityLog is a ring buffer of activity events.
type ActivityLog struct {
	mu      sync.Mutex
	entries []logEntry
	maxSize int
}

type logEntry struct {
	timestamp time.Time
	message   string
}

// NewActivityLog creates a new ActivityLog with the given max size.
func NewActivityLog(maxSize int) *ActivityLog {
	if maxSize <= 0 {
		maxSize = 50
	}
	return &ActivityLog{
		entries: make([]logEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

// AddEvent adds a new event to the activity log.
func (a *ActivityLog) AddEvent(msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.entries = append(a.entries, logEntry{
		timestamp: time.Now(),
		message:   msg,
	})
	if len(a.entries) > a.maxSize {
		a.entries = a.entries[len(a.entries)-a.maxSize:]
	}
}

// Len returns the number of entries in the log.
func (a *ActivityLog) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// RenderActivityLog renders the activity log panel.
func (a *ActivityLog) RenderActivityLog(maxRows int, cols int) []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	var lines []string
	lines = append(lines, fmt.Sprintf("  %sACTIVITY%s", Bold+White, Reset))

	contentRows := maxRows - 1
	startIdx := 0
	if len(a.entries) > contentRows {
		startIdx = len(a.entries) - contentRows
	}

	for i := startIdx; i < len(a.entries); i++ {
		e := a.entries[i]
		ts := e.timestamp.Format("15:04:05")
		msg := e.message
		maxMsg := cols - 16
		if maxMsg < 20 {
			maxMsg = 20
		}
		if len(msg) > maxMsg {
			msg = msg[:maxMsg-3] + "..."
		}
		lines = append(lines, fmt.Sprintf("    %s%s %s%s", LightGray, ts, msg, Reset))
	}

	// Pad to maxRows
	for len(lines) < maxRows {
		lines = append(lines, "")
	}
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}
	return lines
}
