package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/fanhongy/Bob-The-Builder/internal/dag"
)

func TestProgressBar(t *testing.T) {
	tests := []struct {
		name      string
		completed int
		total     int
		width     int
		elapsed   time.Duration
		wantFull  string
		wantEmpty string
	}{
		{
			name:      "0%",
			completed: 0,
			total:     10,
			width:     20,
			elapsed:   30 * time.Second,
			wantFull:  "",
			wantEmpty: strings.Repeat("\u2591", 20),
		},
		{
			name:      "50%",
			completed: 5,
			total:     10,
			width:     20,
			elapsed:   60 * time.Second,
			wantFull:  strings.Repeat("\u2588", 10),
			wantEmpty: strings.Repeat("\u2591", 10),
		},
		{
			name:      "100%",
			completed: 10,
			total:     10,
			width:     20,
			elapsed:   120 * time.Second,
			wantFull:  strings.Repeat("\u2588", 20),
			wantEmpty: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProgressBar(tt.completed, tt.total, tt.width, tt.elapsed)
			stripped := stripANSI(result)
			if tt.wantFull != "" && !strings.Contains(result, tt.wantFull) {
				t.Errorf("expected filled blocks in result, got: %q", stripped)
			}
			if tt.wantEmpty != "" && !strings.Contains(result, tt.wantEmpty) {
				t.Errorf("expected empty blocks in result, got: %q", stripped)
			}
			// Verify ratio info present
			if !strings.Contains(stripped, "/") {
				t.Errorf("expected ratio in result, got: %q", stripped)
			}
		})
	}
}

func TestStatusSymbol(t *testing.T) {
	tests := []struct {
		state  string
		expect string
	}{
		{"completed", "\u25cf"},
		{"synced", "\u25cf"},
		{"running", "\u25c9"},
		{"failed", "\u2717"},
		{"pending", "\u25cb"},
		{"skipped", "\u25cb"},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			result := StatusSymbol(tt.state, 0)
			if !strings.Contains(result, tt.expect) {
				t.Errorf("StatusSymbol(%q) = %q, want symbol %q", tt.state, result, tt.expect)
			}
		})
	}
}

func TestModelBadge(t *testing.T) {
	tests := []struct {
		model  string
		expect string
	}{
		{"claude-sonnet-4.5", "sonnet"},
		{"claude-opus-4.6", "opus"},
		{"claude-haiku-3.5", "haiku"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := ModelBadge(tt.model)
			if !strings.Contains(result, tt.expect) {
				t.Errorf("ModelBadge(%q) = %q, want %q", tt.model, result, tt.expect)
			}
		})
	}
}

func TestDAGViewRendering(t *testing.T) {
	// 2-wave DAG
	d := &dag.DAG{
		Waves: []dag.Wave{
			{
				ID: 0,
				Tasks: []dag.Task{
					{ID: "1.1", Description: "First task", Model: "claude-sonnet-4.5"},
				},
			},
			{
				ID: 1,
				Tasks: []dag.Task{
					{ID: "2.1", Description: "Second task A", Model: "claude-opus-4.6"},
					{ID: "2.2", Description: "Second task B", Model: "claude-sonnet-4.5"},
				},
			},
		},
	}

	states := map[string]string{
		"1.1": "completed",
		"2.1": "running",
		"2.2": "pending",
	}

	lines := RenderDAGView(d, states, 1, 15, 120, 0)

	if len(lines) != 15 {
		t.Errorf("expected 15 lines, got %d", len(lines))
	}

	// Should contain wave labels and task IDs
	combined := strings.Join(lines, "\n")
	stripped := stripANSI(combined)

	if !strings.Contains(stripped, "w0") {
		t.Errorf("expected w0 in DAG view")
	}
	if !strings.Contains(stripped, "w1") {
		t.Errorf("expected w1 in DAG view")
	}
	if !strings.Contains(stripped, "1.1") {
		t.Errorf("expected task 1.1 in DAG view")
	}
	if !strings.Contains(stripped, "fork(2)") {
		t.Errorf("expected fork(2) for parallel wave")
	}
}

func TestActivityLog(t *testing.T) {
	al := NewActivityLog(5)

	// Add events
	al.AddEvent("first event")
	al.AddEvent("second event")
	al.AddEvent("third event")

	if al.Len() != 3 {
		t.Errorf("expected 3 entries, got %d", al.Len())
	}

	// Overflow
	al.AddEvent("fourth event")
	al.AddEvent("fifth event")
	al.AddEvent("sixth event")

	if al.Len() != 5 {
		t.Errorf("expected 5 entries after overflow, got %d", al.Len())
	}

	// Render
	lines := al.RenderActivityLog(6, 80)
	if len(lines) != 6 {
		t.Errorf("expected 6 rendered lines, got %d", len(lines))
	}

	// First line should be header
	stripped := stripANSI(lines[0])
	if !strings.Contains(stripped, "ACTIVITY") {
		t.Errorf("expected ACTIVITY header, got: %q", stripped)
	}
}

func TestKeyParsing(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect []KeyType
	}{
		{
			name:   "arrow up",
			input:  []byte{0x1b, '[', 'A'},
			expect: []KeyType{KeyUp},
		},
		{
			name:   "arrow down",
			input:  []byte{0x1b, '[', 'B'},
			expect: []KeyType{KeyDown},
		},
		{
			name:   "arrow right",
			input:  []byte{0x1b, '[', 'C'},
			expect: []KeyType{KeyRight},
		},
		{
			name:   "arrow left",
			input:  []byte{0x1b, '[', 'D'},
			expect: []KeyType{KeyLeft},
		},
		{
			name:   "enter",
			input:  []byte{'\n'},
			expect: []KeyType{KeyEnter},
		},
		{
			name:   "q key",
			input:  []byte{'q'},
			expect: []KeyType{KeyQ},
		},
		{
			name:   "escape alone",
			input:  []byte{0x1b},
			expect: []KeyType{KeyEscape},
		},
		{
			name:   "multiple keys",
			input:  []byte{0x1b, '[', 'A', 0x1b, '[', 'B'},
			expect: []KeyType{KeyUp, KeyDown},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ParseInput(tt.input)
			if len(events) != len(tt.expect) {
				t.Fatalf("expected %d events, got %d", len(tt.expect), len(events))
			}
			for i, ev := range events {
				if ev.Type != tt.expect[i] {
					t.Errorf("event[%d] type = %d, want %d", i, ev.Type, tt.expect[i])
				}
			}
		})
	}
}

func TestTaskMapRendering(t *testing.T) {
	d := &dag.DAG{
		Waves: []dag.Wave{
			{
				ID: 0,
				Tasks: []dag.Task{
					{ID: "1.1", Description: "Task A", Model: "claude-sonnet-4.5"},
					{ID: "1.2", Description: "Task B", Model: "claude-sonnet-4.5"},
				},
			},
			{
				ID: 1,
				Tasks: []dag.Task{
					{ID: "2.1", Description: "Task C", Model: "claude-opus-4.6"},
				},
			},
		},
	}

	states := map[string]string{
		"1.1": "completed",
		"1.2": "running",
		"2.1": "pending",
	}

	lines := RenderTaskMap(d, states, 0, 10, 120)
	if len(lines) != 10 {
		t.Errorf("expected 10 lines, got %d", len(lines))
	}

	combined := strings.Join(lines, "\n")
	stripped := stripANSI(combined)
	if !strings.Contains(stripped, "TASK MAP") {
		t.Errorf("expected TASK MAP header in output")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{0, "0s"},
		{3661 * time.Second, "61m 01s"},
	}

	for _, tt := range tests {
		result := FormatDuration(tt.d)
		if result != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, result, tt.want)
		}
	}
}

func TestWorkersPanel(t *testing.T) {
	workers := []WorkerInfo{
		{TaskID: "1.1", Description: "First task", Model: "claude-sonnet-4.5"},
		{TaskID: "2.1", Description: "Second task", Model: "claude-opus-4.6"},
	}

	lines := RenderWorkersPanel(workers, 0, 5, 1, "executing", 6, 120)
	if len(lines) != 6 {
		t.Errorf("expected 6 lines, got %d", len(lines))
	}

	combined := strings.Join(lines, "\n")
	stripped := stripANSI(combined)
	if !strings.Contains(stripped, "ACTIVE WORKERS") {
		t.Errorf("expected ACTIVE WORKERS header")
	}
	if !strings.Contains(stripped, "1.1") {
		t.Errorf("expected task 1.1 in workers panel")
	}
}
