package dag

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fanhongy/Bob-The-Builder/internal/taskfile"
)

// BuildFallbackDAG builds a DAG from task numbering when the planner fails.
// Groups tasks by parent, subtasks within the same parent are sequential,
// different parent groups run in parallel waves. Tasks at the same index
// within their parent group go in the same wave.
func BuildFallbackDAG(taskFilePath string, defaultModel string) (*DAG, error) {
	// Get all leaf tasks
	allTasks, err := taskfile.GetAllLeafTasks(taskFilePath)
	if err != nil {
		return nil, err
	}

	// Filter to incomplete tasks only
	var incomplete []string
	for _, tid := range allTasks {
		complete, err := taskfile.IsTaskComplete(taskFilePath, tid)
		if err != nil {
			return nil, err
		}
		if !complete {
			incomplete = append(incomplete, tid)
		}
	}

	if len(incomplete) == 0 {
		return &DAG{Waves: []Wave{}}, nil
	}

	// Group by parent
	groups := make(map[string][]string)
	for _, tid := range incomplete {
		parent := getParentID(tid)
		groups[parent] = append(groups[parent], tid)
	}

	// Sort subtasks within each group numerically
	for parent := range groups {
		sort.Slice(groups[parent], func(i, j int) bool {
			return compareTaskIDs(groups[parent][i], groups[parent][j]) < 0
		})
	}

	// Sort parent keys
	parentKeys := make([]string, 0, len(groups))
	for parent := range groups {
		parentKeys = append(parentKeys, parent)
	}
	sort.Slice(parentKeys, func(i, j int) bool {
		return compareTaskIDs(parentKeys[i], parentKeys[j]) < 0
	})

	// Determine max depth across all groups
	maxDepth := 0
	for _, tasks := range groups {
		if len(tasks) > maxDepth {
			maxDepth = len(tasks)
		}
	}

	// Build waves: tasks at the same depth position within their parent group
	// can run in parallel
	var waves []Wave
	waveID := 0
	for depthIdx := 0; depthIdx < maxDepth; depthIdx++ {
		var waveTasks []Task
		for _, parent := range parentKeys {
			subtasks := groups[parent]
			if depthIdx >= len(subtasks) {
				continue
			}
			tid := subtasks[depthIdx]
			var deps []string
			if depthIdx > 0 {
				deps = []string{subtasks[depthIdx-1]}
			}

			desc, _ := taskfile.GetTaskDescription(taskFilePath, tid)
			waveTasks = append(waveTasks, Task{
				ID:           tid,
				Description:  desc,
				Parent:       parent,
				Dependencies: deps,
				Model:        defaultModel,
				WaveID:       waveID,
			})
		}
		if len(waveTasks) > 0 {
			waves = append(waves, Wave{
				ID:    waveID,
				Tasks: waveTasks,
			})
			waveID++
		}
	}

	return &DAG{Waves: waves}, nil
}

// getParentID extracts the parent portion of a task ID.
// For "2.3" returns "2", for "10" returns "10".
func getParentID(taskID string) string {
	parts := strings.SplitN(taskID, ".", 2)
	return parts[0]
}

// compareTaskIDs compares two task IDs numerically by their parts.
func compareTaskIDs(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var va, vb int
		if i < len(partsA) {
			va, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			vb, _ = strconv.Atoi(partsB[i])
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}
