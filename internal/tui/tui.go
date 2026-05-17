package tui

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/dag"
)

// TUI is the main interactive terminal user interface.
type TUI struct {
	dag         *dag.DAG
	states      map[string]string // taskID -> status string
	config      *config.Config
	activityLog *ActivityLog

	selectedIdx int
	currentWave int
	completed   int
	total       int
	elapsed     time.Duration
	phase       string
	frame       int
	running     bool

	mu      sync.Mutex
	inputCh chan KeyEvent
	done    chan struct{}

	restoreTerminal func()
	inputReader     *InputReader
	spinner         *Spinner
}

// NewTUI creates a new TUI instance.
func NewTUI(d *dag.DAG, cfg *config.Config) *TUI {
	states := make(map[string]string)
	for _, id := range d.AllTaskIDs() {
		states[id] = "pending"
	}

	return &TUI{
		dag:         d,
		states:      states,
		config:      cfg,
		activityLog: NewActivityLog(50),
		selectedIdx: -1,
		currentWave: -1,
		total:       d.TaskCount(),
		phase:       "executing",
		done:        make(chan struct{}),
		spinner:     NewSpinner(),
	}
}

// Start initializes the TUI: enters alternate screen, hides cursor, enables raw mode,
// and starts the render loop and input reader.
func (t *TUI) Start() error {
	// Enter alternate screen and hide cursor
	os.Stdout.WriteString(EnterAltScreen() + HideCursor())

	// Enable raw mode
	restore, err := EnableRawMode()
	if err != nil {
		// If raw mode fails, still run TUI without keyboard input
		restore = func() {}
	}
	t.restoreTerminal = restore

	// Start input reader
	t.inputReader = NewInputReader()
	t.inputCh = t.inputReader.ch

	// Handle SIGWINCH for terminal resize
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-sigWinch:
				// Just triggers a re-render on next tick
			case <-t.done:
				signal.Stop(sigWinch)
				return
			}
		}
	}()

	t.running = true

	// Start render loop at ~2 FPS (500ms interval)
	go t.renderLoop()

	return nil
}

// Stop restores the terminal state and exits the alternate screen.
func (t *TUI) Stop() {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	t.running = false
	t.mu.Unlock()

	close(t.done)

	if t.inputReader != nil {
		t.inputReader.Close()
	}

	if t.restoreTerminal != nil {
		t.restoreTerminal()
	}

	// Show cursor and exit alternate screen
	os.Stdout.WriteString(ShowCursor() + ExitAltScreen())
}

// SetTaskState updates the state of a task (implements TUIInterface).
func (t *TUI) SetTaskState(id string, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states[id] = state
}

// AddEvent adds an event to the activity log (implements TUIInterface).
func (t *TUI) AddEvent(msg string) {
	t.activityLog.AddEvent(msg)
}

// SetPhase sets the current execution phase (implements TUIInterface).
func (t *TUI) SetPhase(phase string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.phase = phase
}

// SetProgress updates the progress counters (implements TUIInterface).
func (t *TUI) SetProgress(completed, total int, elapsed time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.completed = completed
	t.total = total
	t.elapsed = elapsed
}

// SetCurrentWave sets the current executing wave.
func (t *TUI) SetCurrentWave(wave int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentWave = wave
}

func (t *TUI) renderLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case key := <-t.inputCh:
			t.handleInput(key)
			t.render()
		case <-ticker.C:
			t.render()
		}
	}
}

func (t *TUI) handleInput(key KeyEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	workers := t.getRunningWorkers()
	count := len(workers)

	switch key.Type {
	case KeyUp:
		if count > 0 {
			if t.selectedIdx <= 0 {
				t.selectedIdx = count - 1
			} else {
				t.selectedIdx--
			}
		}
	case KeyDown:
		if count > 0 {
			t.selectedIdx = (t.selectedIdx + 1) % count
		}
	case KeyQ:
		// Do nothing for now - orchestrator handles shutdown via signals
	}
}

func (t *TUI) render() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.frame++
	t.spinner.Tick()

	rows, cols := GetTerminalSize()

	// Get running workers
	workers := t.getRunningWorkers()

	// Auto-select logic
	if len(workers) == 0 {
		t.selectedIdx = -1
	} else if t.selectedIdx < 0 || t.selectedIdx >= len(workers) {
		t.selectedIdx = 0
	}

	// Calculate max subs per parent for layout
	maxSubsPerParent := 1
	if t.dag != nil {
		parentCounts := make(map[string]int)
		for _, id := range t.dag.AllTaskIDs() {
			parts := strings.SplitN(id, ".", 2)
			parentCounts[parts[0]]++
		}
		for _, c := range parentCounts {
			if c > maxSubsPerParent {
				maxSubsPerParent = c
			}
		}
	}

	layout := ComputeLayout(rows, cols, t.config.WorkerSlots, maxSubsPerParent)

	// Header
	header := t.renderHeader(cols)

	// Progress
	progress := ProgressBar(t.completed, t.total, cols-30, t.elapsed)

	// DAG view
	dagLines := RenderDAGView(t.dag, t.states, t.currentWave, layout.DAGRows, cols, t.frame)

	// Task map
	taskMapLines := RenderTaskMap(t.dag, t.states, t.frame, layout.TaskMapRows, cols)

	// Workers panel
	workerInfos := make([]WorkerInfo, len(workers))
	for i, w := range workers {
		workerInfos[i] = w
	}
	workerLines := RenderWorkersPanel(workerInfos, t.selectedIdx, t.config.WorkerSlots, t.config.ReviewReservedSlots, t.phase, layout.WorkerRows, cols)

	// Output panel
	selectedTaskID := ""
	selectedLogFile := ""
	if t.selectedIdx >= 0 && t.selectedIdx < len(workers) {
		selectedTaskID = workers[t.selectedIdx].TaskID
		selectedLogFile = workers[t.selectedIdx].LogFile
	}
	outputLines := RenderOutputPanel(selectedTaskID, selectedLogFile, layout.OutputRows, cols)

	// Activity log
	activityLines := t.activityLog.RenderActivityLog(layout.ActivityRows, cols)

	// Build frame
	frame := RenderFrame(layout, header, progress, dagLines, taskMapLines, workerLines, outputLines, activityLines)

	// Write entire frame in one call to minimize flicker
	os.Stdout.WriteString(frame)
}

func (t *TUI) renderHeader(cols int) []string {
	specName := t.config.SpecName
	if specName == "" && t.config.SpecDir != "" {
		specName = t.config.SpecDir
	}

	mode := "concurrent"
	if t.config.Sequential {
		mode = "sequential"
	}

	reviewStatus := "on"
	if !t.config.EnableReview {
		reviewStatus = "off"
	}

	line1 := fmt.Sprintf("  %s%sBOB THE BUILDER%s %sconcurrent task orchestrator%s",
		Bold, White, Reset, Dim, Reset)
	line2 := fmt.Sprintf("  %sspec %s%s%s%s  mode %s%s%s%s  workers %s%d%s%s+%s%d%s%sr  review %s%s%s",
		Dim+LightGray, Reset, White, specName, Reset+Dim+LightGray,
		Reset, White, mode, Reset+Dim+LightGray,
		Reset+White, t.config.WorkerSlots, Reset+Dim+LightGray, Reset,
		Reset+White, t.config.ReviewReservedSlots, Reset+Dim+LightGray, Reset+Dim+LightGray,
		Reset+White, reviewStatus, Reset)

	_ = cols // available for truncation if needed
	return []string{line1, line2}
}

func (t *TUI) getRunningWorkers() []WorkerInfo {
	var workers []WorkerInfo
	if t.dag == nil {
		return workers
	}
	for _, id := range t.dag.AllTaskIDs() {
		if state, ok := t.states[id]; ok && state == "running" {
			task := t.dag.GetTask(id)
			desc := ""
			model := ""
			if task != nil {
				desc = task.Description
				model = task.Model
			}
			logFile := ""
			if t.config.LogDir != "" {
				logFile = t.config.LogDir + "/task_" + strings.ReplaceAll(id, ".", "_") + ".log"
			}
			workers = append(workers, WorkerInfo{
				TaskID:      id,
				Description: desc,
				Model:       model,
				LogFile:     logFile,
			})
		}
	}
	return workers
}
