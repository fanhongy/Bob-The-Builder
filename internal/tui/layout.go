package tui

import (
	"fmt"
	"strings"
)

// Layout calculates row budgets for each TUI panel based on terminal size.
type Layout struct {
	Rows int
	Cols int

	HeaderRows       int
	ProgressRows     int
	DAGRows          int
	TaskMapRows      int
	WorkerRows       int
	OutputRows       int
	ActivityRows     int
	FooterRows       int
	SeparatorCount   int
}

// ComputeLayout calculates the layout given terminal dimensions and worker slot count.
func ComputeLayout(rows, cols, workerSlots, maxSubsPerParent int) Layout {
	l := Layout{
		Rows:           rows,
		Cols:           cols,
		HeaderRows:     3,
		ProgressRows:   3,
		FooterRows:     1,
		SeparatorCount: 5,
	}

	// Worker panel height
	l.WorkerRows = workerSlots + 2
	if l.WorkerRows < 4 {
		l.WorkerRows = 4
	}
	if l.WorkerRows > 10 {
		l.WorkerRows = 10
	}

	// Output panel
	l.OutputRows = 12

	// Activity log
	l.ActivityRows = 6

	// Task map: header + labels + spine + subtask pairs + legend
	l.TaskMapRows = 4 + (maxSubsPerParent-1)*2
	if l.TaskMapRows < 5 {
		l.TaskMapRows = 5
	}
	if l.TaskMapRows > 16 {
		l.TaskMapRows = 16
	}

	// DAG gets remaining space
	fixed := l.HeaderRows + l.ProgressRows + l.TaskMapRows + l.WorkerRows +
		l.OutputRows + l.ActivityRows + l.SeparatorCount + l.FooterRows + 2
	l.DAGRows = rows - fixed
	if l.DAGRows < 5 {
		l.DAGRows = 5
	}

	return l
}

// RenderFrame builds the complete TUI frame as a single string.
func RenderFrame(l Layout, header []string, progress string, dagLines []string, taskMapLines []string, workerLines []string, outputLines []string, activityLines []string) string {
	var buf strings.Builder
	row := 1

	// Header
	for _, line := range header {
		buf.WriteString(Goto(row, 1))
		buf.WriteString(line)
		buf.WriteString(ClearLine())
		row++
	}
	for row <= l.HeaderRows {
		buf.WriteString(Goto(row, 1))
		buf.WriteString(ClearLine())
		row++
	}

	// Progress
	buf.WriteString(Goto(row, 1))
	buf.WriteString(ClearLine())
	row++
	buf.WriteString(Goto(row, 1))
	buf.WriteString(progress)
	buf.WriteString(ClearLine())
	row++
	buf.WriteString(Goto(row, 1))
	buf.WriteString(ClearLine())
	row++

	// Separator
	buf.WriteString(renderSeparator(row, l.Cols))
	row++

	// DAG header
	dagHeader := fmt.Sprintf("  %sEXECUTION GRAPH%s", Bold+White, Reset)
	buf.WriteString(Goto(row, 1))
	buf.WriteString(dagHeader)
	buf.WriteString(ClearLine())
	row++

	// DAG lines
	for _, line := range dagLines {
		if row > l.Rows-l.FooterRows {
			break
		}
		buf.WriteString(Goto(row, 1))
		buf.WriteString(line)
		buf.WriteString(ClearLine())
		row++
	}

	// Separator
	buf.WriteString(renderSeparator(row, l.Cols))
	row++

	// Task map lines
	for _, line := range taskMapLines {
		if row > l.Rows-l.FooterRows {
			break
		}
		buf.WriteString(Goto(row, 1))
		buf.WriteString(line)
		buf.WriteString(ClearLine())
		row++
	}

	// Separator
	buf.WriteString(renderSeparator(row, l.Cols))
	row++

	// Worker lines
	for _, line := range workerLines {
		if row > l.Rows-l.FooterRows {
			break
		}
		buf.WriteString(Goto(row, 1))
		buf.WriteString(line)
		buf.WriteString(ClearLine())
		row++
	}

	// Separator
	buf.WriteString(renderSeparator(row, l.Cols))
	row++

	// Output lines
	for _, line := range outputLines {
		if row > l.Rows-l.FooterRows {
			break
		}
		buf.WriteString(Goto(row, 1))
		buf.WriteString(line)
		buf.WriteString(ClearLine())
		row++
	}

	// Separator
	buf.WriteString(renderSeparator(row, l.Cols))
	row++

	// Activity lines
	for _, line := range activityLines {
		if row > l.Rows-l.FooterRows {
			break
		}
		buf.WriteString(Goto(row, 1))
		buf.WriteString(line)
		buf.WriteString(ClearLine())
		row++
	}

	// Clear remaining space
	for row < l.Rows {
		buf.WriteString(Goto(row, 1))
		buf.WriteString(ClearLine())
		row++
	}

	// Footer
	footer := fmt.Sprintf("  %s\u2191\u2193 select worker  q quit  ctrl-c abort%s", Dim+Gray, Reset)
	buf.WriteString(Goto(l.Rows, 1))
	buf.WriteString(footer)
	buf.WriteString(ClearLine())

	return buf.String()
}

func renderSeparator(row, cols int) string {
	return Goto(row, 1) + Dim + Gray + strings.Repeat("\u2500", cols) + Reset
}
