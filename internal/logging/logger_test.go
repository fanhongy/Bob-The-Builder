package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLoggerNoFile(t *testing.T) {
	l, err := NewLogger(LevelInfo, "")
	if err != nil {
		t.Fatalf("NewLogger error: %v", err)
	}
	defer l.Close()

	if l.debugLog != nil {
		t.Error("expected debugLog to be nil when no path given")
	}
}

func TestNewLoggerWithFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "btb-log-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	logPath := filepath.Join(tmpDir, "debug.log")
	l, err := NewLogger(LevelDebug, logPath)
	if err != nil {
		t.Fatalf("NewLogger error: %v", err)
	}

	l.Info("hello %s", "world")
	l.Debug("debug msg")
	l.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "INFO: hello world") {
		t.Errorf("log file missing INFO message, got: %s", content)
	}
	if !strings.Contains(content, "DEBUG: debug msg") {
		t.Errorf("log file missing DEBUG message, got: %s", content)
	}
}

func TestLevelFiltering(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "btb-log-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	logPath := filepath.Join(tmpDir, "debug.log")
	l, err := NewLogger(LevelWarn, logPath)
	if err != nil {
		t.Fatalf("NewLogger error: %v", err)
	}

	l.Debug("should not appear")
	l.Info("should not appear")
	l.Warn("warning message")
	l.Error("error message")
	l.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "should not appear") {
		t.Errorf("log file should not contain filtered messages, got: %s", content)
	}
	if !strings.Contains(content, "WARN: warning message") {
		t.Errorf("log file missing WARN message, got: %s", content)
	}
	if !strings.Contains(content, "ERROR: error message") {
		t.Errorf("log file missing ERROR message, got: %s", content)
	}
}

func TestTUIMode(t *testing.T) {
	l, err := NewLogger(LevelInfo, "")
	if err != nil {
		t.Fatalf("NewLogger error: %v", err)
	}
	defer l.Close()

	l.SetTUIMode(true)
	l.Info("msg1")
	l.Warn("msg2")
	l.Task("task msg")
	l.Wave("wave msg")

	msgs := l.Messages()
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4", len(msgs))
	}
	if msgs[0].Content != "msg1" || msgs[0].Level != "INFO" {
		t.Errorf("msgs[0] = %+v, want INFO/msg1", msgs[0])
	}
	if msgs[1].Content != "msg2" || msgs[1].Level != "WARN" {
		t.Errorf("msgs[1] = %+v, want WARN/msg2", msgs[1])
	}
	if msgs[2].Content != "task msg" || msgs[2].Level != "TASK" {
		t.Errorf("msgs[2] = %+v, want TASK/task msg", msgs[2])
	}
	if msgs[3].Content != "wave msg" || msgs[3].Level != "WAVE" {
		t.Errorf("msgs[3] = %+v, want WAVE/wave msg", msgs[3])
	}
}

func TestRingBufferOverflow(t *testing.T) {
	l, err := NewLogger(LevelInfo, "")
	if err != nil {
		t.Fatalf("NewLogger error: %v", err)
	}
	defer l.Close()

	l.SetTUIMode(true)

	// Fill beyond ring buffer size
	for i := 0; i < ringBufferSize+10; i++ {
		l.Info("msg %d", i)
	}

	msgs := l.Messages()
	if len(msgs) != ringBufferSize {
		t.Errorf("ring buffer size = %d, want %d", len(msgs), ringBufferSize)
	}
	// First message should be msg 10 (first 10 were dropped)
	if msgs[0].Content != "msg 10" {
		t.Errorf("first msg = %q, want %q", msgs[0].Content, "msg 10")
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"error", LevelError},
		{"unknown", LevelInfo},
	}

	for _, tc := range tests {
		result := ParseLevel(tc.input)
		if result != tc.expected {
			t.Errorf("ParseLevel(%q) = %d, want %d", tc.input, result, tc.expected)
		}
	}
}
