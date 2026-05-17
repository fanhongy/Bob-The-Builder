package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	primaryBranch     string
	primaryBranchOnce sync.Once
)

// GetPrimaryBranch detects the primary branch name (main/master/current).
// The result is cached after the first call.
func GetPrimaryBranch() (string, error) {
	var retErr error
	primaryBranchOnce.Do(func() {
		primaryBranch, retErr = detectPrimaryBranch()
	})
	if retErr != nil {
		return "", retErr
	}
	return primaryBranch, nil
}

// ResetPrimaryBranchCache clears the cached primary branch (for testing).
func ResetPrimaryBranchCache() {
	primaryBranchOnce = sync.Once{}
	primaryBranch = ""
}

func detectPrimaryBranch() (string, error) {
	// 1. If we have a HEAD, use the current branch
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err == nil {
		current := strings.TrimSpace(string(out))
		if current != "" {
			// If on a ralph/* branch, find the non-ralph branch
			if strings.HasPrefix(current, "ralph/") {
				for _, candidate := range []string{"main", "master"} {
					if err := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+candidate).Run(); err == nil {
						return candidate, nil
					}
				}
				// Fall back to first non-ralph branch
				branchOut, err := exec.Command("git", "branch").Output()
				if err == nil {
					for _, line := range strings.Split(string(branchOut), "\n") {
						br := strings.TrimLeft(line, "* ")
						br = strings.TrimSpace(br)
						if br != "" && !strings.HasPrefix(br, "ralph/") {
							return br, nil
						}
					}
				}
				return "main", nil
			}
			return current, nil
		}
	}

	// 2. No current branch (detached HEAD or bare init) -- check refs
	for _, candidate := range []string{"main", "master"} {
		if err := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+candidate).Run(); err == nil {
			return candidate, nil
		}
	}

	// 3. Default to main
	return "main", nil
}

// EnsureGitReady verifies .git exists and has at least one commit.
// If not, initializes a repo and creates an initial commit.
func EnsureGitReady() error {
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		if err := exec.Command("git", "init", "-b", "main").Run(); err != nil {
			return fmt.Errorf("git init failed: %w", err)
		}
		if err := exec.Command("git", "add", ".").Run(); err != nil {
			return fmt.Errorf("git add failed: %w", err)
		}
		cmd := exec.Command("git", "commit", "-m", "Initial commit", "--allow-empty")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git commit failed: %w", err)
		}
	} else {
		// Ensure there is at least one commit
		if err := exec.Command("git", "rev-parse", "HEAD").Run(); err != nil {
			if err := exec.Command("git", "add", ".").Run(); err != nil {
				return fmt.Errorf("git add failed: %w", err)
			}
			cmd := exec.Command("git", "commit", "-m", "Initial commit", "--allow-empty")
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("git commit failed: %w", err)
			}
		}
	}
	return nil
}

// TaskIDBranchName converts a task ID to its branch name.
// Dots are replaced with hyphens: "1.1" -> "ralph/task-1-1"
func TaskIDBranchName(taskID string) string {
	return "ralph/task-" + strings.ReplaceAll(taskID, ".", "-")
}

// TaskIDDirName converts a task ID to the worktree directory name.
func TaskIDDirName(taskID string) string {
	return "task-" + strings.ReplaceAll(taskID, ".", "-")
}

// CreateWorktree creates a git worktree for the given task ID.
// It retries up to 5 times with backoff on index lock errors,
// and prunes stale worktrees before each attempt.
func CreateWorktree(taskID, worktreeBase string) (string, error) {
	branchName := TaskIDBranchName(taskID)
	dirName := TaskIDDirName(taskID)

	// Resolve worktree_base to absolute path, creating it if needed
	if err := os.MkdirAll(worktreeBase, 0o755); err != nil {
		return "", fmt.Errorf("failed to create worktree base: %w", err)
	}
	absBase, err := filepath.Abs(worktreeBase)
	if err != nil {
		return "", fmt.Errorf("failed to resolve worktree base: %w", err)
	}
	worktreePath := filepath.Join(absBase, dirName)

	// Clean up any stale worktree at this path
	if _, err := os.Stat(worktreePath); err == nil {
		exec.Command("git", "worktree", "remove", worktreePath, "--force").Run()
	}
	exec.Command("git", "branch", "-D", branchName).Run()
	exec.Command("git", "worktree", "prune").Run()

	// Retry loop
	maxAttempts := 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName)
		out, err := cmd.CombinedOutput()
		if err == nil {
			// Verify the worktree actually exists
			if _, statErr := os.Stat(worktreePath); statErr == nil {
				return worktreePath, nil
			}
		}
		lastErr = fmt.Errorf("attempt %d: %s: %s", attempt, err, string(out))

		if attempt < maxAttempts {
			// Backoff: sleep for attempt seconds (linear)
			time.Sleep(time.Duration(attempt) * time.Second)
			// Clean up partial state
			exec.Command("git", "worktree", "remove", worktreePath, "--force").Run()
			exec.Command("git", "branch", "-D", branchName).Run()
			exec.Command("git", "worktree", "prune").Run()
		}
	}

	return "", fmt.Errorf("failed to create worktree after %d attempts: %w", maxAttempts, lastErr)
}

// CleanupWorktree removes a worktree and its branch for the given task ID.
func CleanupWorktree(taskID, worktreeBase string) error {
	branchName := TaskIDBranchName(taskID)
	dirName := TaskIDDirName(taskID)

	var worktreePath string
	if info, err := os.Stat(worktreeBase); err == nil && info.IsDir() {
		absBase, err := filepath.Abs(worktreeBase)
		if err == nil {
			worktreePath = filepath.Join(absBase, dirName)
		} else {
			worktreePath = filepath.Join(worktreeBase, dirName)
		}
	} else {
		worktreePath = filepath.Join(worktreeBase, dirName)
	}

	exec.Command("git", "worktree", "remove", worktreePath, "--force").Run()
	exec.Command("git", "branch", "-D", branchName).Run()
	return nil
}

// CleanupAll removes all ralph worktrees and branches.
func CleanupAll(worktreeBase string) error {
	// Remove all ralph worktrees
	out, err := exec.Command("git", "worktree", "list").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "ralph") {
				parts := strings.Fields(line)
				if len(parts) > 0 {
					exec.Command("git", "worktree", "remove", parts[0], "--force").Run()
				}
			}
		}
	}
	exec.Command("git", "worktree", "prune").Run()

	// Remove all ralph branches
	branchOut, err := exec.Command("git", "branch").Output()
	if err == nil {
		for _, line := range strings.Split(string(branchOut), "\n") {
			br := strings.TrimLeft(line, "* ")
			br = strings.TrimSpace(br)
			if strings.HasPrefix(br, "ralph/") {
				exec.Command("git", "branch", "-D", br).Run()
			}
		}
	}

	// Remove the worktree base directory if it exists
	if worktreeBase != "" {
		os.RemoveAll(worktreeBase)
	}

	return nil
}
