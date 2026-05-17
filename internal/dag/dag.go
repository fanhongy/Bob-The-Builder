package dag

// Task represents a single unit of work in the DAG.
type Task struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Parent       string   `json:"parent"`
	Dependencies []string `json:"dependencies"`
	Model        string   `json:"model"`
	WaveID       int      // set after parsing
}

// Wave represents a group of tasks that can execute in parallel.
type Wave struct {
	ID    int    `json:"id"`
	Tasks []Task `json:"tasks"`
}

// DAG represents the full directed acyclic graph of task execution.
type DAG struct {
	Waves []Wave `json:"waves"`
}

// AllTaskIDs returns all task IDs in the DAG, ordered by wave then by position within wave.
func (d *DAG) AllTaskIDs() []string {
	var ids []string
	for _, wave := range d.Waves {
		for _, task := range wave.Tasks {
			ids = append(ids, task.ID)
		}
	}
	return ids
}

// GetTask returns a pointer to the task with the given ID, or nil if not found.
func (d *DAG) GetTask(id string) *Task {
	for i := range d.Waves {
		for j := range d.Waves[i].Tasks {
			if d.Waves[i].Tasks[j].ID == id {
				return &d.Waves[i].Tasks[j]
			}
		}
	}
	return nil
}

// GetTaskWave returns the wave ID for the given task, or -1 if not found.
func (d *DAG) GetTaskWave(id string) int {
	for _, wave := range d.Waves {
		for _, task := range wave.Tasks {
			if task.ID == id {
				return wave.ID
			}
		}
	}
	return -1
}

// GetTaskDependencies returns the dependencies for the given task, or nil if not found.
func (d *DAG) GetTaskDependencies(id string) []string {
	t := d.GetTask(id)
	if t == nil {
		return nil
	}
	return t.Dependencies
}

// TaskCount returns the total number of tasks in the DAG.
func (d *DAG) TaskCount() int {
	count := 0
	for _, wave := range d.Waves {
		count += len(wave.Tasks)
	}
	return count
}

// WaveCount returns the number of waves in the DAG.
func (d *DAG) WaveCount() int {
	return len(d.Waves)
}
