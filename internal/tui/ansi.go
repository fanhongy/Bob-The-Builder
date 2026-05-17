package tui

import "fmt"

// ANSI escape code constants for terminal styling.
const (
	Reset     = "\033[0m"
	Bold      = "\033[1m"
	Dim       = "\033[2m"
	White     = "\033[97m"
	Gray      = "\033[90m"
	LightGray = "\033[37m"
	Cyan      = "\033[96m"
	Blue      = "\033[94m"
	Yellow    = "\033[93m"
	Red       = "\033[91m"
	Magenta   = "\033[95m"
	Green     = "\033[92m"
	BgCyan    = "\033[46m"
	Reverse   = "\033[7m"
)

// Goto moves the cursor to the specified row and column (1-indexed).
func Goto(row, col int) string {
	return fmt.Sprintf("\033[%d;%dH", row, col)
}

// ClearLine clears from cursor to end of line.
func ClearLine() string {
	return "\033[K"
}

// ClearScreen clears the entire screen.
func ClearScreen() string {
	return "\033[2J"
}

// HideCursor hides the terminal cursor.
func HideCursor() string {
	return "\033[?25l"
}

// ShowCursor shows the terminal cursor.
func ShowCursor() string {
	return "\033[?25h"
}

// EnterAltScreen switches to the alternate screen buffer.
func EnterAltScreen() string {
	return "\033[?1049h"
}

// ExitAltScreen switches back from the alternate screen buffer.
func ExitAltScreen() string {
	return "\033[?1049l"
}

// ColorText wraps text with a color code and reset.
func ColorText(text, color string) string {
	return color + text + Reset
}
