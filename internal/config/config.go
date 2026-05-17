package config

import (
	"os"
	"path/filepath"
)

// CLIFlags holds the parsed command-line flags passed from main.
type CLIFlags struct {
	SpecDir     string
	SpecName    string
	MaxParallel int
	MaxIters    int
	Sequential  bool
	DryRun      bool
	NoTUI       bool
	NoReview    bool
	Cleanup     bool
}

// Config holds all configuration values for Bob the Builder.
type Config struct {
	// Spec settings
	SpecDir          string
	SpecName         string
	TaskFile         string
	DesignFile       string
	RequirementsFile string

	// Concurrency settings
	MaxParallel         int
	MaxIters            int
	ReviewReservedSlots int
	WorkerSlots         int
	WorktreeBase        string
	SyncInterval        int

	// Execution mode
	Sequential bool
	DryRun     bool
	NoTUI      bool
	Cleanup    bool

	// Retry / Safety
	MaxRetries     int
	StaleThreshold int
	JobTimeout     int
	RateLimitPause int

	// Review settings
	EnableReview     bool
	MaxReviewRetries int
	ReviewBatchSize  int
	ReviewTimeout    int

	// Shared build cache
	SharedBuildCacheDir string

	// Logging
	LogDir string

	// Model settings
	AvailableModels  string
	DefaultTaskModel string

	// Health check
	HealthCheckEnabled  bool
	HealthCheckInterval int

	// Completion tokens
	TaskCompletePrefix string
}

// NewConfig creates a Config with defaults matching config.sh, then applies CLI flags.
func NewConfig(flags CLIFlags) *Config {
	cfg := &Config{
		// Defaults from config.sh
		MaxParallel:         6,
		MaxIters:            20,
		ReviewReservedSlots: 1,
		WorktreeBase:        "../.ralph-worktrees",
		SyncInterval:        5,

		MaxRetries:     10,
		StaleThreshold: 600,
		JobTimeout:     43200,
		RateLimitPause: 3,

		EnableReview:     true,
		MaxReviewRetries: 4,
		ReviewBatchSize:  3,
		ReviewTimeout:    1800,

		SharedBuildCacheDir: "../.ralph-build-cache",
		LogDir:             ".ralph-logs",

		AvailableModels:  "claude-sonnet-4.5,claude-opus-4.6",
		DefaultTaskModel: "claude-opus-4.6",

		HealthCheckEnabled:  true,
		HealthCheckInterval: 1800,

		TaskCompletePrefix: "TASK_COMPLETE",
	}

	// Apply CLI flags
	cfg.SpecName = flags.SpecName
	cfg.MaxParallel = flags.MaxParallel
	cfg.MaxIters = flags.MaxIters
	cfg.Sequential = flags.Sequential
	cfg.DryRun = flags.DryRun
	cfg.NoTUI = flags.NoTUI
	cfg.Cleanup = flags.Cleanup

	if flags.NoReview {
		cfg.EnableReview = false
	}

	// Resolve spec directory
	if flags.SpecDir != "" {
		cfg.SpecDir = flags.SpecDir
	} else if flags.SpecName != "" {
		cfg.SpecDir = resolveSpecDir(flags.SpecName)
	}

	// Derive file paths from spec dir
	if cfg.SpecDir != "" {
		cfg.TaskFile = filepath.Join(cfg.SpecDir, "tasks.md")
		cfg.DesignFile = filepath.Join(cfg.SpecDir, "design.md")
		cfg.RequirementsFile = filepath.Join(cfg.SpecDir, "requirements.md")
	}

	// Derive worker slots
	cfg.DeriveWorkerSlots()

	return cfg
}

// DeriveWorkerSlots calculates WorkerSlots from MaxParallel - ReviewReservedSlots.
func (c *Config) DeriveWorkerSlots() {
	c.WorkerSlots = c.MaxParallel - c.ReviewReservedSlots
	if c.WorkerSlots < 1 {
		c.WorkerSlots = 1
	}
}

// resolveSpecDir resolves the spec directory from a spec name.
// It checks .kiro/specs/<name>, specs/<name>, <name> as-is.
func resolveSpecDir(specName string) string {
	candidates := []string{
		filepath.Join(".kiro", "specs", specName),
		filepath.Join("specs", specName),
		specName,
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}

	// Default to .kiro/specs/<name> (will fail at validation)
	return filepath.Join(".kiro", "specs", specName)
}
