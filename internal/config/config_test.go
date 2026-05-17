package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfigDefaults(t *testing.T) {
	cfg := NewConfig(CLIFlags{
		MaxParallel: 6,
		MaxIters:    20,
	})

	if cfg.MaxParallel != 6 {
		t.Errorf("MaxParallel = %d, want 6", cfg.MaxParallel)
	}
	if cfg.MaxIters != 20 {
		t.Errorf("MaxIters = %d, want 20", cfg.MaxIters)
	}
	if cfg.WorkerSlots != 5 {
		t.Errorf("WorkerSlots = %d, want 5", cfg.WorkerSlots)
	}
	if cfg.ReviewReservedSlots != 1 {
		t.Errorf("ReviewReservedSlots = %d, want 1", cfg.ReviewReservedSlots)
	}
	if cfg.EnableReview != true {
		t.Errorf("EnableReview = %v, want true", cfg.EnableReview)
	}
	if cfg.StaleThreshold != 600 {
		t.Errorf("StaleThreshold = %d, want 600", cfg.StaleThreshold)
	}
	if cfg.JobTimeout != 43200 {
		t.Errorf("JobTimeout = %d, want 43200", cfg.JobTimeout)
	}
	if cfg.TaskCompletePrefix != "TASK_COMPLETE" {
		t.Errorf("TaskCompletePrefix = %q, want %q", cfg.TaskCompletePrefix, "TASK_COMPLETE")
	}
}

func TestNewConfigNoReview(t *testing.T) {
	cfg := NewConfig(CLIFlags{
		MaxParallel: 6,
		MaxIters:    20,
		NoReview:    true,
	})
	if cfg.EnableReview != false {
		t.Errorf("EnableReview = %v, want false when NoReview=true", cfg.EnableReview)
	}
}

func TestDeriveWorkerSlots(t *testing.T) {
	cfg := &Config{MaxParallel: 2, ReviewReservedSlots: 3}
	cfg.DeriveWorkerSlots()
	if cfg.WorkerSlots != 1 {
		t.Errorf("WorkerSlots = %d, want 1 (minimum)", cfg.WorkerSlots)
	}

	cfg = &Config{MaxParallel: 10, ReviewReservedSlots: 2}
	cfg.DeriveWorkerSlots()
	if cfg.WorkerSlots != 8 {
		t.Errorf("WorkerSlots = %d, want 8", cfg.WorkerSlots)
	}
}

func TestResolveSpecDir(t *testing.T) {
	// Create temporary directory structure for testing
	tmpDir, err := os.MkdirTemp("", "btb-config-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save and restore working directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)
	os.Chdir(tmpDir)

	// Create .kiro/specs/my-spec directory
	kiroSpec := filepath.Join(tmpDir, ".kiro", "specs", "my-spec")
	if err := os.MkdirAll(kiroSpec, 0755); err != nil {
		t.Fatal(err)
	}

	result := resolveSpecDir("my-spec")
	expected := filepath.Join(".kiro", "specs", "my-spec")
	if result != expected {
		t.Errorf("resolveSpecDir = %q, want %q", result, expected)
	}

	// Test specs/<name> fallback
	specsDir := filepath.Join(tmpDir, "specs", "other-spec")
	if err := os.MkdirAll(specsDir, 0755); err != nil {
		t.Fatal(err)
	}

	result = resolveSpecDir("other-spec")
	expected = filepath.Join("specs", "other-spec")
	if result != expected {
		t.Errorf("resolveSpecDir = %q, want %q", result, expected)
	}

	// Test fallback to default when nothing exists
	result = resolveSpecDir("nonexistent")
	expected = filepath.Join(".kiro", "specs", "nonexistent")
	if result != expected {
		t.Errorf("resolveSpecDir = %q, want %q", result, expected)
	}
}

func TestNewConfigWithSpecDir(t *testing.T) {
	cfg := NewConfig(CLIFlags{
		SpecDir:     "/some/path",
		MaxParallel: 6,
		MaxIters:    20,
	})

	if cfg.SpecDir != "/some/path" {
		t.Errorf("SpecDir = %q, want %q", cfg.SpecDir, "/some/path")
	}
	if cfg.TaskFile != "/some/path/tasks.md" {
		t.Errorf("TaskFile = %q, want %q", cfg.TaskFile, "/some/path/tasks.md")
	}
	if cfg.DesignFile != "/some/path/design.md" {
		t.Errorf("DesignFile = %q, want %q", cfg.DesignFile, "/some/path/design.md")
	}
}
