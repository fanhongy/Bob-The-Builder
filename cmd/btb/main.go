package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/dag"
	"github.com/fanhongy/Bob-The-Builder/internal/git"
	"github.com/fanhongy/Bob-The-Builder/internal/logging"
	"github.com/fanhongy/Bob-The-Builder/internal/orchestrator"
	"github.com/fanhongy/Bob-The-Builder/internal/setup"
	"github.com/fanhongy/Bob-The-Builder/internal/steering"
	"github.com/fanhongy/Bob-The-Builder/internal/tui"
	"github.com/fanhongy/Bob-The-Builder/internal/worker"
)

// braille spinner frames for non-TUI mode
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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

	// Handle --cleanup: clean up all worktrees/branches/locks and exit
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

	// Validate spec
	if err := setup.ValidateSpec(cfg.SpecDir); err != nil {
		logger.Error("invalid spec: %v", err)
		os.Exit(1)
	}

	// Ensure spec files are committed (worktrees need them in git)
	if err := setup.ValidateSpecCommitted(cfg.SpecDir); err != nil {
		logger.Error("spec commit failed: %v", err)
		os.Exit(1)
	}

	// Ensure .gitignore covers btb artifacts
	setup.EnsureGitIgnore(setup.DefaultIgnorePatterns())

	// Install project dependencies (best effort)
	setup.InstallProjectDeps()

	// Check kiro-cli credentials
	if err := setup.CheckKiroCredentials(); err != nil {
		logger.Warn("kiro credentials check: %v", err)
		// Non-fatal: credential check failing should not block execution
		// since kiro-cli might not be installed in all environments
	}

	// Ensure steering docs exist
	if err := steering.EnsureSteeringDocs(cfg.SpecDir, cfg); err != nil {
		logger.Warn("steering docs: %v", err)
		// Non-fatal: continue without steering docs
	}

	// Print pre-TUI banner
	mode := "concurrent"
	if cfg.Sequential {
		mode = "sequential"
	}
	reviewStatus := "on"
	if !cfg.EnableReview {
		reviewStatus = "off"
	}
	fmt.Println()
	fmt.Printf("  \033[1m\033[97mBOB THE BUILDER\033[0m \033[2mconcurrent task orchestrator\033[0m\n")
	fmt.Printf("  \033[37mspec\033[0m %s  \033[37mmode\033[0m %s  \033[37mworkers\033[0m %d+%dr  \033[37mreview\033[0m %s\n",
		cfg.SpecName, mode, cfg.WorkerSlots, cfg.ReviewReservedSlots, reviewStatus)
	fmt.Println()

	// Analyze DAG (show spinner in non-TUI mode)
	var taskDAG *dag.DAG
	spinnerDone := make(chan struct{})

	if !cfg.NoTUI {
		// Show analysis spinner in non-TUI mode (terminal, before TUI takes over)
		go func() {
			i := 0
			for {
				select {
				case <-spinnerDone:
					fmt.Print("\r\033[K") // clear spinner line
					return
				default:
					fmt.Printf("\r  \033[37m  %s  analyzing...\033[0m", spinnerFrames[i%len(spinnerFrames)])
					i++
					time.Sleep(120 * time.Millisecond)
				}
			}
		}()
	}

	if cfg.Sequential {
		taskDAG, err = dag.BuildFallbackDAG(cfg.TaskFile, cfg.DefaultTaskModel)
	} else {
		taskDAG, err = dag.AnalyzeDependencies(cfg.TaskFile, cfg.DesignFile, cfg.RequirementsFile, cfg)
		if err != nil {
			logger.Warn("planner failed, falling back to sequential: %v", err)
			taskDAG, err = dag.BuildFallbackDAG(cfg.TaskFile, cfg.DefaultTaskModel)
		}
	}

	close(spinnerDone)

	if err != nil {
		logger.Error("failed to build DAG: %v", err)
		os.Exit(1)
	}

	// Check for cycles (fall back to sequential on cycle)
	if cycleErr := dag.CheckCycles(taskDAG); cycleErr != nil {
		logger.Warn("cycle detected, falling back to sequential: %v", cycleErr)
		taskDAG, err = dag.BuildFallbackDAG(cfg.TaskFile, cfg.DefaultTaskModel)
		if err != nil {
			logger.Error("failed to build fallback DAG: %v", err)
			os.Exit(1)
		}
	}

	// Validate DAG completeness (warn if tasks missing)
	dagTaskCount := taskDAG.TaskCount()
	logger.Info("DAG analysis complete: %d waves, %d tasks", taskDAG.WaveCount(), dagTaskCount)

	// Save DAG to log file
	if cfg.LogDir != "" {
		dagJSON, marshalErr := json.MarshalIndent(taskDAG, "", "  ")
		if marshalErr == nil {
			dagFile := cfg.LogDir + "/dag.json"
			os.WriteFile(dagFile, dagJSON, 0644)
		}
	}

	// Handle --dry-run: print DAG info and exit
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

	// Initialize state manager
	state := worker.NewStateManager()

	// Initialize TUI (if enabled)
	var tuiInstance *tui.TUI
	if !cfg.NoTUI {
		tuiInstance = tui.NewTUI(taskDAG, cfg)
	}

	var tuiIface orchestrator.TUIInterface
	if tuiInstance != nil {
		tuiIface = tuiInstance
	}

	// Initialize orchestrator
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
		sig := <-sigCh
		logger.Info("received signal %v, shutting down...", sig)
		cancel()

		// Give the orchestrator time to stop gracefully
		time.Sleep(2 * time.Second)

		// Force cleanup if orchestrator hasn't stopped
		if tuiInstance != nil {
			tuiInstance.Stop()
		}
		git.CleanupAll(cfg.WorktreeBase)

		// Print summary even on signal
		fmt.Println()
		fmt.Println("  Interrupted. Cleaned up worktrees and restored terminal.")
		os.Exit(130)
	}()

	// Start TUI if enabled (defer Stop to restore terminal even on panic)
	if tuiInstance != nil {
		if err := tuiInstance.Start(); err != nil {
			logger.Warn("failed to start TUI: %v", err)
		} else {
			defer tuiInstance.Stop()
		}
	}

	// Run the orchestrator
	runErr := orch.Run(ctx)

	// Stop TUI before printing summary
	if tuiInstance != nil {
		tuiInstance.Stop()
	}

	// Print summary
	fmt.Println()
	if runErr != nil {
		if runErr == context.Canceled {
			fmt.Println("  Execution cancelled.")
		} else {
			logger.Error("orchestrator error: %v", runErr)
			os.Exit(1)
		}
	} else {
		completed := state.CountWithStatus(worker.StatusSynced)
		failed := state.CountWithStatus(worker.StatusFailed)
		skipped := state.CountWithStatus(worker.StatusSkipped)
		total := taskDAG.TaskCount()
		fmt.Printf("  Execution complete: %d/%d tasks completed", completed, total)
		if failed > 0 {
			fmt.Printf(", %d failed", failed)
		}
		if skipped > 0 {
			fmt.Printf(", %d skipped", skipped)
		}
		fmt.Println()
	}
}
