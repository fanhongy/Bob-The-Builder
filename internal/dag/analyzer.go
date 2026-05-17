package dag

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/fanhongy/Bob-The-Builder/internal/config"
	"github.com/fanhongy/Bob-The-Builder/internal/taskfile"
)

const maxDAGRepairAttempts = 3

// AnalyzeDependencies uses kiro-cli to analyze task dependencies and produce a DAG.
// It shells out to `kiro-cli chat --no-interactive --agent planner --trust-all-tools`
// with a prompt asking it to analyze tasks and produce a DAG as JSON.
// If kiro-cli fails entirely, falls back to BuildFallbackDAG.
func AnalyzeDependencies(taskFile, designFile, reqsFile string, cfg *config.Config) (*DAG, error) {
	// Build context files hint
	var contextFiles string
	if designFile != "" {
		if _, err := os.Stat(designFile); err == nil {
			contextFiles += fmt.Sprintf("Also read %s for implementation context. ", designFile)
		}
	}
	if reqsFile != "" {
		if _, err := os.Stat(reqsFile); err == nil {
			contextFiles += fmt.Sprintf("Also read %s for requirements context. ", reqsFile)
		}
	}

	modelsList := cfg.AvailableModels
	if modelsList == "" {
		modelsList = "claude-sonnet-4.5,claude-opus-4.6"
	}

	// Build steering hint
	var steeringHint string
	if entries, err := os.ReadDir(".kiro/steering"); err == nil && len(entries) > 0 {
		steeringHint = "You have project context in .kiro/steering/ -- read those files first for architecture and conventions context before analyzing dependencies."
	}

	// Step 1: Initial analysis
	prompt := fmt.Sprintf(`%s

Read %s. %s

Analyze ALL incomplete tasks (marked with [ ]) and output a dependency DAG as JSON.

Rules:
1. Include EVERY incomplete leaf task (subtasks like 1.1, 2.3 -- NOT parent headers)
2. Tasks writing to the SAME file cannot be parallel
3. Sequential subtasks within a group (2.1->2.2->2.3) have implicit ordering
4. Setup tasks have no dependencies; tests depend on their implementation

Model assignment -- pick ONLY from: %s
- claude-sonnet-4.5: simple/standard tasks
- claude-opus-4.6: complex/architectural tasks
WARNING: 'claude-opus-4.5' does NOT exist.

Output ONLY valid JSON, no markdown fences, no explanation:
{"waves":[{"id":0,"tasks":[{"id":"1.1","description":"...","parent":"1","dependencies":[],"model":"claude-sonnet-4.5"}]}]}`,
		steeringHint, taskFile, contextFiles, modelsList)

	rawResponse, err := runKiroCLI(prompt)
	if err != nil {
		// kiro-cli failed entirely, use fallback
		return BuildFallbackDAG(taskFile, cfg.DefaultTaskModel)
	}

	jsonBytes, err := ExtractJSON(rawResponse)
	if err != nil {
		return BuildFallbackDAG(taskFile, cfg.DefaultTaskModel)
	}

	d, err := ParseDAGJSON(jsonBytes)
	if err != nil {
		return BuildFallbackDAG(taskFile, cfg.DefaultTaskModel)
	}

	if err := CheckCycles(d); err != nil {
		return BuildFallbackDAG(taskFile, cfg.DefaultTaskModel)
	}

	// Step 2: Repair loop - patch missing tasks
	for attempt := 0; attempt < maxDAGRepairAttempts; attempt++ {
		missingIDs, err := computeMissingTasks(taskFile, d)
		if err != nil || len(missingIDs) == 0 {
			break
		}

		// Build missing detail
		var missingDetail strings.Builder
		for _, mid := range missingIDs {
			desc, _ := taskfile.GetTaskDescription(taskFile, mid)
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&missingDetail, "\n- %s: %s", mid, desc)
		}

		// Build existing IDs string
		existingIDs := d.AllTaskIDs()
		sort.Slice(existingIDs, func(i, j int) bool {
			return compareTaskIDs(existingIDs[i], existingIDs[j]) < 0
		})
		existingIDsStr := strings.Join(existingIDs, ", ")

		patchPrompt := fmt.Sprintf(`Read %s. %s

Your previous DAG missed %d tasks. Produce a DAG fragment for ONLY these:
%s

Already in DAG (do NOT include): %s

Output ONLY valid JSON, no markdown fences:
{"waves":[{"id":<wave>,"tasks":[{"id":"<id>","description":"...","parent":"<parent>","dependencies":[],"model":"claude-sonnet-4.5"}]}]}

Models: pick from %s only. 'claude-opus-4.5' does NOT exist.`,
			taskFile, contextFiles, len(missingIDs), missingDetail.String(), existingIDsStr, modelsList)

		patchResponse, err := runKiroCLI(patchPrompt)
		if err != nil {
			continue
		}

		patchJSON, err := ExtractJSON(patchResponse)
		if err != nil {
			continue
		}

		patchDAG, err := ParseDAGJSON(patchJSON)
		if err != nil {
			continue
		}

		// Merge the patch into existing DAG
		d = mergeDAGs(d, patchDAG)
	}

	return d, nil
}

// runKiroCLI executes kiro-cli chat with the given prompt and returns stdout.
func runKiroCLI(prompt string) (string, error) {
	cmd := exec.Command("kiro-cli", "chat", "--no-interactive", "--agent", "planner", "--trust-all-tools")
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("kiro-cli failed: %w", err)
	}
	return string(out), nil
}

// computeMissingTasks finds incomplete tasks that are not in the DAG.
func computeMissingTasks(taskFile string, d *DAG) ([]string, error) {
	allTasks, err := taskfile.GetAllLeafTasks(taskFile)
	if err != nil {
		return nil, err
	}

	dagTaskIDs := make(map[string]bool)
	for _, id := range d.AllTaskIDs() {
		dagTaskIDs[id] = true
	}

	var missing []string
	for _, tid := range allTasks {
		complete, err := taskfile.IsTaskComplete(taskFile, tid)
		if err != nil {
			return nil, err
		}
		if !complete && !dagTaskIDs[tid] {
			missing = append(missing, tid)
		}
	}
	return missing, nil
}

// mergeDAGs merges a patch DAG into the base DAG. Tasks from the patch are
// inserted into existing waves (by wave id) or new waves are appended.
// Duplicate task IDs are skipped.
func mergeDAGs(base, patch *DAG) *DAG {
	existingIDs := make(map[string]bool)
	waveMap := make(map[int]*Wave)
	maxWaveID := -1

	for i := range base.Waves {
		wave := &base.Waves[i]
		waveMap[wave.ID] = wave
		if wave.ID > maxWaveID {
			maxWaveID = wave.ID
		}
		for _, task := range wave.Tasks {
			existingIDs[task.ID] = true
		}
	}

	for _, pWave := range patch.Waves {
		var newTasks []Task
		for _, t := range pWave.Tasks {
			if !existingIDs[t.ID] {
				newTasks = append(newTasks, t)
				existingIDs[t.ID] = true
			}
		}
		if len(newTasks) == 0 {
			continue
		}
		if w, ok := waveMap[pWave.ID]; ok {
			w.Tasks = append(w.Tasks, newTasks...)
		} else {
			newWID := pWave.ID
			if newWID <= maxWaveID {
				newWID = maxWaveID + 1
			}
			maxWaveID = newWID
			newWave := Wave{ID: newWID, Tasks: newTasks}
			base.Waves = append(base.Waves, newWave)
			waveMap[newWID] = &base.Waves[len(base.Waves)-1]
		}
	}

	// Sort waves by ID and renumber contiguously
	sort.Slice(base.Waves, func(i, j int) bool {
		return base.Waves[i].ID < base.Waves[j].ID
	})
	for i := range base.Waves {
		base.Waves[i].ID = i
		for j := range base.Waves[i].Tasks {
			base.Waves[i].Tasks[j].WaveID = i
		}
	}

	return base
}

// MergeDAGJSON is an exported wrapper for mergeDAGs, useful for testing.
func MergeDAGJSON(baseJSON, patchJSON []byte) ([]byte, error) {
	base, err := ParseDAGJSON(baseJSON)
	if err != nil {
		return nil, err
	}
	patch, err := ParseDAGJSON(patchJSON)
	if err != nil {
		return nil, err
	}
	merged := mergeDAGs(base, patch)
	return json.Marshal(merged)
}
