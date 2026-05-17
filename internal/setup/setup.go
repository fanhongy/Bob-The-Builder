package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ValidateSpec verifies that the spec directory contains tasks.md.
// It warns (does not fail) if design.md or requirements.md are missing.
func ValidateSpec(specDir string) error {
	tasksFile := specDir + "/tasks.md"
	if _, err := os.Stat(tasksFile); os.IsNotExist(err) {
		return fmt.Errorf("tasks.md not found in %s", specDir)
	}

	// Warn about missing optional files (informational only)
	for _, f := range []string{"design.md", "requirements.md"} {
		if _, err := os.Stat(specDir + "/" + f); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  warning: %s/%s not found (optional)\n", specDir, f)
		}
	}
	return nil
}

// ValidateSpecCommitted checks for untracked/modified spec files and auto-commits if found.
func ValidateSpecCommitted(specDir string) error {
	if specDir == "" {
		return nil
	}
	if _, err := os.Stat(specDir); os.IsNotExist(err) {
		return nil // will fail later with proper error
	}

	// Check for untracked files
	untrackedOut, _ := exec.Command("git", "ls-files", "--others", "--exclude-standard", specDir).Output()
	untracked := strings.TrimSpace(string(untrackedOut))

	// Check for modified files
	modifiedOut, _ := exec.Command("git", "diff", "--name-only", specDir).Output()
	modified := strings.TrimSpace(string(modifiedOut))

	if untracked == "" && modified == "" {
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n  Spec files are not committed to git.\n")
	fmt.Fprintf(os.Stderr, "  btb creates git worktrees for parallel task execution. Uncommitted files\n")
	fmt.Fprintf(os.Stderr, "  will not exist in the worktrees, causing workers to crash.\n\n")

	if untracked != "" {
		fmt.Fprintf(os.Stderr, "  Untracked files in %s:\n", specDir)
		for _, line := range strings.Split(untracked, "\n") {
			if line != "" {
				fmt.Fprintf(os.Stderr, "    %s\n", line)
			}
		}
	}
	if modified != "" {
		fmt.Fprintf(os.Stderr, "  Modified files in %s:\n", specDir)
		for _, line := range strings.Split(modified, "\n") {
			if line != "" {
				fmt.Fprintf(os.Stderr, "    %s\n", line)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "\n  Auto-committing spec files...\n")
	exec.Command("git", "add", specDir).Run()
	cmd := exec.Command("git", "commit", "-m", "btb: auto-commit spec files")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not auto-commit spec files: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  Spec files committed successfully.\n\n")
	return nil
}

// DefaultIgnorePatterns returns the list of gitignore patterns used by btb.
func DefaultIgnorePatterns() []string {
	return []string{
		".ralph-logs/",
		".ralph-worktrees/",
		".ralph-build-cache/",
		"target/",
		"build/",
		"dist/",
		"out/",
		".next/",
		"node_modules/",
		"__pycache__/",
		"*.pyc",
		".pytest_cache/",
		".cargo/",
		"*.class",
		"bin/",
		"obj/",
	}
}

// EnsureGitIgnore adds patterns to .gitignore if not already present.
func EnsureGitIgnore(patterns []string) error {
	existing := make(map[string]bool)

	if f, err := os.Open(".gitignore"); err == nil {
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			existing[scanner.Text()] = true
		}
		f.Close()
	}

	var toAdd []string
	for _, p := range patterns {
		if !existing[p] {
			toAdd = append(toAdd, p)
		}
	}

	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open .gitignore: %w", err)
	}
	defer f.Close()

	for _, p := range toAdd {
		if _, err := fmt.Fprintln(f, p); err != nil {
			return err
		}
	}
	return nil
}

// InstallProjectDeps auto-detects the project type and installs dependencies.
// This is best-effort: failures are printed but do not cause btb to abort.
func InstallProjectDeps() error {
	installed := 0

	// Node.js
	if _, err := os.Stat("package.json"); err == nil {
		if _, err := os.Stat("node_modules"); os.IsNotExist(err) {
			pkgMgr, installCmd := detectNodePackageManager()
			if path, err := exec.LookPath(pkgMgr); err == nil && path != "" {
				fmt.Fprintf(os.Stderr, "  installing node dependencies (%s)...\n", pkgMgr)
				parts := strings.Fields(installCmd)
				cmd := exec.Command(parts[0], parts[1:]...)
				if err := cmd.Run(); err == nil {
					fmt.Fprintf(os.Stderr, "  node dependencies installed\n")
					installed++
				} else {
					fmt.Fprintf(os.Stderr, "  node dependency install failed - agents will handle it\n")
				}
			}
		}
	}

	// Python (requirements.txt)
	if _, err := os.Stat("requirements.txt"); err == nil {
		if _, err := os.Stat(".venv"); os.IsNotExist(err) {
			if _, err := exec.LookPath("python3"); err == nil {
				fmt.Fprintf(os.Stderr, "  installing python dependencies...\n")
				if err := exec.Command("python3", "-m", "venv", ".venv").Run(); err == nil {
					if err := exec.Command(".venv/bin/pip", "install", "-r", "requirements.txt").Run(); err == nil {
						fmt.Fprintf(os.Stderr, "  python dependencies installed\n")
						installed++
					} else {
						fmt.Fprintf(os.Stderr, "  python dependency install failed - agents will handle it\n")
					}
				}
			}
		}
	}

	// Python (pyproject.toml + poetry)
	if _, err := os.Stat("pyproject.toml"); err == nil {
		if _, err := os.Stat(".venv"); os.IsNotExist(err) {
			if _, err := exec.LookPath("poetry"); err == nil {
				fmt.Fprintf(os.Stderr, "  installing python dependencies (poetry)...\n")
				if err := exec.Command("poetry", "install").Run(); err == nil {
					fmt.Fprintf(os.Stderr, "  python dependencies installed\n")
					installed++
				} else {
					fmt.Fprintf(os.Stderr, "  poetry install failed - agents will handle it\n")
				}
			}
		}
	}

	// Go
	if _, err := os.Stat("go.mod"); err == nil {
		if _, err := exec.LookPath("go"); err == nil {
			fmt.Fprintf(os.Stderr, "  downloading go modules...\n")
			if err := exec.Command("go", "mod", "download").Run(); err == nil {
				fmt.Fprintf(os.Stderr, "  go modules downloaded\n")
				installed++
			}
		}
	}

	_ = installed
	return nil
}

// CheckKiroCredentials verifies kiro-cli authentication status.
func CheckKiroCredentials() error {
	// Try 'auth status' subcommand first
	out, err := exec.Command("kiro-cli", "auth", "status").CombinedOutput()
	if err == nil {
		return nil
	}

	// If unrecognized subcommand, fall back to checking credential db
	if strings.Contains(string(out), "unrecognized subcommand") {
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			home, _ := os.UserHomeDir()
			dataHome = home + "/.local/share"
		}
		dbPath := dataHome + "/kiro-cli/data.sqlite3"
		if _, err := os.Stat(dbPath); err == nil {
			return nil
		}
	}

	return fmt.Errorf("kiro-cli is not authenticated. Run: kiro-cli login --use-device-flow")
}

func detectNodePackageManager() (string, string) {
	if _, err := os.Stat("pnpm-lock.yaml"); err == nil {
		return "pnpm", "pnpm install --frozen-lockfile"
	}
	if _, err := os.Stat("yarn.lock"); err == nil {
		return "yarn", "yarn install --frozen-lockfile"
	}
	if _, err := os.Stat("bun.lockb"); err == nil {
		return "bun", "bun install --frozen-lockfile"
	}
	if _, err := os.Stat("bun.lock"); err == nil {
		return "bun", "bun install --frozen-lockfile"
	}
	if _, err := os.Stat("package-lock.json"); err == nil {
		return "npm", "npm ci"
	}
	return "npm", "npm install"
}
