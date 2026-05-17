package tui

import (
	"fmt"
	"strings"
	"time"
)

// ProgressBar renders a progress bar with percentage and elapsed time.
func ProgressBar(completed, total int, width int, elapsed time.Duration) string {
	if width < 10 {
		width = 10
	}
	pct := 0
	filled := 0
	if total > 0 {
		pct = completed * 100 / total
		filled = completed * width / total
	}
	if filled > width {
		filled = width
	}
	empty := width - filled

	bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", empty)
	elapsedStr := FormatDuration(elapsed)

	return fmt.Sprintf("  %s%s%s %s%d/%d%s %s(%d%%)%s  %s%selapsed %s%s",
		Bold+White, bar, Reset,
		LightGray, completed, total, Reset,
		LightGray, pct, Reset,
		Dim+LightGray, "", elapsedStr, Reset)
}

// FormatDuration formats a duration as "Xm XXs" or "Xs".
func FormatDuration(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 0 {
		secs = 0
	}
	m := secs / 60
	s := secs % 60
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// Spinner provides animated spinner frames.
type Spinner struct {
	frames []string
	index  int
}

// NewSpinner creates a new Spinner with braille dot animation frames.
func NewSpinner() *Spinner {
	return &Spinner{
		frames: []string{"\u2839", "\u2838", "\u2834", "\u2826", "\u2807", "\u280f", "\u2819", "\u2839"},
	}
}

// Tick advances the spinner and returns the current frame.
func (s *Spinner) Tick() string {
	frame := s.frames[s.index%len(s.frames)]
	s.index++
	return frame
}

// StatusSymbol returns a colored status symbol for the given state.
func StatusSymbol(state string, frame int) string {
	switch state {
	case "completed", "synced":
		return Bold + White + "\u25cf" + Reset // filled circle
	case "running":
		if frame%2 == 0 {
			return Bold + Cyan + "\u25c9" + Reset // fisheye
		}
		return Bold + Cyan + "\u25cf" + Reset // filled circle
	case "failed":
		return Bold + Red + "\u2717" + Reset // cross mark
	case "skipped":
		return Dim + Yellow + "\u25cb" + Reset // empty circle
	default: // pending
		return Dim + Gray + "\u25cb" + Reset // empty circle
	}
}

// ModelBadge returns a colored model name badge.
func ModelBadge(model string) string {
	switch {
	case strings.Contains(model, "sonnet"):
		return Bold + Cyan + "sonnet" + Reset
	case strings.Contains(model, "opus"):
		return Bold + Yellow + "opus" + Reset
	case strings.Contains(model, "haiku"):
		return Bold + Green + "haiku" + Reset
	default:
		return Dim + Gray + model + Reset
	}
}
