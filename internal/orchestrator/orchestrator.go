package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/dag"
	"github.com/fanhongy/Bob-The-Builder/internal/git"
	"github.com/fanhongy/Bob-The-Builder/internal/logging"
	"github.com/fanhongy/Bob-The-Builder/internal/reviewer"
	"github.com/fanhongy/Bob-The-Builder/internal/syncer"
	"github.com/fanhongy/Bob-The-Builder/internal/taskfile"
	"github.com/fanhongy/Bob-The-Builder/internal/worker"
)

// TUIInterface allows the orchestrator to send updates without importing the tui package.
type TUIInterface interface {
	SetTaskState(id string, state string)
	AddEvent(msg string)
	SetPhase(phase string)
	SetProgress(completed, total int, elapsed time.Duration)
	SetCurrentWave(wave int)
}

// Orchestrator manages the execution of tasks based on their DAG dependencies.
type Orchestrator struct {
	Config *config.Config
	DAG    *dag.DAG
	State  *worker.StateManager
	Logger *logging.Logger
	TUI    TUIInterface // nil if --no-tui
}

// Run implements the dependency-ready scheduler.
func (o *Orchestrator) Run(ctx context.Context) error {
	allTaskIDs := o.DAG.AllTaskIDs()
	totalTasks := len(allTaskIDs)

	if totalTasks == 0 {
		o.Logger.Info("no tasks to execute")
		return nil
	}

	// Initialize all tasks as pending
	for _, id := range allTaskIDs {
		o.State.SetStatus(id, worker.StatusPending)
	}

	// Mark already-completed tasks as synced
	for _, id := range allTaskIDs {
		complete, _ := taskfile.IsTaskComplete(o.Config.TaskFile, id)
		if complete {
			o.State.SetStatus(id, worker.StatusSynced)
			o.tuiSetTaskState(id, "completed")
		}
	}

	// Create working directories
	os.MkdirAll(o.Config.LogDir, 0755)
	os.MkdirAll(o.Config.WorktreeBase, 0755)

	// Channels for worker results
	resultCh := make(chan worker.WorkerResult, totalTasks)

	// Track review batching
	var reviewBatchTasks []string
	var reviewBatchBaseSHA string
	syncedSinceReview := 0

	totalCompleted := o.State.CountWithStatus(worker.StatusSynced)
	totalFailed := 0
	totalSkipped := 0
	startTime := time.Now()

	o.tuiAddEvent(fmt.Sprintf("scheduler started - %d tasks, %d worker slots", totalTasks, o.Config.WorkerSlots))
	o.tuiSetPhase("executing")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 1. Collect results from completed workers (non-blocking)
		o.collectResults(resultCh, &totalCompleted, &totalFailed, &syncedSinceReview, &reviewBatchTasks, &reviewBatchBaseSHA)

		// 2. Skip tasks whose dependencies failed
		for _, id := range allTaskIDs {
			if o.State.GetStatus(id) != worker.StatusPending {
				continue
			}
			_, err := o.AreDependenciesSatisfied(id)
			if err != nil {
				// Dependency failed - skip this task
				o.State.SetStatus(id, worker.StatusSkipped)
				totalSkipped++
				o.tuiSetTaskState(id, "skipped")
				o.tuiAddEvent(fmt.Sprintf("task %s skipped (dependency failed)", id))
				o.Logger.Info("task %s skipped: dependency failed", id)
			}
		}

		// 3. Check stale/timed-out workers
		o.checkStaleWorkers()
		o.checkJobTimeouts()

		// 4. Compute ready tasks and spawn workers
		readyTasks := o.ComputeReadyTasks()
		runningCount := o.State.CountWithStatus(worker.StatusRunning)

		for _, id := range readyTasks {
			if runningCount >= o.Config.WorkerSlots {
				break
			}
			if err := o.spawnWorker(ctx, id, resultCh); err != nil {
				o.Logger.Warn("failed to spawn worker for task %s: %v", id, err)
				continue
			}
			runningCount++
		}

		// 5. Trigger review if batch is full
		if syncedSinceReview >= o.Config.ReviewBatchSize && o.Config.EnableReview {
			runningNow := o.State.CountWithStatus(worker.StatusRunning)
			if runningNow == 0 {
				o.tuiSetPhase("reviewing")
				reviewer.ReviewWave("batch", reviewBatchTasks, reviewBatchBaseSHA, o.Config, o.Logger)
				syncedSinceReview = 0
				reviewBatchTasks = nil
				reviewBatchBaseSHA = ""
				o.tuiSetPhase("executing")
			}
		}

		// 6. Update progress
		o.tuiSetProgress(totalCompleted, totalTasks, time.Since(startTime))

		// 7. Check if all tasks are resolved
		if o.allTasksResolved(allTaskIDs) {
			break
		}

		// Sleep between iterations
		time.Sleep(time.Duration(o.Config.SyncInterval) * time.Second)
	}

	// Final review for any remaining batch
	if len(reviewBatchTasks) > 0 && o.Config.EnableReview {
		reviewer.ReviewWave("final", reviewBatchTasks, reviewBatchBaseSHA, o.Config, o.Logger)
	}

	o.Logger.Info("execution complete: %d completed, %d failed, %d skipped", totalCompleted, totalFailed, totalSkipped)
	return nil
}

// AreDependenciesSatisfied checks if all dependencies of a task are in synced state.
// Returns (true, nil) if all deps are synced.
// Returns (false, nil) if some deps are not yet synced but none failed.
// Returns (false, error) if any dep has failed or been skipped.
func (o *Orchestrator) AreDependenciesSatisfied(taskID string) (bool, error) {
	deps := o.DAG.GetTaskDependencies(taskID)
	if len(deps) == 0 {
		return true, nil
	}

	for _, dep := range deps {
		status := o.State.GetStatus(dep)
		switch status {
		case worker.StatusSynced:
			// Good - dependency satisfied
		case worker.StatusFailed, worker.StatusSkipped:
			return false, fmt.Errorf("dependency %s is %s", dep, status.String())
		default:
			// Not yet synced
			return false, nil
		}
	}
	return true, nil
}

// ComputeReadyTasks returns task IDs that are pending and have all dependencies satisfied.
func (o *Orchestrator) ComputeReadyTasks() []string {
	var ready []string
	allTaskIDs := o.DAG.AllTaskIDs()

	for _, id := range allTaskIDs {
		if o.State.GetStatus(id) != worker.StatusPending {
			continue
		}
		satisfied, err := o.AreDependenciesSatisfied(id)
		if err != nil {
			// Will be handled in the skip logic
			continue
		}
		if satisfied {
			ready = append(ready, id)
		}
	}
	return ready
}

func (o *Orchestrator) collectResults(resultCh chan worker.WorkerResult, totalCompleted, totalFailed, syncedSinceReview *int, reviewBatchTasks *[]string, reviewBatchBaseSHA *string) {
	for {
		select {
		case result := <-resultCh:
			if result.Success {
				// Sync to main
				o.tuiAddEvent(fmt.Sprintf("syncing task %s to main", result.TaskID))
				err := syncer.SyncTaskToMain(result.TaskID, o.Config.WorktreeBase, o.Config)
				if err != nil {
					o.Logger.Error("failed to sync task %s: %v", result.TaskID, err)
					o.State.SetStatus(result.TaskID, worker.StatusFailed)
					(*totalFailed)++
					o.tuiSetTaskState(result.TaskID, "failed")
					continue
				}

				o.State.SetStatus(result.TaskID, worker.StatusSynced)
				(*totalCompleted)++
				(*syncedSinceReview)++
				*reviewBatchTasks = append(*reviewBatchTasks, result.TaskID)

				if *reviewBatchBaseSHA == "" {
					out, err := exec.Command("git", "rev-parse", "HEAD~1").Output()
					if err == nil {
						*reviewBatchBaseSHA = strings.TrimSpace(string(out))
					}
				}

				// Update parent tasks
				syncer.UpdateParentTasks(o.Config.TaskFile)

				o.tuiSetTaskState(result.TaskID, "synced")
				o.tuiAddEvent(fmt.Sprintf("task %s synced", result.TaskID))
				o.Logger.Info("task %s synced to main", result.TaskID)
			} else {
				// Handle failure with retries
				retries := o.State.GetRetries(result.TaskID)
				if retries < o.Config.MaxRetries {
					o.State.IncrRetries(result.TaskID)
					o.State.SetStatus(result.TaskID, worker.StatusPending)
					o.tuiAddEvent(fmt.Sprintf("task %s failed (attempt %d/%d), will retry", result.TaskID, retries+1, o.Config.MaxRetries))
					o.Logger.Warn("task %s failed (attempt %d/%d): %v", result.TaskID, retries+1, o.Config.MaxRetries, result.Error)
					// Clean up worktree for retry
					git.CleanupWorktree(result.TaskID, o.Config.WorktreeBase)
				} else {
					o.State.SetStatus(result.TaskID, worker.StatusFailed)
					(*totalFailed)++
					o.tuiSetTaskState(result.TaskID, "failed")
					o.tuiAddEvent(fmt.Sprintf("task %s FAILED after %d retries", result.TaskID, o.Config.MaxRetries))
					o.Logger.Error("task %s permanently failed after %d retries", result.TaskID, o.Config.MaxRetries)
					git.CleanupWorktree(result.TaskID, o.Config.WorktreeBase)
				}
			}
		default:
			return
		}
	}
}

func (o *Orchestrator) spawnWorker(ctx context.Context, taskID string, resultCh chan worker.WorkerResult) error {
	task := o.DAG.GetTask(taskID)
	if task == nil {
		return fmt.Errorf("task %s not found in DAG", taskID)
	}

	// Get task description
	taskDesc, _ := taskfile.GetTaskDescription(o.Config.TaskFile, taskID)
	if taskDesc == "" {
		taskDesc = task.Description
	}

	// Get model
	taskModel := task.Model
	if taskModel == "" {
		taskModel = o.Config.DefaultTaskModel
	}

	// Create worktree
	worktreePath, err := git.CreateWorktree(taskID, o.Config.WorktreeBase)
	if err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	// Set up state
	o.State.SetStatus(taskID, worker.StatusRunning)
	o.State.SetWorktreePath(taskID, worktreePath)
	o.State.SetStartedAt(taskID, time.Now())
	o.State.UpdateHeartbeat(taskID)

	logFile := filepath.Join(o.Config.LogDir, fmt.Sprintf("task_%s.log", strings.ReplaceAll(taskID, ".", "_")))
	o.State.SetLogFile(taskID, logFile)

	o.tuiSetTaskState(taskID, "running")
	o.tuiAddEvent(fmt.Sprintf("worker spawned for task %s [%s]", taskID, taskModel))
	o.Logger.Task("spawning worker for %s: %s", taskID, taskDesc)

	// Launch worker in goroutine
	w := &worker.Worker{
		TaskID:       taskID,
		TaskDesc:     taskDesc,
		TaskModel:    taskModel,
		SpecDir:      o.Config.SpecDir,
		TaskFile:     o.Config.TaskFile,
		WorktreePath: worktreePath,
		LogFile:      logFile,
		MaxIters:     o.Config.MaxIters,
		Config:       o.Config,
	}

	go func() {
		result := w.Run(ctx)
		resultCh <- result
	}()

	return nil
}

func (o *Orchestrator) checkStaleWorkers() {
	running := o.State.GetAllWithStatus(worker.StatusRunning)
	now := time.Now()

	for _, id := range running {
		heartbeat := o.State.GetHeartbeat(id)
		if heartbeat.IsZero() {
			heartbeat = o.State.GetStartedAt(id)
		}

		idleTime := now.Sub(heartbeat)
		threshold := time.Duration(o.Config.StaleThreshold) * time.Second

		if idleTime > threshold {
			pid := o.State.GetPID(id)
			if pid > 0 {
				// Kill the stale process
				proc, err := os.FindProcess(pid)
				if err == nil {
					proc.Signal(os.Kill)
				}
			}
			o.Logger.Warn("task %s stale (idle %v), killed", id, idleTime)
			o.tuiAddEvent(fmt.Sprintf("task %s stale (idle %v), killed", id, idleTime))
		}
	}
}

func (o *Orchestrator) checkJobTimeouts() {
	if o.Config.JobTimeout <= 0 {
		return
	}

	running := o.State.GetAllWithStatus(worker.StatusRunning)
	now := time.Now()
	timeout := time.Duration(o.Config.JobTimeout) * time.Second

	for _, id := range running {
		startedAt := o.State.GetStartedAt(id)
		if startedAt.IsZero() {
			continue
		}

		elapsed := now.Sub(startedAt)
		if elapsed > timeout {
			pid := o.State.GetPID(id)
			if pid > 0 {
				proc, err := os.FindProcess(pid)
				if err == nil {
					proc.Signal(os.Kill)
				}
			}
			o.Logger.Warn("task %s hit job timeout (%v > %v), killed", id, elapsed, timeout)
			o.tuiAddEvent(fmt.Sprintf("task %s hit job timeout, killed", id))
		}
	}
}

func (o *Orchestrator) allTasksResolved(allTaskIDs []string) bool {
	for _, id := range allTaskIDs {
		status := o.State.GetStatus(id)
		switch status {
		case worker.StatusSynced, worker.StatusFailed, worker.StatusSkipped:
			// Resolved
		default:
			return false
		}
	}
	return true
}

// TUI helper methods - no-op if TUI is nil
func (o *Orchestrator) tuiSetTaskState(id, state string) {
	if o.TUI != nil {
		o.TUI.SetTaskState(id, state)
	}
}

func (o *Orchestrator) tuiAddEvent(msg string) {
	if o.TUI != nil {
		o.TUI.AddEvent(msg)
	}
}

func (o *Orchestrator) tuiSetPhase(phase string) {
	if o.TUI != nil {
		o.TUI.SetPhase(phase)
	}
}

func (o *Orchestrator) tuiSetProgress(completed, total int, elapsed time.Duration) {
	if o.TUI != nil {
		o.TUI.SetProgress(completed, total, elapsed)
	}
}
