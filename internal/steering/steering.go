package steering

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
)

const (
	steeringDir   = ".kiro/steering"
	steeringAgent = "planner"
	steeringModel = "claude-opus-4.6"
)

// HasSteeringDocs checks if .kiro/steering/ exists and has files.
func HasSteeringDocs(dir string) bool {
	steeringPath := filepath.Join(dir, steeringDir)
	entries, err := os.ReadDir(steeringPath)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// GenerateSteeringDocs shells out to kiro-cli with the planner agent
// to generate project.md, conventions.md, spec-context.md in .kiro/steering/.
func GenerateSteeringDocs(specDir string, cfg *config.Config) error {
	taskFile := filepath.Join(specDir, "tasks.md")
	designFile := filepath.Join(specDir, "design.md")
	requirementsFile := filepath.Join(specDir, "requirements.md")

	os.MkdirAll(steeringDir, 0755)

	// Build spec files list
	var specFilesList strings.Builder
	if _, err := os.Stat(taskFile); err == nil {
		specFilesList.WriteString("\n- " + taskFile)
	}
	if _, err := os.Stat(designFile); err == nil {
		specFilesList.WriteString("\n- " + designFile)
	}
	if _, err := os.Stat(requirementsFile); err == nil {
		specFilesList.WriteString("\n- " + requirementsFile)
	}

	// Detect existing codebase structure
	treeSnapshot := detectCodebaseFiles()

	prompt := fmt.Sprintf(`You are setting up project context for an AI-assisted development workflow.

READ THESE SPEC FILES:
%s

EXISTING CODEBASE FILES (if any):
%s

YOUR JOB: Create steering documents in %s/ that give future AI agents
persistent context about this project. These files are automatically included in
every agent conversation, so they must be concise and actionable.

CREATE EXACTLY THESE 3 FILES:

1. %s/project.md
   - Project name and one-line description
   - Tech stack (languages, frameworks, key libraries)
   - Architecture overview (patterns, directory structure)
   - Build/test/run commands
   - Keep it under 80 lines. Be specific, not generic.

2. %s/conventions.md
   - Naming conventions (files, variables, functions, classes)
   - Code style (formatting, imports, error handling patterns)
   - Testing approach (framework, file naming, patterns)
   - Any patterns visible in existing code
   - Keep it under 60 lines. Be opinionated based on what you see.

3. %s/spec-context.md
   - One-paragraph summary of what the spec is building
   - Key architectural decisions from design.md
   - Critical requirements/constraints from requirements.md
   - Reference the spec files by path so agents can read them for details:
     'For full task list, read %s'
     'For architecture details, read %s'
     'For acceptance criteria, read %s'
   - Keep it under 60 lines.

RULES:
- Write ALL 3 files using your file writing tools
- Be concise - these consume tokens on every agent request
- Be specific to THIS project, not generic boilerplate
- If the codebase already has patterns, document them
- If it is a new project, infer conventions from the spec
- Output 'STEERING_COMPLETE' when all 3 files are written`,
		specFilesList.String(),
		treeSnapshot,
		steeringDir,
		steeringDir,
		steeringDir,
		steeringDir,
		taskFile,
		designFile,
		requirementsFile,
	)

	// Run kiro-cli
	logDir := cfg.LogDir
	if logDir == "" {
		logDir = ".ralph-logs"
	}
	os.MkdirAll(logDir, 0755)
	timestamp := time.Now().Format("20060102_150405")
	steeringLog := filepath.Join(logDir, "steering_"+timestamp+".log")

	logFile, _ := os.Create(steeringLog)
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	args := []string{"chat", "--no-interactive", "--agent", steeringAgent,
		"--model", steeringModel, "--trust-all-tools", prompt}
	cmd := exec.Command("kiro-cli", args...)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	cmd.Run() // best-effort, don't fail the build

	// Verify files were created
	created := 0
	for _, f := range []string{"project.md", "conventions.md", "spec-context.md"} {
		if _, err := os.Stat(filepath.Join(steeringDir, f)); err == nil {
			created++
		}
	}

	if created >= 2 {
		// Commit the steering docs
		exec.Command("git", "add", steeringDir+"/").Run()
		exec.Command("git", "commit", "-m", "Add steering docs (generated from spec)", "--allow-empty").Run()
		return nil
	}

	return fmt.Errorf("steering generation incomplete (%d/3 files)", created)
}

// EnsureSteeringDocs checks for existing steering docs and generates them if missing.
func EnsureSteeringDocs(specDir string, cfg *config.Config) error {
	if HasSteeringDocs(".") {
		return nil
	}
	return GenerateSteeringDocs(specDir, cfg)
}

// detectCodebaseFiles returns a snapshot of the codebase file structure.
func detectCodebaseFiles() string {
	extensions := []string{
		".ts", ".tsx", ".js", ".jsx", ".py", ".go", ".rs", ".java",
		".json", ".yaml", ".yml", ".toml", ".cfg",
	}
	specialNames := []string{"Makefile", "Dockerfile"}

	var files []string
	filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip excluded directories
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "node_modules" || base == ".git" || strings.HasPrefix(base, ".ralph-") {
				return filepath.SkipDir
			}
			// Limit depth to 3
			if strings.Count(path, string(os.PathSeparator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if file matches
		ext := filepath.Ext(path)
		for _, e := range extensions {
			if ext == e {
				files = append(files, path)
				return nil
			}
		}
		base := filepath.Base(path)
		for _, name := range specialNames {
			if base == name {
				files = append(files, path)
				return nil
			}
		}
		return nil
	})

	if len(files) > 50 {
		files = files[:50]
	}

	if len(files) == 0 {
		return "No existing source files found - this may be a new project."
	}
	return strings.Join(files, "\n")
}
