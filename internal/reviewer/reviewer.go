package reviewer

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/logging"
)

// ReviewWave performs the post-wave quality gate. It invokes kiro-cli with
// the reviewer agent to audit completed work against the spec. On rejection,
// it invokes the player agent for fixes and re-reviews up to MaxReviewRetries.
// Returns nil even if review fails (non-blocking, matching original behavior).
func ReviewWave(waveLabel string, taskIDs []string, baseSHA string, cfg *config.Config, logger *logging.Logger) error {
	if !cfg.EnableReview {
		return nil
	}

	maxRetries := cfg.MaxReviewRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}

	// Build summary of changed files
	diffRef := baseSHA
	if diffRef == "" {
		diffRef = "HEAD~1"
	}
	changedOut, _ := exec.Command("git", "diff", "--name-only", diffRef, "HEAD").Output()
	changedFiles := strings.TrimSpace(string(changedOut))

	taskList := strings.Join(taskIDs, ", ")

	for attempt := 1; attempt <= maxRetries; attempt++ {
		logger.Info("reviewing wave %s (attempt %d/%d)", waveLabel, attempt, maxRetries)

		reviewPrompt := buildReviewPrompt(waveLabel, taskList, changedFiles, cfg)

		// Run reviewer agent
		cmd := exec.Command("kiro-cli", "chat", "--no-interactive", "--agent", "reviewer", "--trust-all-tools", reviewPrompt)
		output, _ := cmd.CombinedOutput()
		response := string(output)

		// Check for AUDIT_PASSED
		if strings.Contains(response, "AUDIT_PASSED") {
			logger.Info("wave %s review passed (attempt %d)", waveLabel, attempt)
			return nil
		}

		// Extract rejection reason
		rejectionReason := extractRejection(response)
		logger.Warn("wave %s review rejected (attempt %d/%d): %s", waveLabel, attempt, maxRetries, truncate(rejectionReason, 100))

		// If we have retries left, invoke the fixer
		if attempt < maxRetries {
			fixPrompt := buildFixPrompt(rejectionReason, changedFiles, cfg)

			fixCmd := exec.Command("kiro-cli", "chat", "--no-interactive", "--agent", "player", "--trust-all-tools", fixPrompt)
			fixCmd.CombinedOutput()

			// Commit the fixes
			exec.Command("git", "add", "--all").Run()
			exec.Command("git", "commit", "-m", fmt.Sprintf("Wave %s review fix (attempt %d)", waveLabel, attempt), "--allow-empty").Run()

			// Update changed files for next review
			changedOut, _ = exec.Command("git", "diff", "--name-only", diffRef, "HEAD").Output()
			changedFiles = strings.TrimSpace(string(changedOut))
		}
	}

	// All retries exhausted - non-blocking (matching original behavior)
	logger.Warn("wave %s review failed after %d attempts - proceeding with warning", waveLabel, maxRetries)
	return nil
}

func buildReviewPrompt(waveLabel, taskList, changedFiles string, cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("WAVE %s QUALITY REVIEW\n\n", waveLabel))
	sb.WriteString("You are auditing work that was just completed. Review it against the spec.\n\n")
	sb.WriteString(fmt.Sprintf("SPEC FILES TO READ:\n"))
	sb.WriteString(fmt.Sprintf("- %s/design.md (architecture and interfaces)\n", cfg.SpecDir))
	sb.WriteString(fmt.Sprintf("- %s/requirements.md (acceptance criteria)\n", cfg.SpecDir))
	sb.WriteString(fmt.Sprintf("- %s (task list)\n\n", cfg.TaskFile))
	sb.WriteString(fmt.Sprintf("TASKS COMPLETED IN THIS WAVE: %s\n\n", taskList))
	sb.WriteString(fmt.Sprintf("FILES CHANGED:\n%s\n\n", changedFiles))
	sb.WriteString("REVIEW CHECKLIST:\n")
	sb.WriteString("1. Read each changed source file and verify it matches the spec\n")
	sb.WriteString("2. Check that implementations are complete (no TODOs, no placeholders)\n")
	sb.WriteString("3. Verify type safety\n")
	sb.WriteString("4. Check that imports/exports are correct\n")
	sb.WriteString("5. Verify tasks.md was updated\n")
	sb.WriteString("6. Check for obvious bugs, missing error handling, or security issues\n\n")
	sb.WriteString("OUTPUT FORMAT:\n")
	sb.WriteString("- If everything passes: output exactly 'AUDIT_PASSED'\n")
	sb.WriteString("- If issues found: output 'REJECTED: ' followed by a numbered list of specific issues\n")
	return sb.String()
}

func buildFixPrompt(rejectionReason, changedFiles string, cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString("CODE REVIEW FIX REQUEST\n\n")
	sb.WriteString("The reviewer found issues with the recent implementation. Fix them.\n\n")
	sb.WriteString(fmt.Sprintf("REJECTION DETAILS:\n%s\n\n", rejectionReason))
	sb.WriteString(fmt.Sprintf("SPEC FILES:\n"))
	sb.WriteString(fmt.Sprintf("- %s/design.md\n", cfg.SpecDir))
	sb.WriteString(fmt.Sprintf("- %s/requirements.md\n", cfg.SpecDir))
	sb.WriteString(fmt.Sprintf("- %s\n\n", cfg.TaskFile))
	sb.WriteString(fmt.Sprintf("FILES TO CHECK/FIX:\n%s\n\n", changedFiles))
	sb.WriteString("INSTRUCTIONS:\n")
	sb.WriteString("1. Read the rejection details carefully\n")
	sb.WriteString("2. Fix the specific issues\n")
	sb.WriteString("3. Verify your fixes resolve ALL the listed issues\n")
	sb.WriteString("4. Output 'FIXES_APPLIED' when done\n")
	return sb.String()
}

func extractRejection(response string) string {
	lines := strings.Split(response, "\n")
	var result []string
	found := false
	for _, line := range lines {
		if strings.Contains(line, "REJECTED:") {
			found = true
		}
		if found {
			result = append(result, line)
			if len(result) >= 30 {
				break
			}
		}
	}
	if len(result) == 0 {
		return "Reviewer did not output AUDIT_PASSED (ambiguous result)"
	}
	return strings.Join(result, "\n")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
