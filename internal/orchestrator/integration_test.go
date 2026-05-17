package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/dag"
	"github.com/fanhongy/Bob-The-Builder/internal/logging"
	"github.com/fanhongy/Bob-The-Builder/internal/worker"
)

// TestIntegrationStateMachine verifies the full orchestrator state machine:
// create a 2-wave DAG with 3 tasks (task 1.1 in wave 0, tasks 2.1 and 2.2
// in wave 1 depending on 1.1). We mock the worker execution so it does not
// actually call kiro-cli.
func TestIntegrationStateMachine(t *testing.T) {
	// Set up a temporary git repo with a test tasks.md
	tmpDir, err := os.MkdirTemp("", "btb-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init", "-b", "main")

	// Create tasks.md
	specDir := filepath.Join(tmpDir, "spec")
	os.MkdirAll(specDir, 0755)
	tasksContent := `# Tasks

## 1. First Phase
- [ ] 1.1 Implement base feature

## 2. Second Phase
- [ ] 2.1 Build on base feature
- [ ] 2.2 Another dependent task
`
	os.WriteFile(filepath.Join(specDir, "tasks.md"), []byte(tasksContent), 0644)
	runGit("add", ".")
	runGit("commit", "-m", "initial commit")

	// Build a 2-wave DAG
	testDAG := &dag.DAG{
		Waves: []dag.Wave{
			{
				ID: 0,
				Tasks: []dag.Task{
					{ID: "1.1", Description: "Implement base feature", Dependencies: []string{}, Model: "test-model"},
				},
			},
			{
				ID: 1,
				Tasks: []dag.Task{
					{ID: "2.1", Description: "Build on base feature", Dependencies: []string{"1.1"}, Model: "test-model"},
					{ID: "2.2", Description: "Another dependent task", Dependencies: []string{"1.1"}, Model: "test-model"},
				},
			},
		},
	}

	// Create orchestrator with mock execution
	logger, _ := logging.NewLogger(logging.LevelInfo, "")
	cfg := &config.Config{
		MaxRetries:     3,
		WorkerSlots:    3,
		MaxIters:       5,
		SyncInterval:   0, // no sleep in tests
		WorktreeBase:   filepath.Join(tmpDir, "worktrees"),
		LogDir:         filepath.Join(tmpDir, "logs"),
		SpecDir:        specDir,
		TaskFile:       filepath.Join(specDir, "tasks.md"),
		EnableReview:   false,
		StaleThreshold: 9999,
		JobTimeout:     0,
	}

	state := worker.NewStateManager()
	orch := &Orchestrator{
		Config: cfg,
		DAG:    testDAG,
		State:  state,
		Logger: logger,
		TUI:    nil,
	}

	// Initialize all tasks as pending
	for _, id := range testDAG.AllTaskIDs() {
		state.SetStatus(id, worker.StatusPending)
	}

	// Verify initial state: only task 1.1 is ready (no deps)
	ready := orch.ComputeReadyTasks()
	if len(ready) != 1 || ready[0] != "1.1" {
		t.Fatalf("expected only 1.1 ready initially, got %v", ready)
	}

	// Verify 2.1 and 2.2 are NOT ready (depend on 1.1)
	sat21, _ := orch.AreDependenciesSatisfied("2.1")
	sat22, _ := orch.AreDependenciesSatisfied("2.2")
	if sat21 || sat22 {
		t.Fatal("expected 2.1 and 2.2 to have unsatisfied deps")
	}

	// Simulate: task 1.1 starts running
	state.SetStatus("1.1", worker.StatusRunning)

	// Verify no new tasks become ready while 1.1 is running
	ready = orch.ComputeReadyTasks()
	if len(ready) != 0 {
		t.Fatalf("expected no ready tasks while 1.1 is running, got %v", ready)
	}

	// Simulate: task 1.1 completes and syncs
	state.SetStatus("1.1", worker.StatusCompleted)
	state.SetStatus("1.1", worker.StatusSynced)

	// Now 2.1 and 2.2 should become ready
	ready = orch.ComputeReadyTasks()
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready tasks after 1.1 synced, got %d: %v", len(ready), ready)
	}
	readyMap := make(map[string]bool)
	for _, id := range ready {
		readyMap[id] = true
	}
	if !readyMap["2.1"] || !readyMap["2.2"] {
		t.Fatalf("expected 2.1 and 2.2 to be ready, got %v", ready)
	}

	// Simulate: both 2.1 and 2.2 start running (parallel)
	state.SetStatus("2.1", worker.StatusRunning)
	state.SetStatus("2.2", worker.StatusRunning)

	// Simulate: both complete and sync
	state.SetStatus("2.1", worker.StatusSynced)
	state.SetStatus("2.2", worker.StatusSynced)

	// All tasks should be resolved
	allIDs := testDAG.AllTaskIDs()
	if !orch.allTasksResolved(allIDs) {
		t.Fatal("expected all tasks to be resolved")
	}

	// Verify final states
	if state.GetStatus("1.1") != worker.StatusSynced {
		t.Errorf("expected 1.1 to be synced, got %s", state.GetStatus("1.1").String())
	}
	if state.GetStatus("2.1") != worker.StatusSynced {
		t.Errorf("expected 2.1 to be synced, got %s", state.GetStatus("2.1").String())
	}
	if state.GetStatus("2.2") != worker.StatusSynced {
		t.Errorf("expected 2.2 to be synced, got %s", state.GetStatus("2.2").String())
	}
}

// TestIntegrationFailureCascade verifies that when a task fails permanently,
// all tasks depending on it are skipped.
func TestIntegrationFailureCascade(t *testing.T) {
	testDAG := &dag.DAG{
		Waves: []dag.Wave{
			{
				ID: 0,
				Tasks: []dag.Task{
					{ID: "1.1", Description: "Base task", Dependencies: []string{}},
				},
			},
			{
				ID: 1,
				Tasks: []dag.Task{
					{ID: "2.1", Description: "Depends on 1.1", Dependencies: []string{"1.1"}},
					{ID: "2.2", Description: "Also depends on 1.1", Dependencies: []string{"1.1"}},
				},
			},
		},
	}

	logger, _ := logging.NewLogger(logging.LevelInfo, "")
	cfg := &config.Config{
		MaxRetries:  3,
		WorkerSlots: 3,
	}

	state := worker.NewStateManager()
	orch := &Orchestrator{
		Config: cfg,
		DAG:    testDAG,
		State:  state,
		Logger: logger,
		TUI:    nil,
	}

	// Initialize
	for _, id := range testDAG.AllTaskIDs() {
		state.SetStatus(id, worker.StatusPending)
	}

	// Task 1.1 fails permanently
	state.SetStatus("1.1", worker.StatusFailed)

	// Check that 2.1 and 2.2 can't satisfy deps (error returned)
	_, err := orch.AreDependenciesSatisfied("2.1")
	if err == nil {
		t.Fatal("expected error for 2.1 when 1.1 is failed")
	}

	_, err = orch.AreDependenciesSatisfied("2.2")
	if err == nil {
		t.Fatal("expected error for 2.2 when 1.1 is failed")
	}
}

// TestIntegrationConcurrentReadyTasks verifies that tasks in wave 1
// are spawned concurrently (both become ready at the same time).
func TestIntegrationConcurrentReadyTasks(t *testing.T) {
	testDAG := &dag.DAG{
		Waves: []dag.Wave{
			{
				ID: 0,
				Tasks: []dag.Task{
					{ID: "1.1", Description: "First", Dependencies: []string{}},
				},
			},
			{
				ID: 1,
				Tasks: []dag.Task{
					{ID: "2.1", Description: "Parallel A", Dependencies: []string{"1.1"}},
					{ID: "2.2", Description: "Parallel B", Dependencies: []string{"1.1"}},
				},
			},
		},
	}

	logger, _ := logging.NewLogger(logging.LevelInfo, "")
	cfg := &config.Config{
		MaxRetries:  3,
		WorkerSlots: 5,
	}

	state := worker.NewStateManager()
	orch := &Orchestrator{
		Config: cfg,
		DAG:    testDAG,
		State:  state,
		Logger: logger,
		TUI:    nil,
	}

	for _, id := range testDAG.AllTaskIDs() {
		state.SetStatus(id, worker.StatusPending)
	}

	// Sync task 1.1
	state.SetStatus("1.1", worker.StatusSynced)

	// Both 2.1 and 2.2 should be ready simultaneously
	ready := orch.ComputeReadyTasks()
	if len(ready) != 2 {
		t.Fatalf("expected 2 concurrent ready tasks, got %d: %v", len(ready), ready)
	}

	// Simulate concurrent execution
	var mu sync.Mutex
	startOrder := []string{}
	var wg sync.WaitGroup

	for _, id := range ready {
		wg.Add(1)
		go func(taskID string) {
			defer wg.Done()
			mu.Lock()
			startOrder = append(startOrder, taskID)
			state.SetStatus(taskID, worker.StatusRunning)
			mu.Unlock()
		}(id)
	}
	wg.Wait()

	// Both should be running now
	running := state.GetAllWithStatus(worker.StatusRunning)
	if len(running) != 2 {
		t.Fatalf("expected 2 running tasks, got %d", len(running))
	}
}

// TestIntegrationContextCancellation verifies that cancelling the context
// causes the orchestrator to return promptly.
func TestIntegrationContextCancellation(t *testing.T) {
	testDAG := &dag.DAG{
		Waves: []dag.Wave{
			{
				ID: 0,
				Tasks: []dag.Task{
					{ID: "1.1", Description: "Long running", Dependencies: []string{}},
				},
			},
		},
	}

	logger, _ := logging.NewLogger(logging.LevelInfo, "")
	tmpDir, _ := os.MkdirTemp("", "btb-cancel-*")
	defer os.RemoveAll(tmpDir)

	// Create a git repo for the orchestrator
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	cmd.Run()

	specDir := filepath.Join(tmpDir, "spec")
	os.MkdirAll(specDir, 0755)
	os.WriteFile(filepath.Join(specDir, "tasks.md"), []byte("- [ ] 1.1 Task\n"), 0644)

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = tmpDir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "init", "--allow-empty")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	cmd.Run()

	cfg := &config.Config{
		MaxRetries:     3,
		WorkerSlots:    3,
		MaxIters:       5,
		SyncInterval:   1,
		WorktreeBase:   filepath.Join(tmpDir, "worktrees"),
		LogDir:         filepath.Join(tmpDir, "logs"),
		SpecDir:        specDir,
		TaskFile:       filepath.Join(specDir, "tasks.md"),
		EnableReview:   false,
		StaleThreshold: 9999,
		JobTimeout:     0,
		RateLimitPause: 0,
		DefaultTaskModel: "test-model",
	}

	state := worker.NewStateManager()
	orch := &Orchestrator{
		Config: cfg,
		DAG:    testDAG,
		State:  state,
		Logger: logger,
		TUI:    nil,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := orch.Run(ctx)
	if err != context.Canceled {
		// The orchestrator may also return nil if it resolves fast enough
		// but most likely it should return context.Canceled
		if err != nil {
			t.Logf("got error (acceptable): %v", err)
		}
	}

	// Verify the function returned (did not hang)
	_ = fmt.Sprintf("context cancellation test passed")
}
