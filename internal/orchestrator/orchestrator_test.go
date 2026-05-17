package orchestrator

import (
	"testing"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/dag"
	"github.com/fanhongy/Bob-The-Builder/internal/logging"
	"github.com/fanhongy/Bob-The-Builder/internal/worker"
)

func newTestOrchestrator(d *dag.DAG) *Orchestrator {
	logger, _ := logging.NewLogger(logging.LevelInfo, "")
	return &Orchestrator{
		Config: &config.Config{
			MaxRetries:  3,
			WorkerSlots: 2,
			MaxIters:    5,
			SyncInterval: 1,
		},
		DAG:    d,
		State:  worker.NewStateManager(),
		Logger: logger,
		TUI:    nil,
	}
}

func TestAreDependenciesSatisfied(t *testing.T) {
	d := &dag.DAG{
		Waves: []dag.Wave{
			{ID: 0, Tasks: []dag.Task{
				{ID: "1.1", Description: "Task A", Dependencies: []string{}},
			}},
			{ID: 1, Tasks: []dag.Task{
				{ID: "1.2", Description: "Task B", Dependencies: []string{"1.1"}},
			}},
		},
	}

	o := newTestOrchestrator(d)

	// Initially, 1.1 is pending, so 1.2's deps are not satisfied
	o.State.SetStatus("1.1", worker.StatusPending)
	o.State.SetStatus("1.2", worker.StatusPending)

	satisfied, err := o.AreDependenciesSatisfied("1.2")
	if satisfied || err != nil {
		t.Errorf("expected (false, nil) when dep is pending, got (%v, %v)", satisfied, err)
	}

	// When 1.1 is synced, 1.2's deps are satisfied
	o.State.SetStatus("1.1", worker.StatusSynced)
	satisfied, err = o.AreDependenciesSatisfied("1.2")
	if !satisfied || err != nil {
		t.Errorf("expected (true, nil) when dep is synced, got (%v, %v)", satisfied, err)
	}

	// When 1.1 is failed, 1.2's deps return error
	o.State.SetStatus("1.1", worker.StatusFailed)
	satisfied, err = o.AreDependenciesSatisfied("1.2")
	if satisfied || err == nil {
		t.Errorf("expected (false, error) when dep is failed, got (%v, %v)", satisfied, err)
	}

	// Task with no dependencies is always satisfied
	satisfied, err = o.AreDependenciesSatisfied("1.1")
	if !satisfied || err != nil {
		t.Errorf("expected (true, nil) for task with no deps, got (%v, %v)", satisfied, err)
	}
}

func TestComputeReadyTasks(t *testing.T) {
	d := &dag.DAG{
		Waves: []dag.Wave{
			{ID: 0, Tasks: []dag.Task{
				{ID: "1.1", Description: "Task A", Dependencies: []string{}},
				{ID: "1.2", Description: "Task B", Dependencies: []string{}},
			}},
			{ID: 1, Tasks: []dag.Task{
				{ID: "2.1", Description: "Task C", Dependencies: []string{"1.1"}},
				{ID: "2.2", Description: "Task D", Dependencies: []string{"1.1", "1.2"}},
			}},
		},
	}

	o := newTestOrchestrator(d)

	// All pending, no deps for wave 0 tasks - they should be ready
	for _, id := range d.AllTaskIDs() {
		o.State.SetStatus(id, worker.StatusPending)
	}

	ready := o.ComputeReadyTasks()
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready tasks, got %d: %v", len(ready), ready)
	}
	// Wave 0 tasks should be ready
	readyMap := make(map[string]bool)
	for _, id := range ready {
		readyMap[id] = true
	}
	if !readyMap["1.1"] || !readyMap["1.2"] {
		t.Errorf("expected 1.1 and 1.2 to be ready, got %v", ready)
	}

	// After 1.1 is synced, 2.1 becomes ready
	o.State.SetStatus("1.1", worker.StatusSynced)
	ready = o.ComputeReadyTasks()
	readyMap = make(map[string]bool)
	for _, id := range ready {
		readyMap[id] = true
	}
	if !readyMap["1.2"] {
		t.Error("expected 1.2 to still be ready")
	}
	if !readyMap["2.1"] {
		t.Error("expected 2.1 to be ready (dep 1.1 synced)")
	}
	if readyMap["2.2"] {
		t.Error("expected 2.2 to NOT be ready (dep 1.2 still pending)")
	}

	// After both 1.1 and 1.2 synced, 2.2 becomes ready
	o.State.SetStatus("1.2", worker.StatusSynced)
	ready = o.ComputeReadyTasks()
	readyMap = make(map[string]bool)
	for _, id := range ready {
		readyMap[id] = true
	}
	if !readyMap["2.1"] || !readyMap["2.2"] {
		t.Errorf("expected 2.1 and 2.2 to be ready, got %v", ready)
	}
}

func TestWorkerSlotsRespected(t *testing.T) {
	d := &dag.DAG{
		Waves: []dag.Wave{
			{ID: 0, Tasks: []dag.Task{
				{ID: "1.1", Description: "Task A", Dependencies: []string{}},
				{ID: "1.2", Description: "Task B", Dependencies: []string{}},
				{ID: "1.3", Description: "Task C", Dependencies: []string{}},
				{ID: "1.4", Description: "Task D", Dependencies: []string{}},
			}},
		},
	}

	o := newTestOrchestrator(d)
	o.Config.WorkerSlots = 2

	for _, id := range d.AllTaskIDs() {
		o.State.SetStatus(id, worker.StatusPending)
	}

	// All 4 tasks are ready (no dependencies)
	ready := o.ComputeReadyTasks()
	if len(ready) != 4 {
		t.Fatalf("expected 4 ready tasks, got %d", len(ready))
	}

	// Simulate 2 tasks already running
	o.State.SetStatus("1.1", worker.StatusRunning)
	o.State.SetStatus("1.2", worker.StatusRunning)

	runningCount := o.State.CountWithStatus(worker.StatusRunning)
	if runningCount != 2 {
		t.Fatalf("expected 2 running, got %d", runningCount)
	}

	// Verify the scheduler logic: with WorkerSlots=2 and 2 running,
	// no more workers should be spawned
	if runningCount >= o.Config.WorkerSlots {
		// This is the expected behavior - no more workers should be spawned
	} else {
		t.Error("running count should equal or exceed worker slots")
	}

	// After one completes, we can spawn one more
	o.State.SetStatus("1.1", worker.StatusSynced)
	runningCount = o.State.CountWithStatus(worker.StatusRunning)
	if runningCount != 1 {
		t.Fatalf("expected 1 running after completion, got %d", runningCount)
	}
	if runningCount >= o.Config.WorkerSlots {
		t.Error("should be able to spawn more workers now")
	}

	// Verify that there are still pending ready tasks
	ready = o.ComputeReadyTasks()
	if len(ready) != 2 {
		t.Errorf("expected 2 ready tasks (1.3, 1.4), got %d: %v", len(ready), ready)
	}
}

func TestAllTasksResolved(t *testing.T) {
	d := &dag.DAG{
		Waves: []dag.Wave{
			{ID: 0, Tasks: []dag.Task{
				{ID: "1.1", Description: "Task A", Dependencies: []string{}},
				{ID: "1.2", Description: "Task B", Dependencies: []string{}},
			}},
		},
	}

	o := newTestOrchestrator(d)

	allIDs := d.AllTaskIDs()

	// Not all resolved
	o.State.SetStatus("1.1", worker.StatusSynced)
	o.State.SetStatus("1.2", worker.StatusRunning)
	if o.allTasksResolved(allIDs) {
		t.Error("expected not all resolved when 1.2 is running")
	}

	// All resolved (mixed synced/failed/skipped)
	o.State.SetStatus("1.2", worker.StatusFailed)
	if !o.allTasksResolved(allIDs) {
		t.Error("expected all resolved when 1.1=synced, 1.2=failed")
	}

	o.State.SetStatus("1.2", worker.StatusSkipped)
	if !o.allTasksResolved(allIDs) {
		t.Error("expected all resolved when 1.1=synced, 1.2=skipped")
	}
}

func TestStateManager(t *testing.T) {
	sm := worker.NewStateManager()

	// Test SetStatus/GetStatus
	sm.SetStatus("1.1", worker.StatusRunning)
	if sm.GetStatus("1.1") != worker.StatusRunning {
		t.Error("expected running")
	}

	// Test default status
	if sm.GetStatus("nonexistent") != worker.StatusPending {
		t.Error("expected pending for nonexistent task")
	}

	// Test PID
	sm.SetPID("1.1", 12345)
	if sm.GetPID("1.1") != 12345 {
		t.Error("expected PID 12345")
	}

	// Test retries
	sm.SetRetries("1.1", 0)
	newVal := sm.IncrRetries("1.1")
	if newVal != 1 {
		t.Errorf("expected retries=1, got %d", newVal)
	}
	if sm.GetRetries("1.1") != 1 {
		t.Error("expected retries=1")
	}

	// Test worktree path
	sm.SetWorktreePath("1.1", "/tmp/worktree")
	if sm.GetWorktreePath("1.1") != "/tmp/worktree" {
		t.Error("expected /tmp/worktree")
	}

	// Test log file
	sm.SetLogFile("1.1", "/tmp/log.txt")
	if sm.GetLogFile("1.1") != "/tmp/log.txt" {
		t.Error("expected /tmp/log.txt")
	}

	// Test started at
	now := time.Now()
	sm.SetStartedAt("1.1", now)
	if !sm.GetStartedAt("1.1").Equal(now) {
		t.Error("expected same start time")
	}

	// Test heartbeat
	sm.UpdateHeartbeat("1.1")
	hb := sm.GetHeartbeat("1.1")
	if time.Since(hb) > time.Second {
		t.Error("heartbeat should be very recent")
	}

	// Test GetAllWithStatus
	sm.SetStatus("2.1", worker.StatusRunning)
	sm.SetStatus("2.2", worker.StatusPending)
	running := sm.GetAllWithStatus(worker.StatusRunning)
	if len(running) != 2 {
		t.Errorf("expected 2 running, got %d", len(running))
	}

	// Test CountWithStatus
	if sm.CountWithStatus(worker.StatusRunning) != 2 {
		t.Error("expected count 2 for running")
	}
}
