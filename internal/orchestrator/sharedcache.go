package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SetupSharedBuildCache configures shared build cache environment variables
// and creates symlinks for worktree builds. Returns environment variables
// to pass to worker processes.
func SetupSharedBuildCache(worktreePath string, cacheDir string) []string {
	if cacheDir == "" {
		return nil
	}

	// Resolve absolute path
	absCacheDir, err := filepath.Abs(cacheDir)
	if err != nil {
		return nil
	}

	os.MkdirAll(absCacheDir, 0755)

	// Set up cache subdirectories
	cargoTarget := filepath.Join(absCacheDir, "cargo-target")
	gradleHome := filepath.Join(absCacheDir, "gradle")
	goPath := filepath.Join(absCacheDir, "go")
	goCache := filepath.Join(absCacheDir, "go-cache")
	pipCache := filepath.Join(absCacheDir, "pip-cache")

	os.MkdirAll(cargoTarget, 0755)
	os.MkdirAll(gradleHome, 0755)
	os.MkdirAll(goCache, 0755)
	os.MkdirAll(pipCache, 0755)

	envVars := []string{
		"CARGO_TARGET_DIR=" + cargoTarget,
		"GRADLE_USER_HOME=" + gradleHome,
		"GOPATH=" + goPath,
		"GOCACHE=" + goCache,
		"PIP_CACHE_DIR=" + pipCache,
	}

	// Node.js: symlink node_modules into the worktree
	if worktreePath != "" {
		packageJSON := filepath.Join(worktreePath, "package.json")
		nodeModules := filepath.Join(worktreePath, "node_modules")

		if _, err := os.Stat(packageJSON); err == nil {
			if _, err := os.Stat(nodeModules); os.IsNotExist(err) {
				// Try shared cache first
				cacheNM := filepath.Join(absCacheDir, "node_modules")
				if _, err := os.Stat(cacheNM); err == nil {
					os.Symlink(cacheNM, nodeModules)
				} else {
					// Fall back to main repo's node_modules
					mainRepoRoot := getMainRepoRoot()
					if mainRepoRoot != "" {
						mainNM := filepath.Join(mainRepoRoot, "node_modules")
						if _, err := os.Stat(mainNM); err == nil {
							os.Symlink(mainNM, nodeModules)
						}
					}
				}
			}
		}
	}

	return envVars
}

func getMainRepoRoot() string {
	out, err := exec.Command("git", "worktree", "list").Output()
	if err != nil {
		return ""
	}
	// First line is the main worktree; output is newline-delimited.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	// Parse first word of first line (the worktree path)
	firstLine := lines[0]
	for i, c := range firstLine {
		if c == ' ' || c == '\t' {
			return firstLine[:i]
		}
	}
	return firstLine
}
