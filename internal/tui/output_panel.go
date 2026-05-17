package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// RenderOutputPanel renders the live worker output panel.
func RenderOutputPanel(selectedTaskID string, logFile string, maxRows int, cols int) []string {
	var lines []string

	// Header
	if selectedTaskID != "" {
		lines = append(lines, fmt.Sprintf("  %sWORKER OUTPUT%s  %s[%s]  up/down to switch%s",
			Bold+White, Reset, Dim+Gray, selectedTaskID, Reset))
	} else {
		lines = append(lines, fmt.Sprintf("  %sWORKER OUTPUT%s  %sup/down to switch%s",
			Bold+White, Reset, Dim+Gray, Reset))
	}

	contentRows := maxRows - 1

	if selectedTaskID == "" {
		lines = append(lines, fmt.Sprintf("    %sno active workers%s", LightGray, Reset))
	} else if logFile == "" {
		lines = append(lines, fmt.Sprintf("    %swaiting for output...%s", LightGray, Reset))
	} else {
		logLines := tailLogFile(logFile, contentRows)
		for _, ln := range logLines {
			// Strip ANSI codes and truncate
			clean := stripANSI(ln)
			clean = strings.TrimRight(clean, "\r\n")
			if len(clean) > cols-8 {
				clean = clean[:cols-8]
			}
			lines = append(lines, fmt.Sprintf("    %s%s%s", LightGray, clean, Reset))
		}
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

// tailLogFile reads the last n lines from a file, filtering noise.
func tailLogFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{"waiting for output..."}
	}
	defer f.Close()

	// Read all lines (for tail behavior)
	var allLines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*64), 1024*64)
	for scanner.Scan() {
		line := scanner.Text()
		// Filter noise
		if isNoiseLine(line) {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		allLines = append(allLines, line)
	}

	// Return last n lines
	if len(allLines) > n {
		return allLines[len(allLines)-n:]
	}
	return allLines
}

// isNoiseLine returns true if the line should be filtered from output.
func isNoiseLine(line string) bool {
	noisePatterns := []string{
		"STARTED",
		"ITERATION",
		"RESPONSE_START",
		"RESPONSE_END",
	}
	stripped := stripANSI(line)
	for _, p := range noisePatterns {
		if strings.Contains(stripped, p) {
			// Only filter if it looks like an internal marker
			trimmed := strings.TrimSpace(stripped)
			if len(trimmed) > 0 && (trimmed[0] >= '0' && trimmed[0] <= '9') {
				return true
			}
		}
	}
	return false
}
