package worker

import (
	"sync"
	"time"
)

// TaskStatus represents the execution state of a task.
type TaskStatus int

const (
	StatusPending TaskStatus = iota
	StatusReady
	StatusRunning
	StatusCompleted
	StatusFailed
	StatusSkipped
	StatusSynced
)

// String returns the string representation of a TaskStatus.
func (s TaskStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusReady:
		return "ready"
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	case StatusSynced:
		return "synced"
	default:
		return "unknown"
	}
}

// TaskInfo holds the runtime metadata for a single task.
type TaskInfo struct {
	Status       TaskStatus
	PID          int
	Retries      int
	WorktreePath string
	LogFile      string
	StartedAt    time.Time
	HeartbeatAt  time.Time
}

// StateManager provides thread-safe access to task state.
type StateManager struct {
	mu    sync.RWMutex
	tasks map[string]*TaskInfo
}

// NewStateManager creates a new StateManager.
func NewStateManager() *StateManager {
	return &StateManager{
		tasks: make(map[string]*TaskInfo),
	}
}

func (sm *StateManager) ensure(id string) *TaskInfo {
	if _, ok := sm.tasks[id]; !ok {
		sm.tasks[id] = &TaskInfo{}
	}
	return sm.tasks[id]
}

// SetStatus sets the status for the given task.
func (sm *StateManager) SetStatus(id string, status TaskStatus) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).Status = status
}

// GetStatus returns the status for the given task.
func (sm *StateManager) GetStatus(id string) TaskStatus {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.Status
	}
	return StatusPending
}

// SetPID sets the process ID for the given task.
func (sm *StateManager) SetPID(id string, pid int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).PID = pid
}

// GetPID returns the process ID for the given task.
func (sm *StateManager) GetPID(id string) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.PID
	}
	return 0
}

// SetRetries sets the retry count for the given task.
func (sm *StateManager) SetRetries(id string, n int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).Retries = n
}

// GetRetries returns the retry count for the given task.
func (sm *StateManager) GetRetries(id string) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.Retries
	}
	return 0
}

// IncrRetries increments the retry count for the given task and returns the new value.
func (sm *StateManager) IncrRetries(id string) int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	info := sm.ensure(id)
	info.Retries++
	return info.Retries
}

// SetWorktreePath sets the worktree path for the given task.
func (sm *StateManager) SetWorktreePath(id string, path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).WorktreePath = path
}

// GetWorktreePath returns the worktree path for the given task.
func (sm *StateManager) GetWorktreePath(id string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.WorktreePath
	}
	return ""
}

// SetLogFile sets the log file path for the given task.
func (sm *StateManager) SetLogFile(id string, path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).LogFile = path
}

// GetLogFile returns the log file path for the given task.
func (sm *StateManager) GetLogFile(id string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.LogFile
	}
	return ""
}

// SetStartedAt sets the start time for the given task.
func (sm *StateManager) SetStartedAt(id string, t time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).StartedAt = t
}

// GetStartedAt returns the start time for the given task.
func (sm *StateManager) GetStartedAt(id string) time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.StartedAt
	}
	return time.Time{}
}

// UpdateHeartbeat updates the heartbeat timestamp for the given task to now.
func (sm *StateManager) UpdateHeartbeat(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ensure(id).HeartbeatAt = time.Now()
}

// GetHeartbeat returns the last heartbeat time for the given task.
func (sm *StateManager) GetHeartbeat(id string) time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if info, ok := sm.tasks[id]; ok {
		return info.HeartbeatAt
	}
	return time.Time{}
}

// GetAllWithStatus returns all task IDs with the given status.
func (sm *StateManager) GetAllWithStatus(status TaskStatus) []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var result []string
	for id, info := range sm.tasks {
		if info.Status == status {
			result = append(result, id)
		}
	}
	return result
}

// CountWithStatus returns the count of tasks with the given status.
func (sm *StateManager) CountWithStatus(status TaskStatus) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	count := 0
	for _, info := range sm.tasks {
		if info.Status == status {
			count++
		}
	}
	return count
}
