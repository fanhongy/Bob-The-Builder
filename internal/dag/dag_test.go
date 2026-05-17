package dag

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseDAGJSON(t *testing.T) {
	input := `{
		"waves": [
			{
				"id": 0,
				"tasks": [
					{"id": "1.1", "description": "Setup project", "parent": "1", "dependencies": [], "model": "claude-sonnet-4.5"},
					{"id": "2.1", "description": "Build API", "parent": "2", "dependencies": [], "model": "claude-sonnet-4.5"}
				]
			},
			{
				"id": 1,
				"tasks": [
					{"id": "1.2", "description": "Add database", "parent": "1", "dependencies": ["1.1"], "model": "claude-opus-4.6"},
					{"id": "2.2", "description": "Add auth", "parent": "2", "dependencies": ["2.1"], "model": "claude-sonnet-4.5"}
				]
			}
		]
	}`

	d, err := ParseDAGJSON([]byte(input))
	if err != nil {
		t.Fatalf("ParseDAGJSON failed: %v", err)
	}

	// Verify task count
	if got := d.TaskCount(); got != 4 {
		t.Errorf("TaskCount() = %d, want 4", got)
	}

	// Verify wave count
	if got := d.WaveCount(); got != 2 {
		t.Errorf("WaveCount() = %d, want 2", got)
	}

	// Verify wave IDs are set on tasks
	task11 := d.GetTask("1.1")
	if task11 == nil {
		t.Fatal("GetTask(\"1.1\") returned nil")
	}
	if task11.WaveID != 0 {
		t.Errorf("task 1.1 WaveID = %d, want 0", task11.WaveID)
	}

	task12 := d.GetTask("1.2")
	if task12 == nil {
		t.Fatal("GetTask(\"1.2\") returned nil")
	}
	if task12.WaveID != 1 {
		t.Errorf("task 1.2 WaveID = %d, want 1", task12.WaveID)
	}

	// Verify dependencies
	deps := d.GetTaskDependencies("1.2")
	if len(deps) != 1 || deps[0] != "1.1" {
		t.Errorf("GetTaskDependencies(\"1.2\") = %v, want [\"1.1\"]", deps)
	}

	// Verify GetTaskWave
	if got := d.GetTaskWave("2.2"); got != 1 {
		t.Errorf("GetTaskWave(\"2.2\") = %d, want 1", got)
	}

	// Verify AllTaskIDs order
	ids := d.AllTaskIDs()
	expected := []string{"1.1", "2.1", "1.2", "2.2"}
	if len(ids) != len(expected) {
		t.Fatalf("AllTaskIDs() len = %d, want %d", len(ids), len(expected))
	}
	for i, id := range ids {
		if id != expected[i] {
			t.Errorf("AllTaskIDs()[%d] = %q, want %q", i, id, expected[i])
		}
	}
}

func TestParseDAGJSON_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"invalid json", "{not valid json}"},
		{"array instead of object", "[1,2,3]"},
		{"missing closing brace", `{"waves": [`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDAGJSON([]byte(tc.input))
			if err == nil {
				t.Error("expected error for invalid input, got nil")
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			"raw JSON",
			`{"waves":[{"id":0,"tasks":[{"id":"1.1","description":"test","parent":"1","dependencies":[],"model":"claude-sonnet-4.5"}]}]}`,
		},
		{
			"markdown fenced",
			"```json\n{\"waves\":[{\"id\":0,\"tasks\":[{\"id\":\"1.1\",\"description\":\"test\",\"parent\":\"1\",\"dependencies\":[],\"model\":\"claude-sonnet-4.5\"}]}]}\n```",
		},
		{
			"surrounding explanation",
			"Here is the DAG:\n\n{\"waves\":[{\"id\":0,\"tasks\":[{\"id\":\"1.1\",\"description\":\"test\",\"parent\":\"1\",\"dependencies\":[],\"model\":\"claude-sonnet-4.5\"}]}]}\n\nThis represents the execution plan.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ExtractJSON(tc.input)
			if err != nil {
				t.Fatalf("ExtractJSON failed: %v", err)
			}

			// Parse the result to verify it is valid and has waves
			d, err := ParseDAGJSON(result)
			if err != nil {
				t.Fatalf("result is not valid DAG JSON: %v", err)
			}
			if d.TaskCount() != 1 {
				t.Errorf("expected 1 task, got %d", d.TaskCount())
			}
		})
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	_, err := ExtractJSON("this is just plain text with no json")
	if err == nil {
		t.Error("expected error for input with no JSON, got nil")
	}
}

func TestCheckCycles_NoCycle(t *testing.T) {
	input := `{
		"waves": [
			{"id": 0, "tasks": [
				{"id": "A", "description": "task A", "parent": "1", "dependencies": [], "model": "m"},
				{"id": "B", "description": "task B", "parent": "1", "dependencies": ["A"], "model": "m"}
			]},
			{"id": 1, "tasks": [
				{"id": "C", "description": "task C", "parent": "1", "dependencies": ["B"], "model": "m"}
			]}
		]
	}`

	d, err := ParseDAGJSON([]byte(input))
	if err != nil {
		t.Fatalf("ParseDAGJSON failed: %v", err)
	}

	if err := CheckCycles(d); err != nil {
		t.Errorf("CheckCycles reported cycle in acyclic graph: %v", err)
	}
}

func TestCheckCycles_WithCycle(t *testing.T) {
	// A -> B -> C -> A (circular)
	input := `{
		"waves": [
			{"id": 0, "tasks": [
				{"id": "A", "description": "task A", "parent": "1", "dependencies": ["C"], "model": "m"},
				{"id": "B", "description": "task B", "parent": "1", "dependencies": ["A"], "model": "m"},
				{"id": "C", "description": "task C", "parent": "1", "dependencies": ["B"], "model": "m"}
			]}
		]
	}`

	d, err := ParseDAGJSON([]byte(input))
	if err != nil {
		t.Fatalf("ParseDAGJSON failed: %v", err)
	}

	err = CheckCycles(d)
	if err == nil {
		t.Error("CheckCycles did not detect cycle")
	}
	if err != nil && !contains(err.Error(), "circular dependency detected") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCheckCycles_Empty(t *testing.T) {
	d := &DAG{Waves: []Wave{}}
	if err := CheckCycles(d); err != nil {
		t.Errorf("CheckCycles returned error for empty DAG: %v", err)
	}
}

func TestCheckCycles_Nil(t *testing.T) {
	if err := CheckCycles(nil); err != nil {
		t.Errorf("CheckCycles returned error for nil DAG: %v", err)
	}
}

func TestBuildFallbackDAG(t *testing.T) {
	// Use the testdata/tasks.md fixture from the taskfile package
	_, filename, _, _ := runtime.Caller(0)
	testdataPath := filepath.Join(filepath.Dir(filename), "..", "taskfile", "testdata", "tasks.md")

	d, err := BuildFallbackDAG(testdataPath, "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("BuildFallbackDAG failed: %v", err)
	}

	// The fixture has these incomplete tasks:
	// Parent 1: 1.2 (1.1 is complete)
	// Parent 2: 2.1, 2.3 (2.2 is complete)
	// Parent 4: 4.1
	// Task 3 is complete
	//
	// Groups:
	//   parent "1": [1.2]
	//   parent "2": [2.1, 2.3]
	//   parent "4": [4.1]
	//
	// Wave 0 (depth 0): 1.2, 2.1, 4.1 (first task from each parent group)
	// Wave 1 (depth 1): 2.3 (second task from parent 2 group)

	if d.WaveCount() != 2 {
		t.Fatalf("WaveCount() = %d, want 2", d.WaveCount())
	}

	// Wave 0 should have tasks 1.2, 2.1, 4.1
	wave0Tasks := d.Waves[0].Tasks
	wave0IDs := make(map[string]bool)
	for _, task := range wave0Tasks {
		wave0IDs[task.ID] = true
	}
	for _, expected := range []string{"1.2", "2.1", "4.1"} {
		if !wave0IDs[expected] {
			t.Errorf("Wave 0 missing task %s, got %v", expected, wave0IDs)
		}
	}
	if len(wave0Tasks) != 3 {
		t.Errorf("Wave 0 has %d tasks, want 3", len(wave0Tasks))
	}

	// Wave 1 should have task 2.3
	wave1Tasks := d.Waves[1].Tasks
	if len(wave1Tasks) != 1 {
		t.Fatalf("Wave 1 has %d tasks, want 1", len(wave1Tasks))
	}
	if wave1Tasks[0].ID != "2.3" {
		t.Errorf("Wave 1 task = %s, want 2.3", wave1Tasks[0].ID)
	}

	// Task 2.3 should depend on 2.1 (sequential within parent)
	deps := d.GetTaskDependencies("2.3")
	if len(deps) != 1 || deps[0] != "2.1" {
		t.Errorf("Task 2.3 dependencies = %v, want [\"2.1\"]", deps)
	}

	// Tasks in wave 0 should have no dependencies (first in their group)
	for _, task := range wave0Tasks {
		if len(task.Dependencies) != 0 {
			t.Errorf("Task %s in wave 0 has dependencies %v, want none", task.ID, task.Dependencies)
		}
	}

	// All tasks should have model set
	for _, id := range d.AllTaskIDs() {
		task := d.GetTask(id)
		if task.Model != "claude-sonnet-4.5" {
			t.Errorf("Task %s model = %q, want \"claude-sonnet-4.5\"", id, task.Model)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
