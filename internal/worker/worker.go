package worker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/taskfile"
)

// Worker represents a single task execution unit that runs inside a worktree.
type Worker struct {
	TaskID       string
	TaskDesc     string
	TaskModel    string
	SpecDir      string
	TaskFile     string
	WorktreePath string
	LogFile      string
	MaxIters     int
	Config       *config.Config
	FailContext  string // failure context from previous attempt
}

// WorkerResult holds the outcome of a worker execution.
type WorkerResult struct {
	TaskID  string
	Success bool
	Error   error
}

// Run executes the worker loop: for each iteration up to MaxIters, invoke
// kiro-cli with the player agent and check for task completion.
func (w *Worker) Run(ctx context.Context) WorkerResult {
	logFile, err := os.OpenFile(w.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return WorkerResult{TaskID: w.TaskID, Success: false, Error: fmt.Errorf("failed to open log file: %w", err)}
	}
	defer logFile.Close()

	w.logEntry(logFile, "STARTED task=%s", w.TaskID)

	for i := 1; i <= w.MaxIters; i++ {
		select {
		case <-ctx.Done():
			w.logEntry(logFile, "CANCELLED task=%s iter=%d", w.TaskID, i)
			return WorkerResult{TaskID: w.TaskID, Success: false, Error: ctx.Err()}
		default:
		}

		w.logEntry(logFile, "ITERATION i=%d task=%s", i, w.TaskID)

		// Check if task is already complete
		taskFilePath := filepath.Join(w.WorktreePath, w.TaskFile)
		complete, _ := taskfile.IsTaskComplete(taskFilePath, w.TaskID)
		if complete {
			w.logEntry(logFile, "ALREADY_COMPLETE task=%s", w.TaskID)
			w.commitWork()
			return WorkerResult{TaskID: w.TaskID, Success: true}
		}

		// Build prompt
		prompt := w.buildPrompt()

		// Run kiro-cli
		completed, runErr := w.runKiroCLI(ctx, prompt, logFile)
		if runErr != nil {
			if ctx.Err() != nil {
				return WorkerResult{TaskID: w.TaskID, Success: false, Error: ctx.Err()}
			}
			w.logEntry(logFile, "KIRO_CLI_ERROR task=%s iter=%d err=%v", w.TaskID, i, runErr)
		}

		if completed {
			w.logEntry(logFile, "COMPLETED task=%s iterations=%d", w.TaskID, i)
			w.commitWork()
			return WorkerResult{TaskID: w.TaskID, Success: true}
		}

		// Also check tasks.md in the worktree
		complete, _ = taskfile.IsTaskComplete(taskFilePath, w.TaskID)
		if complete {
			w.logEntry(logFile, "COMPLETED_VIA_FILE task=%s iterations=%d", w.TaskID, i)
			w.commitWork()
			return WorkerResult{TaskID: w.TaskID, Success: true}
		}

		// Commit progress even if not complete
		w.commitProgress(i)

		// Rate limit pause
		time.Sleep(time.Duration(w.Config.RateLimitPause) * time.Second)
	}

	w.logEntry(logFile, "FAILED task=%s reason=max_iterations", w.TaskID)
	return WorkerResult{TaskID: w.TaskID, Success: false, Error: fmt.Errorf("exhausted %d iterations", w.MaxIters)}
}

func (w *Worker) buildPrompt() string {
	var sb strings.Builder

	sb.WriteString("You are working on a specific task from a spec.\n\n")

	// Steering context
	steeringDir := filepath.Join(w.WorktreePath, ".kiro", "steering")
	if info, err := os.Stat(steeringDir); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(steeringDir)
		if len(entries) > 0 {
			sb.WriteString("You have project context in .kiro/steering/ -- read those files first for architecture, conventions, and spec summary.\n\n")
		}
	}

	// Retry context from previous failure
	if w.FailContext != "" {
		sb.WriteString("IMPORTANT -- PREVIOUS ATTEMPT FAILED:\n")
		sb.WriteString("This is a RETRY. The previous attempt at this task failed. Here is what happened:\n")
		sb.WriteString(w.FailContext)
		sb.WriteString("\n\nLEARN FROM THIS FAILURE:\n")
		sb.WriteString("- Do NOT repeat the same sequence of actions that led to the failure.\n\n")
	}

	sb.WriteString(fmt.Sprintf("READ these spec files for task details:\n"))
	sb.WriteString(fmt.Sprintf("1. %s - the full task list\n", w.TaskFile))
	sb.WriteString(fmt.Sprintf("2. %s/design.md - architecture and implementation guidance (if it exists)\n", w.SpecDir))
	sb.WriteString(fmt.Sprintf("3. %s/requirements.md - acceptance criteria (if it exists)\n\n", w.SpecDir))

	sb.WriteString(fmt.Sprintf("YOUR TASK: Implement ONLY task %s: %s\n\n", w.TaskID, w.TaskDesc))

	sb.WriteString("RULES:\n")
	sb.WriteString(fmt.Sprintf("- Focus ONLY on task %s. Do NOT work on other tasks.\n", w.TaskID))
	sb.WriteString("- Implement it fully with no placeholders or TODOs.\n")
	sb.WriteString("- If this task involves writing code, write complete working code.\n")
	sb.WriteString("- If this task involves tests, write and RUN the tests using NON-INTERACTIVE commands.\n")
	sb.WriteString("- After implementation, verify your work compiles/runs correctly.\n")
	sb.WriteString(fmt.Sprintf("- Update %s to mark task %s as complete: change '- [ ] %s' to '- [x] %s'\n", w.TaskFile, w.TaskID, w.TaskID, w.TaskID))
	sb.WriteString(fmt.Sprintf("- Output '%s::%s' when the task is done and verified.\n", w.Config.TaskCompletePrefix, w.TaskID))

	return sb.String()
}

func (w *Worker) runKiroCLI(ctx context.Context, prompt string, logFile *os.File) (bool, error) {
	args := []string{"chat", "--no-interactive", "--agent", "player", "--trust-all-tools"}
	if w.TaskModel != "" {
		args = append(args, "--model", w.TaskModel)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, "kiro-cli", args...)
	cmd.Dir = w.WorktreePath
	cmd.Env = append(os.Environ(), "CI=true")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("failed to start kiro-cli: %w", err)
	}

	// Start heartbeat goroutine that monitors the process.
	// Use a separate stopCh (closed by the caller) to signal the goroutine to exit.
	// The goroutine must not close stopCh itself to avoid send-on-closed-channel panics.
	stopHeartbeat := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Touch heartbeat file
				hbFile := filepath.Join(filepath.Dir(w.LogFile), fmt.Sprintf("%s.heartbeat", w.TaskID))
				os.WriteFile(hbFile, []byte(fmt.Sprintf("%d", time.Now().Unix())), 0644)
			case <-ctx.Done():
				return
			case <-stopHeartbeat:
				return
			}
		}
	}()

	// Read output, tee to log file, and scan for completion signal
	completionSignal := fmt.Sprintf("%s::%s", w.Config.TaskCompletePrefix, w.TaskID)
	completed := false

	reader := bufio.NewReader(stdout)
	teeReader := io.TeeReader(reader, logFile)
	scanner := bufio.NewScanner(teeReader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, completionSignal) {
			completed = true
		}
	}

	err = cmd.Wait()

	// Signal heartbeat goroutine to stop
	close(stopHeartbeat)

	if err != nil && ctx.Err() == nil {
		return completed, fmt.Errorf("kiro-cli exited with error: %w", err)
	}

	return completed, nil
}

func (w *Worker) commitWork() {
	cmd := exec.Command("git", "add", "--all")
	cmd.Dir = w.WorktreePath
	cmd.Run()

	commitMsg := fmt.Sprintf("Completed task %s: %s", w.TaskID, w.TaskDesc)
	cmd = exec.Command("git", "commit", "-m", commitMsg, "--allow-empty")
	cmd.Dir = w.WorktreePath
	cmd.Run()
}

func (w *Worker) commitProgress(iter int) {
	cmd := exec.Command("git", "add", "--all")
	cmd.Dir = w.WorktreePath
	cmd.Run()

	commitMsg := fmt.Sprintf("Task %s iteration %d: in progress", w.TaskID, iter)
	cmd = exec.Command("git", "commit", "-m", commitMsg, "--allow-empty")
	cmd.Dir = w.WorktreePath
	cmd.Run()
}

func (w *Worker) logEntry(f *os.File, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02T15:04:05")
	fmt.Fprintf(f, "%s %s\n", timestamp, msg)
}
