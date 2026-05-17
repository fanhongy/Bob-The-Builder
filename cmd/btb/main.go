package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/dag"
	"github.com/fanhongy/Bob-The-Builder/internal/git"
	"github.com/fanhongy/Bob-The-Builder/internal/logging"
	"github.com/fanhongy/Bob-The-Builder/internal/orchestrator"
	"github.com/fanhongy/Bob-The-Builder/internal/tui"
	"github.com/fanhongy/Bob-The-Builder/internal/worker"
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
		git.CleanupAll(cfg.WorktreeBase)
		os.Remove(".ralph-merge-lock")
		fmt.Println("Cleanup complete: removed all worktrees, branches, and locks")
		os.Exit(0)
	}

	if cfg.SpecDir == "" && specName == "" {
		fmt.Fprintf(os.Stderr, "Error: spec name or --spec-dir is required\n\n")
		flag.Usage()
		os.Exit(1)
	}

	// Set up logger
	debugLogPath := ""
	if cfg.LogDir != "" {
		os.MkdirAll(cfg.LogDir, 0755)
		debugLogPath = cfg.LogDir + "/debug.log"
	}
	logger, err := logging.NewLogger(logging.LevelInfo, debugLogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	// Ensure git is ready
	if err := git.EnsureGitReady(); err != nil {
		logger.Error("git not ready: %v", err)
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

	// Analyze DAG
	var taskDAG *dag.DAG
	if cfg.Sequential {
		taskDAG, err = dag.BuildFallbackDAG(cfg.TaskFile, cfg.DefaultTaskModel)
	} else {
		taskDAG, err = dag.AnalyzeDependencies(cfg.TaskFile, cfg.DesignFile, cfg.RequirementsFile, cfg)
		if err != nil {
			logger.Warn("planner failed, falling back to sequential: %v", err)
			taskDAG, err = dag.BuildFallbackDAG(cfg.TaskFile, cfg.DefaultTaskModel)
		}
	}
	if err != nil {
		logger.Error("failed to build DAG: %v", err)
		os.Exit(1)
	}

	// Check for cycles
	if cycleErr := dag.CheckCycles(taskDAG); cycleErr != nil {
		logger.Warn("cycle detected, falling back to sequential: %v", cycleErr)
		taskDAG, err = dag.BuildFallbackDAG(cfg.TaskFile, cfg.DefaultTaskModel)
		if err != nil {
			logger.Error("failed to build fallback DAG: %v", err)
			os.Exit(1)
		}
	}

	logger.Info("DAG analysis complete: %d waves, %d tasks", taskDAG.WaveCount(), taskDAG.TaskCount())

	if cfg.DryRun {
		fmt.Printf("\nDry run complete: %d waves, %d tasks\n", taskDAG.WaveCount(), taskDAG.TaskCount())
		for i, wave := range taskDAG.Waves {
			fmt.Printf("  Wave %d: ", i)
			for j, task := range wave.Tasks {
				if j > 0 {
					fmt.Print(", ")
				}
				fmt.Print(task.ID)
			}
			fmt.Println()
		}
		os.Exit(0)
	}

	// Initialize state manager and orchestrator
	state := worker.NewStateManager()

	var tuiInstance *tui.TUI
	if !cfg.NoTUI {
		tuiInstance = tui.NewTUI(taskDAG, cfg)
	}

	var tuiIface orchestrator.TUIInterface
	if tuiInstance != nil {
		tuiIface = tuiInstance
	}

	orch := &orchestrator.Orchestrator{
		Config: cfg,
		DAG:    taskDAG,
		State:  state,
		Logger: logger,
		TUI:    tuiIface,
	}

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("received shutdown signal, cleaning up...")
		cancel()
	}()

	// Start TUI if enabled
	if tuiInstance != nil {
		if err := tuiInstance.Start(); err != nil {
			logger.Warn("failed to start TUI: %v", err)
		} else {
			defer tuiInstance.Stop()
		}
	}

	// Run the orchestrator
	if err := orch.Run(ctx); err != nil {
		if tuiInstance != nil {
			tuiInstance.Stop()
		}
		if err == context.Canceled {
			logger.Info("execution cancelled")
		} else {
			logger.Error("orchestrator error: %v", err)
			os.Exit(1)
		}
	} else {
		if tuiInstance != nil {
			tuiInstance.Stop()
		}
	}
}
