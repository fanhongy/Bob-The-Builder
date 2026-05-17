package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBranchName(t *testing.T) {
	tests := []struct {
		taskID string
		want   string
	}{
		{"1.1", "ralph/task-1-1"},
		{"2.3", "ralph/task-2-3"},
		{"10.12", "ralph/task-10-12"},
		{"3", "ralph/task-3"},
	}

	for _, tc := range tests {
		got := TaskIDBranchName(tc.taskID)
		if got != tc.want {
			t.Errorf("TaskIDBranchName(%q) = %q, want %q", tc.taskID, got, tc.want)
		}
	}
}

func TestDirName(t *testing.T) {
	tests := []struct {
		taskID string
		want   string
	}{
		{"1.1", "task-1-1"},
		{"2.3", "task-2-3"},
	}

	for _, tc := range tests {
		got := TaskIDDirName(tc.taskID)
		if got != tc.want {
			t.Errorf("TaskIDDirName(%q) = %q, want %q", tc.taskID, got, tc.want)
		}
	}
}

func TestGetPrimaryBranch(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir := t.TempDir()

	// Save current dir and change to temp
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)
	os.Chdir(tmpDir)

	// Reset the cache before test
	ResetPrimaryBranchCache()

	// Initialize a git repo with "main" branch
	run(t, "git", "init", "-b", "main")
	run(t, "git", "config", "user.email", "test@test.com")
	run(t, "git", "config", "user.name", "Test")
	run(t, "git", "commit", "--allow-empty", "-m", "initial")

	branch, err := GetPrimaryBranch()
	if err != nil {
		t.Fatalf("GetPrimaryBranch() error: %v", err)
	}
	if branch != "main" {
		t.Errorf("GetPrimaryBranch() = %q, want \"main\"", branch)
	}
}

func TestCreateAndCleanupWorktree(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir := t.TempDir()

	// Save current dir and change to temp
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)
	os.Chdir(tmpDir)

	// Reset the cache before test
	ResetPrimaryBranchCache()

	// Initialize a git repo
	run(t, "git", "init", "-b", "main")
	run(t, "git", "config", "user.email", "test@test.com")
	run(t, "git", "config", "user.name", "Test")

	// Create a file and commit so worktree has something to work with
	os.WriteFile("README.md", []byte("hello"), 0o644)
	run(t, "git", "add", ".")
	run(t, "git", "commit", "-m", "initial")

	// Create a worktree
	worktreeBase := filepath.Join(tmpDir, ".worktrees")
	path, err := CreateWorktree("1.1", worktreeBase)
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Verify the worktree path exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("worktree path %q does not exist", path)
	}

	// Verify the branch name
	expectedDir := filepath.Join(worktreeBase, "task-1-1")
	absExpected, _ := filepath.Abs(expectedDir)
	if path != absExpected {
		t.Errorf("worktree path = %q, want %q", path, absExpected)
	}

	// Verify the branch was created
	out, err := exec.Command("git", "branch").Output()
	if err != nil {
		t.Fatalf("git branch failed: %v", err)
	}
	if !containsStr(string(out), "ralph/task-1-1") {
		t.Errorf("branch ralph/task-1-1 not found in: %s", out)
	}

	// Clean up the worktree
	err = CleanupWorktree("1.1", worktreeBase)
	if err != nil {
		t.Fatalf("CleanupWorktree failed: %v", err)
	}

	// Verify the worktree is gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree path %q still exists after cleanup", path)
	}

	// Verify the branch is gone
	out, err = exec.Command("git", "branch").Output()
	if err != nil {
		t.Fatalf("git branch failed: %v", err)
	}
	if containsStr(string(out), "ralph/task-1-1") {
		t.Errorf("branch ralph/task-1-1 still exists after cleanup")
	}
}

func TestCleanupAll(t *testing.T) {
	// Create a temp directory with a git repo
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)
	os.Chdir(tmpDir)

	// Reset the cache before test
	ResetPrimaryBranchCache()

	// Initialize a git repo
	run(t, "git", "init", "-b", "main")
	run(t, "git", "config", "user.email", "test@test.com")
	run(t, "git", "config", "user.name", "Test")
	os.WriteFile("README.md", []byte("hello"), 0o644)
	run(t, "git", "add", ".")
	run(t, "git", "commit", "-m", "initial")

	// Create multiple worktrees
	worktreeBase := filepath.Join(tmpDir, ".worktrees")
	_, err = CreateWorktree("1.1", worktreeBase)
	if err != nil {
		t.Fatalf("CreateWorktree 1.1 failed: %v", err)
	}
	_, err = CreateWorktree("2.1", worktreeBase)
	if err != nil {
		t.Fatalf("CreateWorktree 2.1 failed: %v", err)
	}

	// Clean up all
	err = CleanupAll(worktreeBase)
	if err != nil {
		t.Fatalf("CleanupAll failed: %v", err)
	}

	// Verify no ralph branches remain
	out, err := exec.Command("git", "branch").Output()
	if err != nil {
		t.Fatalf("git branch failed: %v", err)
	}
	if containsStr(string(out), "ralph/") {
		t.Errorf("ralph branches still exist after CleanupAll: %s", out)
	}

	// Verify worktree base is gone
	if _, err := os.Stat(worktreeBase); !os.IsNotExist(err) {
		t.Errorf("worktree base %q still exists after CleanupAll", worktreeBase)
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
