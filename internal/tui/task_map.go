package tui

import (
	"fmt"
	"strings"

	"github.com/fanhongy/Bob-The-Builder/internal/dag"
)

// RenderTaskMap renders a node-graph task map visualization.
func RenderTaskMap(d *dag.DAG, states map[string]string, frame int, maxRows int, cols int) []string {
	if d == nil || d.TaskCount() == 0 {
		lines := make([]string, maxRows)
		for i := range lines {
			lines[i] = ""
		}
		return lines
	}

	// Group tasks by parent
	type parentGroup struct {
		parent string
		tasks  []string
	}
	var parents []parentGroup
	parentMap := make(map[string]int)

	allTasks := d.AllTaskIDs()
	for _, tid := range allTasks {
		parent := getParent(tid)
		if idx, ok := parentMap[parent]; ok {
			parents[idx].tasks = append(parents[idx].tasks, tid)
		} else {
			parentMap[parent] = len(parents)
			parents = append(parents, parentGroup{parent: parent, tasks: []string{tid}})
		}
	}

	// Count states
	doneC, runC, pendC, failC := 0, 0, 0, 0
	for _, tid := range allTasks {
		switch getState(states, tid) {
		case "completed", "synced":
			doneC++
		case "running":
			runC++
		case "failed":
			failC++
		default:
			pendC++
		}
	}

	var lines []string

	// Header
	hdr := fmt.Sprintf("  %sTASK MAP%s  %s%d%s%s done  %s%s%d%s%s active  %s%s%d pending%s",
		Bold+White, Reset,
		Bold+White, doneC, Reset, Dim+Gray,
		Reset, Bold+Cyan, runC, Reset, Dim+Gray,
		Dim+Gray, "", pendC, Reset)
	if failC > 0 {
		hdr += fmt.Sprintf("  %s%d%s%s failed%s", Bold+Red, failC, Reset, Dim+Gray, Reset)
	}
	lines = append(lines, hdr)

	// Find max subtasks per parent
	maxSubs := 0
	for _, pg := range parents {
		if len(pg.tasks) > maxSubs {
			maxSubs = len(pg.tasks)
		}
	}

	pcount := len(parents)
	if pcount == 0 {
		for len(lines) < maxRows {
			lines = append(lines, "")
		}
		return lines
	}

	// Adaptive sizing
	nodeW := 3
	connW := 1
	retW := 1
	drawW := cols - 4
	if pcount > 1 {
		for _, tryNw := range []int{3, 1} {
			minTotal := pcount*tryNw + (pcount-1)*(connW+retW+1)
			if minTotal <= drawW {
				nodeW = tryNw
				break
			}
		}
	}

	spineGap := 3
	if pcount > 1 {
		spineGap = (drawW - pcount*nodeW - (pcount-1)*(connW+retW)) / (pcount - 1)
		if spineGap > 10 {
			spineGap = 10
		}
		if spineGap < 1 {
			spineGap = 1
		}
	}

	// Parent labels row
	lblLine := "  "
	for pp, pg := range parents {
		pst := parentState(pg.tasks, states)
		lc := nodeColor(pst, "", frame)
		lpad := (nodeW - len(pg.parent)) / 2
		rpad := nodeW - len(pg.parent) - lpad
		if lpad < 0 {
			lpad = 0
		}
		if rpad < 0 {
			rpad = 0
		}
		lblLine += strings.Repeat(" ", lpad) + lc + pg.parent + Reset + strings.Repeat(" ", rpad)
		if pp < pcount-1 {
			padTotal := connW + retW + spineGap
			lblLine += strings.Repeat(" ", padTotal)
		}
	}
	lines = append(lines, lblLine)

	// Spine row (first subtask of each parent)
	spineLine := "  "
	for pp, pg := range parents {
		tid := pg.tasks[0]
		st := getState(states, tid)
		task := d.GetTask(tid)
		model := ""
		if task != nil {
			model = task.Model
		}
		spineLine += renderNode(st, model, nodeW, frame)

		if pp < pcount-1 {
			pst := parentState(pg.tasks, states)
			pdc := dimColor(pst, "")
			fch := "\u2500" // horizontal line
			if pst != "completed" && pst != "synced" && pst != "running" && pst != "failed" {
				fch = "\u00b7" // middle dot
			}

			conn := strings.Repeat(fch, connW)
			spineLine += pdc + conn + Reset

			if len(pg.tasks) > 1 {
				spineLine += pdc + "\u252c" + Reset // fork from spine
			} else {
				spineLine += pdc + fch + Reset
			}

			sgap := strings.Repeat(fch, spineGap)
			spineLine += pdc + sgap + Reset
		}
	}
	lines = append(lines, spineLine)

	// Subtask rows
	availSubRows := (maxRows - 4) / 2
	if availSubRows < 1 {
		availSubRows = 1
	}
	renderSubs := maxSubs
	if renderSubs > availSubRows+1 {
		renderSubs = availSubRows + 1
	}

	for si := 1; si < renderSubs; si++ {
		if len(lines) >= maxRows-1 {
			break
		}

		// Vertical connector row
		vertLine := "  "
		for pp, pg := range parents {
			sc := len(pg.tasks)
			if si < sc {
				prevTid := pg.tasks[si-1]
				prevSt := getState(states, prevTid)
				dc := dimColor(prevSt, "")
				vertLine += renderVert(dc, nodeW)
			} else {
				vertLine += strings.Repeat(" ", nodeW)
			}

			if pp < pcount-1 {
				vertLine += strings.Repeat(" ", connW)
				if sc > 1 && si < sc {
					pst := parentState(pg.tasks, states)
					pdc := dimColor(pst, "")
					vertLine += pdc + "\u2502" + Reset // vertical
				} else {
					vertLine += " "
				}
				vertLine += strings.Repeat(" ", spineGap)
			}
		}
		lines = append(lines, vertLine)

		if len(lines) >= maxRows-1 {
			break
		}

		// Node row
		nodeLine := "  "
		for pp, pg := range parents {
			sc := len(pg.tasks)
			if si < sc {
				tid := pg.tasks[si]
				st := getState(states, tid)
				task := d.GetTask(tid)
				model := ""
				if task != nil {
					model = task.Model
				}
				dc := dimColor(st, model)
				nodeLine += renderNode(st, model, nodeW, frame)

				if pp < pcount-1 {
					fch := "\u2500"
					if st != "completed" && st != "synced" && st != "running" && st != "failed" {
						fch = "\u00b7"
					}
					conn := strings.Repeat(fch, connW)
					nodeLine += dc + conn + Reset

					if si+1 < sc {
						nodeLine += dc + "\u2524" + Reset // return connector
					} else {
						nodeLine += dc + "\u256f" + Reset // last return
					}
					nodeLine += strings.Repeat(" ", spineGap)
				}
			} else {
				nodeLine += strings.Repeat(" ", nodeW)
				if pp < pcount-1 {
					nodeLine += strings.Repeat(" ", connW)
					nodeLine += " "
					nodeLine += strings.Repeat(" ", spineGap)
				}
			}
		}
		lines = append(lines, nodeLine)
	}

	// Legend
	legend := fmt.Sprintf("  %s  \u25cf  %s%sdone  %s%s  \u25c9  %s%sactive  %s%s  \u25cb  pending  %s%s  \u2717  %s%sfailed%s",
		Bold+White, Reset, Dim+Gray, Reset,
		Bold+Cyan, Reset, Dim+Gray, Reset,
		Dim+Gray, Reset,
		Bold+Red, Reset, Dim+Gray, Reset)
	lines = append(lines, legend)

	// Pad to maxRows
	for len(lines) < maxRows {
		lines = append(lines, "")
	}

	// Trim to maxRows
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}

	return lines
}

func getParent(taskID string) string {
	parts := strings.SplitN(taskID, ".", 2)
	return parts[0]
}

func parentState(tasks []string, states map[string]string) string {
	worst := "completed"
	for _, tid := range tasks {
		st := getState(states, tid)
		switch st {
		case "failed":
			return "failed"
		case "pending":
			if worst != "failed" {
				worst = "pending"
			}
		case "running":
			if worst == "completed" || worst == "synced" {
				worst = "running"
			}
		}
	}
	return worst
}

func nodeColor(state, model string, frame int) string {
	switch state {
	case "completed", "synced":
		return Bold + White
	case "running":
		mc := modelColor(model)
		if frame%2 == 0 {
			return Bold + mc
		}
		return Dim + mc
	case "failed":
		return Bold + Red
	default:
		return Dim + Gray
	}
}

func dimColor(state, model string) string {
	switch state {
	case "completed", "synced":
		return Bold + White
	case "running":
		return Dim + modelColor(model)
	case "failed":
		return Dim + Red
	default:
		return Dim + Gray
	}
}

func modelColor(model string) string {
	switch {
	case strings.Contains(model, "opus"):
		return Yellow
	case strings.Contains(model, "sonnet"):
		return Cyan
	default:
		return Cyan
	}
}

func renderNode(state, model string, nodeW int, frame int) string {
	nc := nodeColor(state, model, frame)
	sym := "\u25cb" // empty circle
	switch state {
	case "completed", "synced":
		sym = "\u25cf" // filled circle
	case "running":
		if frame%2 == 0 {
			sym = "\u25c9" // fisheye
		} else {
			sym = "\u25cf" // filled circle
		}
	case "failed":
		sym = "\u2717" // cross
	}

	lpad := (nodeW - 1) / 2
	rpad := nodeW - 1 - lpad
	return strings.Repeat(" ", lpad) + nc + sym + Reset + strings.Repeat(" ", rpad)
}

func renderVert(color string, nodeW int) string {
	lpad := (nodeW - 1) / 2
	rpad := nodeW - 1 - lpad
	return strings.Repeat(" ", lpad) + color + "\u2502" + Reset + strings.Repeat(" ", rpad)
}
