package tui

import (
	"fmt"
)

// WorkerInfo holds information about a running worker for display.
type WorkerInfo struct {
	TaskID      string
	Description string
	Model       string
	LogFile     string
}

// RenderWorkersPanel renders the active workers panel.
func RenderWorkersPanel(workers []WorkerInfo, selectedIdx int, workerSlots int, reviewReserved int, phase string, maxRows int, cols int) []string {
	var lines []string

	// Header
	running := len(workers)
	hdr := fmt.Sprintf("  %sACTIVE WORKERS%s  %s(%d/%d worker slots, %d reserved for review)%s",
		Bold+White, Reset, Dim+LightGray, running, workerSlots, reviewReserved, Reset)
	lines = append(lines, hdr)

	if running == 0 {
		phaseMsg := "idle"
		switch phase {
		case "syncing":
			phaseMsg = "syncing results to main..."
		case "merging":
			phaseMsg = "merging wave results..."
		case "reviewing":
			phaseMsg = "quality review in progress..."
		case "waiting":
			phaseMsg = "preparing next wave..."
		case "done":
			phaseMsg = "all workers finished"
		case "executing":
			phaseMsg = "waiting for tasks..."
		}
		lines = append(lines, fmt.Sprintf("    %s%s%s", Dim+Gray, phaseMsg, Reset))
	} else {
		for i, w := range workers {
			if len(lines) >= maxRows {
				break
			}
			mbadge := ""
			if w.Model != "" {
				mbadge = " " + ModelBadge(w.Model)
			}
			desc := w.Description
			if len(desc) > cols-35 {
				desc = desc[:cols-38] + "..."
			}
			if i == selectedIdx {
				lines = append(lines, fmt.Sprintf("    %s\u25b8%s %s%s%s%s%s %s%s%s",
					Cyan, Reset, Bold+Cyan, w.TaskID, Reset, mbadge, "", Dim, desc, Reset))
			} else {
				lines = append(lines, fmt.Sprintf("      %s%s%s%s %s%s%s",
					Bold, w.TaskID, Reset, mbadge, Dim, desc, Reset))
			}
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
