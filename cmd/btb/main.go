package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
)

func main() {
	specDir := flag.String("spec-dir", "", "Explicit path to spec directory")
	maxParallel := flag.Int("max-parallel", 6, "Max total concurrent agent processes")
	maxIters := flag.Int("max-iters", 20, "Max iterations per task loop")
	sequential := flag.Bool("sequential", false, "Force sequential execution")
	dryRun := flag.Bool("dry-run", false, "Analyze DAG only, do not execute")
	noTUI := flag.Bool("no-tui", false, "Disable interactive TUI")
	noReview := flag.Bool("no-review", false, "Skip review gate")
	cleanup := flag.Bool("cleanup", false, "Remove all worktrees/branches/locks")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: btb [flags] <spec-name>\n\n")
		fmt.Fprintf(os.Stderr, "Bob the Builder - Parallel task runner with DAG scheduling\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// First positional argument is the spec name
	specName := ""
	if flag.NArg() > 0 {
		specName = flag.Arg(0)
	}

	cfg := config.NewConfig(config.CLIFlags{
		SpecDir:     *specDir,
		SpecName:    specName,
		MaxParallel: *maxParallel,
		MaxIters:    *maxIters,
		Sequential:  *sequential,
		DryRun:      *dryRun,
		NoTUI:       *noTUI,
		NoReview:    *noReview,
		Cleanup:     *cleanup,
	})

	if *cleanup {
		fmt.Println("Cleanup mode: would remove all worktrees/branches/locks")
		os.Exit(0)
	}

	if cfg.SpecDir == "" && specName == "" {
		fmt.Fprintf(os.Stderr, "Error: spec name or --spec-dir is required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	fmt.Printf("Bob the Builder - Go Edition\n")
	fmt.Printf("  Spec Dir:     %s\n", cfg.SpecDir)
	fmt.Printf("  Max Parallel: %d\n", cfg.MaxParallel)
	fmt.Printf("  Worker Slots: %d\n", cfg.WorkerSlots)
	fmt.Printf("  Max Iters:    %d\n", cfg.MaxIters)
	fmt.Printf("  Sequential:   %v\n", cfg.Sequential)
	fmt.Printf("  Dry Run:      %v\n", cfg.DryRun)
	fmt.Printf("  No TUI:       %v\n", cfg.NoTUI)
	fmt.Printf("  Review:       %v\n", cfg.EnableReview)
}
