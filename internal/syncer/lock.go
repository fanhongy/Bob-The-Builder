package syncer

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultLockFile = ".ralph-merge-lock"

// MergeLock provides file-based locking with PID tracking.
type MergeLock struct {
	LockFile string
}

// NewMergeLock creates a MergeLock with the default lock file path.
func NewMergeLock() *MergeLock {
	return &MergeLock{LockFile: defaultLockFile}
}

// Acquire takes the merge lock, waiting up to 60s if held by a living process.
// If the holder is dead, the lock is stolen immediately.
func (ml *MergeLock) Acquire() error {
	maxWait := 60
	waited := 0

	for {
		data, err := os.ReadFile(ml.LockFile)
		if err != nil {
			// Lock file does not exist or cannot be read
			break
		}

		// Parse pid:timestamp
		parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
		if len(parts) >= 1 {
			pid, parseErr := strconv.Atoi(parts[0])
			if parseErr == nil && pid > 0 {
				// Check if the holder process is alive
				if !isProcessAlive(pid) {
					// Holder is dead, steal the lock
					os.Remove(ml.LockFile)
					break
				}
			}
		}

		if waited >= maxWait {
			// Force release after max wait
			os.Remove(ml.LockFile)
			break
		}

		time.Sleep(1 * time.Second)
		waited++
	}

	// Write our PID and timestamp
	content := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().Unix())
	return os.WriteFile(ml.LockFile, []byte(content), 0644)
}

// Release removes the lock file.
func (ml *MergeLock) Release() error {
	return os.Remove(ml.LockFile)
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Signal 0 checks existence.
	err = proc.Signal(os.Signal(nil))
	// If err is nil, process is alive. If it's a permission error, also alive.
	if err == nil {
		return true
	}
	// "operation not permitted" means it exists but we lack permission
	if strings.Contains(err.Error(), "operation not permitted") {
		return true
	}
	return false
}
