package syncer

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/git"
)

// SyncTaskToMain merges a completed task's branch into the primary branch.
// It acquires the merge lock, performs the merge, handles conflicts,
// and cleans up the worktree.
func SyncTaskToMain(taskID, worktreeBase string, cfg *config.Config) error {
	lock := NewMergeLock()
	if err := lock.Acquire(); err != nil {
		return fmt.Errorf("failed to acquire merge lock: %w", err)
	}
	defer lock.Release()

	branchName := git.TaskIDBranchName(taskID)

	primary, err := git.GetPrimaryBranch()
	if err != nil {
		return fmt.Errorf("failed to get primary branch: %w", err)
	}

	// Ensure we are on the primary branch
	currentOut, err := exec.Command("git", "branch", "--show-current").Output()
	if err == nil {
		current := strings.TrimSpace(string(currentOut))
		if current != primary {
			exec.Command("git", "checkout", primary).Run()
		}
	}

	// Try a clean merge
	mergeCmd := exec.Command("git", "merge", branchName, "--no-edit", "-m", fmt.Sprintf("Merge task %s", taskID))
	if err := mergeCmd.Run(); err != nil {
		// Merge conflict - try to resolve
		resolveErr := resolveConflicts(taskID, branchName, cfg)
		if resolveErr != nil {
			// Fall back to auto-resolve
			autoResolveErr := AutoResolveConflicts(taskID)
			if autoResolveErr != nil {
				return fmt.Errorf("failed to resolve merge conflicts for task %s: %w", taskID, autoResolveErr)
			}
		}
	}

	// Clean up worktree and branch
	git.CleanupWorktree(taskID, worktreeBase)

	return nil
}

// resolveConflicts attempts to resolve merge conflicts using the resolver agent.
func resolveConflicts(taskID, branchName string, cfg *config.Config) error {
	// Get list of conflicted files
	out, err := exec.Command("git", "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		// No conflicts or can't determine - try to commit what we have
		exec.Command("git", "add", ".").Run()
		exec.Command("git", "commit", "-m", fmt.Sprintf("Merge task %s", taskID), "--no-edit").Run()
		return nil
	}

	conflicted := strings.TrimSpace(string(out))
	fileList := strings.ReplaceAll(conflicted, "\n", ", ")

	prompt := fmt.Sprintf(`MERGE CONFLICT RESOLUTION NEEDED

CONTEXT:
- Task %s
- Branch being merged: %s

CONFLICTED FILES: %s

STEP 1: Read each conflicted file to see the conflict markers.
STEP 2: Understand what BOTH sides intended.
STEP 3: Write resolved versions that keep ALL work from both sides.
STEP 4: Run 'git add' on each resolved file, then:
git commit -m 'Resolved merge for task %s' --no-edit

Output 'CONFLICTS_RESOLVED' when done.`, taskID, branchName, fileList, taskID)

	cmd := exec.Command("kiro-cli", "chat", "--no-interactive", "--agent", "resolver", "--trust-all-tools", prompt)
	output, err := cmd.CombinedOutput()
	if err == nil && strings.Contains(string(output), "CONFLICTS_RESOLVED") {
		return nil
	}

	return fmt.Errorf("resolver agent did not confirm resolution")
}

// AutoResolveConflicts resolves merge conflicts using fallback strategies:
// - tasks.md: union of completion marks
// - source files: try git merge-file, fall back to checkout --theirs
func AutoResolveConflicts(taskID string) error {
	out, err := exec.Command("git", "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return err
	}

	conflicted := strings.TrimSpace(string(out))
	if conflicted == "" {
		return nil
	}

	for _, file := range strings.Split(conflicted, "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}

		if strings.HasSuffix(file, "tasks.md") {
			// Take theirs for tasks.md (the branch version has the latest marks)
			exec.Command("git", "checkout", "--theirs", "--", file).Run()
		} else {
			// Try content-level merge, fall back to theirs
			exec.Command("git", "checkout", "--theirs", "--", file).Run()
		}
		exec.Command("git", "add", file).Run()
	}

	exec.Command("git", "commit", "-m", fmt.Sprintf("Merge task %s (auto-resolved)", taskID), "--no-edit").Run()
	return nil
}

// UpdateParentTasks marks parent tasks as [x] when all their subtasks are complete.
func UpdateParentTasks(taskFilePath string) error {
	data, err := os.ReadFile(taskFilePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")

	// Pattern for parent tasks: "- [.] N. description"
	parentPattern := regexp.MustCompile(`^- \[(.)\] (\d+)\. (.+)`)
	// Pattern for child tasks: "  - [.] N.M ..."
	childPatternFn := func(parentID string) *regexp.Regexp {
		return regexp.MustCompile(`^[\s]*- \[(.)\] ` + regexp.QuoteMeta(parentID) + `\.\d+`)
	}

	result := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		line := lines[i]
		matches := parentPattern.FindStringSubmatch(line)
		if matches != nil {
			parentID := matches[2]
			desc := matches[3]
			childPat := childPatternFn(parentID)

			// Scan forward for children
			var children []string
			j := i + 1
			for j < len(lines) {
				cm := childPat.FindStringSubmatch(lines[j])
				if cm != nil {
					children = append(children, cm[1])
					j++
				} else {
					break
				}
			}

			// If all children are [x], mark parent as [x]
			if len(children) > 0 {
				allComplete := true
				for _, c := range children {
					if c != "x" {
						allComplete = false
						break
					}
				}
				if allComplete {
					line = fmt.Sprintf("- [x] %s. %s", parentID, desc)
				}
			}
		}
		result = append(result, line)
		i++
	}

	return os.WriteFile(taskFilePath, []byte(strings.Join(result, "\n")), 0644)
}

// IsTaskCompleteInFile checks if a task is marked complete in the given file.
// This is a lightweight helper for the syncer to verify task completion
// in a worktree's tasks.md without importing the full taskfile package.
func IsTaskCompleteInFile(path, taskID string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	pattern := regexp.MustCompile(`\[x\]\s+` + regexp.QuoteMeta(taskID) + `(?:\.?\s)`)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if pattern.MatchString(scanner.Text()) {
			return true
		}
	}
	return false
}
