package dag

import (
	"encoding/json"
	"errors"
	"strings"
)

// ParseDAGJSON parses JSON data into a DAG struct. It expects the format:
// {"waves":[{"id":0,"tasks":[{"id":"1.1","description":"...","parent":"1","dependencies":[],"model":"claude-sonnet-4.5"}]}]}
// It also sets the WaveID field on each task based on its containing wave.
func ParseDAGJSON(data []byte) (*DAG, error) {
	var d DAG
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}

	// Set WaveID on each task
	for i := range d.Waves {
		for j := range d.Waves[i].Tasks {
			d.Waves[i].Tasks[j].WaveID = d.Waves[i].ID
		}
	}

	return &d, nil
}

// ExtractJSON extracts a JSON object from raw text that may contain markdown
// fences or surrounding explanation text. It finds all top-level { ... }
// candidates and prefers the one containing a "waves" key.
func ExtractJSON(raw string) ([]byte, error) {
	// Strip markdown fences
	cleaned := raw
	cleaned = stripMarkdownFences(cleaned)

	// Find all top-level JSON object candidates
	var candidates []json.RawMessage
	depth := 0
	start := -1
	for i := 0; i < len(cleaned); i++ {
		ch := cleaned[i]
		if ch == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 && start >= 0 {
				candidate := cleaned[start : i+1]
				// Verify it is valid JSON
				var raw json.RawMessage
				if err := json.Unmarshal([]byte(candidate), &raw); err == nil {
					candidates = append(candidates, raw)
				}
				start = -1
			}
		}
	}

	if len(candidates) == 0 {
		return nil, errors.New("no valid JSON object found in text")
	}

	// Prefer the candidate that has a "waves" key
	for _, c := range candidates {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(c, &m); err == nil {
			if _, ok := m["waves"]; ok {
				return []byte(c), nil
			}
		}
	}

	// Fall back to the largest candidate
	best := candidates[0]
	for _, c := range candidates[1:] {
		if len(c) > len(best) {
			best = c
		}
	}
	return []byte(best), nil
}

// stripMarkdownFences removes ```json and ``` markers from text.
func stripMarkdownFences(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "```json" || trimmed == "```" || trimmed == "```JSON" {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}
