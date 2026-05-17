package taskfile

import (
	"path/filepath"
	"runtime"
	"testing"
)

func testdataPath(name string) string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata", name)
}

func TestGetAllLeafTasks(t *testing.T) {
	path := testdataPath("tasks.md")
	tasks, err := GetAllLeafTasks(path)
	if err != nil {
		t.Fatalf("GetAllLeafTasks error: %v", err)
	}

	expected := []string{"1.1", "1.2", "2.1", "2.2", "2.3", "4.1", "3"}
	if len(tasks) != len(expected) {
		t.Fatalf("got %d tasks %v, want %d tasks %v", len(tasks), tasks, len(expected), expected)
	}
	for i, tid := range expected {
		if tasks[i] != tid {
			t.Errorf("tasks[%d] = %q, want %q", i, tasks[i], tid)
		}
	}
}

func TestIsTaskComplete(t *testing.T) {
	path := testdataPath("tasks.md")

	tests := []struct {
		taskID   string
		expected bool
	}{
		{"1.1", true},
		{"1.2", false},
		{"2.1", false},
		{"2.2", true},
		{"2.3", false},
		{"3", true},
		{"4.1", false},
	}

	for _, tc := range tests {
		complete, err := IsTaskComplete(path, tc.taskID)
		if err != nil {
			t.Fatalf("IsTaskComplete(%q) error: %v", tc.taskID, err)
		}
		if complete != tc.expected {
			t.Errorf("IsTaskComplete(%q) = %v, want %v", tc.taskID, complete, tc.expected)
		}
	}
}

func TestIsTaskCompleteBoundaryMatching(t *testing.T) {
	path := testdataPath("tasks.md")

	// Task "3" should NOT match the "3" in "2.3"
	complete, err := IsTaskComplete(path, "3")
	if err != nil {
		t.Fatalf("IsTaskComplete('3') error: %v", err)
	}
	if !complete {
		t.Error("IsTaskComplete('3') = false, want true (task 3 is marked [x])")
	}

	// Task "2.3" should NOT be complete
	complete, err = IsTaskComplete(path, "2.3")
	if err != nil {
		t.Fatalf("IsTaskComplete('2.3') error: %v", err)
	}
	if complete {
		t.Error("IsTaskComplete('2.3') = true, want false")
	}
}

func TestGetTaskDescription(t *testing.T) {
	path := testdataPath("tasks.md")

	tests := []struct {
		taskID   string
		expected string
	}{
		{"1.1", "Initialize project with TypeScript config"},
		{"1.2", "Set up database schema"},
		{"2.1", "Build the API layer"},
		{"3", "Checkpoint - basic setup complete"},
		{"4.1", "Unit tests"},
	}

	for _, tc := range tests {
		desc, err := GetTaskDescription(path, tc.taskID)
		if err != nil {
			t.Fatalf("GetTaskDescription(%q) error: %v", tc.taskID, err)
		}
		if desc != tc.expected {
			t.Errorf("GetTaskDescription(%q) = %q, want %q", tc.taskID, desc, tc.expected)
		}
	}
}

func TestCountTasks(t *testing.T) {
	path := testdataPath("tasks.md")

	total, err := CountTotalTasks(path)
	if err != nil {
		t.Fatalf("CountTotalTasks error: %v", err)
	}
	if total != 7 {
		t.Errorf("CountTotalTasks = %d, want 7", total)
	}

	completed, err := CountCompletedTasks(path)
	if err != nil {
		t.Fatalf("CountCompletedTasks error: %v", err)
	}
	if completed != 3 {
		t.Errorf("CountCompletedTasks = %d, want 3", completed)
	}

	incomplete, err := CountIncompleteTasks(path)
	if err != nil {
		t.Fatalf("CountIncompleteTasks error: %v", err)
	}
	if incomplete != 4 {
		t.Errorf("CountIncompleteTasks = %d, want 4", incomplete)
	}
}
