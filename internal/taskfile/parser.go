package taskfile

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// subtaskPattern matches lines like "  - [x] 1.1 description" or "- [ ] 2.3 description"
// Captures: group 1 = parent ID, group 2 = sub part (digit portion after the dot)
var subtaskPattern = regexp.MustCompile(`^[\s]*-\s+\[.\]\s+(\d+)\.(\d+\S*)`)

// topLevelPattern matches lines like "- [x] 3. description" (top-level tasks with integer ID)
var topLevelPattern = regexp.MustCompile(`^-\s+\[.\]\s+(\d+)\.\s`)

// completionPattern builds a regex to check if a task ID is marked complete [x]
func completionPattern(taskID string) *regexp.Regexp {
	return regexp.MustCompile(`\[x\]\s+` + regexp.QuoteMeta(taskID) + `(?:\.?\s)`)
}

// taskLinePattern builds a regex to match a task line (complete or not) for extracting description
func taskLinePattern(taskID string) *regexp.Regexp {
	return regexp.MustCompile(`\[.\]\s+` + regexp.QuoteMeta(taskID) + `(?:\.?\s)(.*)`)
}

// GetAllLeafTasks returns all leaf task IDs from a task file.
// Leaf tasks are: subtask IDs (like 1.1, 2.3) and top-level tasks with no children.
func GetAllLeafTasks(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var subtaskIDs []string
	parentsWithChildren := make(map[string]bool)

	scanner := bufio.NewScanner(file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Pass 1: collect subtask IDs and identify parents that have children
	for _, line := range lines {
		matches := subtaskPattern.FindStringSubmatch(line)
		if matches != nil {
			parentID := matches[1]
			subPart := matches[2]
			// Only treat as subtask if sub_part starts with a digit
			if len(subPart) > 0 && subPart[0] >= '0' && subPart[0] <= '9' {
				fullID := parentID + "." + subPart
				subtaskIDs = append(subtaskIDs, fullID)
				parentsWithChildren[parentID] = true
			}
		}
	}

	// Pass 2: collect top-level tasks that have NO children
	var childlessIDs []string
	for _, line := range lines {
		matches := topLevelPattern.FindStringSubmatch(line)
		if matches != nil {
			tid := matches[1]
			if !parentsWithChildren[tid] {
				childlessIDs = append(childlessIDs, tid)
			}
		}
	}

	// Combine: subtasks first, then childless top-level tasks
	result := make([]string, 0, len(subtaskIDs)+len(childlessIDs))
	result = append(result, subtaskIDs...)
	result = append(result, childlessIDs...)
	return result, nil
}

// IsTaskComplete checks if a specific task is marked as complete [x].
// It handles boundary-aware matching (task "3" does not match "13" or "2.3").
func IsTaskComplete(path, taskID string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	pat := completionPattern(taskID)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		loc := pat.FindStringIndex(line)
		if loc != nil {
			// Verify the ID is not a suffix of a larger number
			prefix := line[:loc[0]]
			trimmed := strings.TrimRight(prefix, " \t")
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] >= '0' && trimmed[len(trimmed)-1] <= '9' {
				continue
			}
			return true, nil
		}
	}
	return false, scanner.Err()
}

// GetTaskDescription returns the description text for a given task ID.
func GetTaskDescription(path, taskID string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	pat := taskLinePattern(taskID)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		loc := pat.FindStringSubmatchIndex(line)
		if loc != nil {
			// Verify boundary: the ID is not a suffix of a larger number
			matchStart := loc[0]
			prefix := line[:matchStart]
			trimmed := strings.TrimRight(prefix, " \t")
			if len(trimmed) > 0 && trimmed[len(trimmed)-1] >= '0' && trimmed[len(trimmed)-1] <= '9' {
				continue
			}
			// Extract the description (capture group 1)
			desc := line[loc[2]:loc[3]]
			return strings.TrimSpace(desc), nil
		}
	}
	return "", scanner.Err()
}

// CountIncompleteTasks counts leaf tasks that are not yet complete.
func CountIncompleteTasks(path string) (int, error) {
	tasks, err := GetAllLeafTasks(path)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, tid := range tasks {
		complete, err := IsTaskComplete(path, tid)
		if err != nil {
			return 0, err
		}
		if !complete {
			count++
		}
	}
	return count, nil
}

// CountCompletedTasks counts leaf tasks that are marked complete.
func CountCompletedTasks(path string) (int, error) {
	tasks, err := GetAllLeafTasks(path)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, tid := range tasks {
		complete, err := IsTaskComplete(path, tid)
		if err != nil {
			return 0, err
		}
		if complete {
			count++
		}
	}
	return count, nil
}

// CountTotalTasks counts total leaf tasks in the file.
func CountTotalTasks(path string) (int, error) {
	tasks, err := GetAllLeafTasks(path)
	if err != nil {
		return 0, err
	}
	return len(tasks), nil
}
