package dag

import "fmt"

// CheckCycles implements Kahn's algorithm (topological sort using in-degree)
// to detect circular dependencies in the DAG. Returns an error with
// "circular dependency detected" if not all nodes can be visited.
func CheckCycles(d *DAG) error {
	if d == nil || len(d.Waves) == 0 {
		return nil
	}

	// Build graph: map task ID -> list of dependency IDs
	allTasks := make(map[string][]string)
	for _, wave := range d.Waves {
		for _, task := range wave.Tasks {
			// Filter out self-dependencies
			var deps []string
			for _, dep := range task.Dependencies {
				if dep != task.ID {
					deps = append(deps, dep)
				}
			}
			allTasks[task.ID] = deps
		}
	}

	if len(allTasks) == 0 {
		return nil
	}

	// Compute in-degree for each task
	inDegree := make(map[string]int)
	for id := range allTasks {
		inDegree[id] = 0
	}
	for id, deps := range allTasks {
		_ = id
		for _, dep := range deps {
			if _, exists := inDegree[dep]; exists {
				inDegree[id]++
			}
		}
	}

	// Initialize queue with nodes that have in-degree 0
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++

		// For each task that depends on this node, reduce in-degree
		for id, deps := range allTasks {
			for _, dep := range deps {
				if dep == node {
					inDegree[id]--
					if inDegree[id] == 0 {
						queue = append(queue, id)
					}
				}
			}
		}
	}

	if visited != len(allTasks) {
		return fmt.Errorf("circular dependency detected: %d of %d tasks could not be resolved", len(allTasks)-visited, len(allTasks))
	}

	return nil
}
