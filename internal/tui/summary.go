package tui

import (
	"fmt"
	"time"
)

// PrintSummary prints the post-run summary after the TUI exits.
func PrintSummary(completed, total, failed, skipped, waves int, elapsed time.Duration, reviewPasses, reviewFixes int, logDir string) {
	elapsedStr := FormatDuration(elapsed)

	fmt.Println()
	fmt.Printf("  %s%sBOB THE BUILDER%s %srun complete%s\n", Bold, White, Reset, Dim, Reset)
	fmt.Println()
	fmt.Printf("  %stasks%s  %d/%d completed\n", Dim, Reset, completed, total)
	if failed > 0 {
		fmt.Printf("  %sfailed%s %s%d%s\n", Dim, Reset, Red, failed, Reset)
	}
	if skipped > 0 {
		fmt.Printf("  %sskipped%s %s%d%s\n", Dim, Reset, Yellow, skipped, Reset)
	}
	fmt.Printf("  %swaves%s  %d\n", Dim, Reset, waves)
	fmt.Printf("  %stime%s   %s\n", Dim, Reset, elapsedStr)
	fmt.Printf("  %sreview%s %d passed, %d fix cycles\n", Dim, Reset, reviewPasses, reviewFixes)
	fmt.Printf("  %slogs%s   %s/\n", Dim, Reset, logDir)
	fmt.Println()

	if completed == total && failed == 0 {
		fmt.Printf("  %s%sall tasks completed%s\n", Bold, White, Reset)
	} else if failed > 0 {
		fmt.Printf("  %ssome tasks failed%s\n", Red, Reset)
	} else {
		fmt.Printf("  %sincomplete%s\n", Yellow, Reset)
	}
	fmt.Println()
}
