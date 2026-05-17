package tui

import (
	"fmt"
	"strings"

	"github.com/fanhongy/Bob-The-Builder/internal/dag"
)

// RenderDAGView renders the DAG tree visualization showing execution waves.
func RenderDAGView(d *dag.DAG, states map[string]string, currentWave int, maxRows int, cols int, frame int) []string {
	if d == nil || len(d.Waves) == 0 {
		lines := make([]string, maxRows)
		for i := range lines {
			lines[i] = ""
		}
		return lines
	}

	// Calculate heights for each wave
	waveHeights := make([]int, len(d.Waves))
	totalLines := 0
	for i, wave := range d.Waves {
		tc := len(wave.Tasks)
		h := 1
		if tc > 1 {
			h = tc + 2 // fork line + tasks + merge line
		}
		waveHeights[i] = h
		totalLines += h
	}

	// Calculate scroll offset to keep current wave visible
	scrollStart := 0
	if currentWave >= 0 && currentWave < len(d.Waves) {
		curOffset := 0
		for w := 0; w < currentWave && w < len(d.Waves); w++ {
			curOffset += waveHeights[w]
		}
		curH := waveHeights[currentWave]
		if curOffset+curH > maxRows {
			scrollStart = curOffset - maxRows/3
			if scrollStart < 0 {
				scrollStart = 0
			}
		}
	}

	// Build all lines
	var allLines []string
	for w, wave := range d.Waves {
		taskCount := len(wave.Tasks)
		isParallel := taskCount > 1

		wmark := Dim + Gray
		if w == currentWave {
			wmark = Bold + Cyan
		}
		wlabel := fmt.Sprintf("w%d", w)
		wpad := ""
		if len(wlabel) < 3 {
			wpad = " "
		}

		if isParallel {
			// Fork line
			allLines = append(allLines, fmt.Sprintf("  %s%s%s%s %s\u252c fork(%d)%s",
				wmark, wlabel, wpad, Reset, Dim+Gray, taskCount, Reset))

			// Task lines
			for t, task := range wave.Tasks {
				state := getState(states, task.ID)
				sym := StatusSymbol(state, frame)
				mbadge := ""
				if task.Model != "" {
					mbadge = " " + ModelBadge(task.Model)
				}
				desc := truncate(task.Description, cols-32)
				conn := Dim + Gray + "\u251c" + Reset
				if t == taskCount-1 {
					conn = Dim + Gray + "\u2514" + Reset
				}
				allLines = append(allLines, fmt.Sprintf("      %s\u2500%s %s%s%s%s %s%s%s",
					conn, sym, Bold, task.ID, Reset, mbadge, Dim, desc, Reset))
			}

			// Merge line
			allLines = append(allLines, fmt.Sprintf("      %s\u2534 merge%s", Dim+Gray, Reset))
		} else {
			// Single task inline
			task := wave.Tasks[0]
			state := getState(states, task.ID)
			sym := StatusSymbol(state, frame)
			mbadge := ""
			if task.Model != "" {
				mbadge = " " + ModelBadge(task.Model)
			}
			desc := truncate(task.Description, cols-32)
			allLines = append(allLines, fmt.Sprintf("  %s%s%s%s %s\u2502%s %s %s%s%s%s %s%s%s",
				wmark, wlabel, wpad, Reset, Dim+Gray, Reset, sym, Bold, task.ID, Reset, mbadge, Dim, desc, Reset))
		}
	}

	// Apply scrolling and limit to maxRows
	var result []string
	end := scrollStart + maxRows
	if end > len(allLines) {
		end = len(allLines)
	}
	if scrollStart < len(allLines) {
		result = allLines[scrollStart:end]
	}

	// Pad to maxRows
	for len(result) < maxRows {
		result = append(result, "")
	}
	return result
}

func getState(states map[string]string, id string) string {
	if s, ok := states[id]; ok {
		return s
	}
	return "pending"
}

func truncate(s string, maxLen int) string {
	if maxLen < 3 {
		maxLen = 3
	}
	// Strip ANSI from length calculation approximation
	plain := stripANSI(s)
	if len(plain) <= maxLen {
		return s
	}
	// Truncate the original string approximately
	if len(s) > maxLen-3 {
		return s[:maxLen-3] + "..."
	}
	return s
}

func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip escape sequence
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
					i++
				}
				if i < len(s) {
					i++ // skip final char
				}
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}
