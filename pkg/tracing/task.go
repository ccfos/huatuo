// Copyright 2025 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tracing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os/exec"
	"path"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
)

// Status represents the status of a task.
type Status string

const (
	// StatusCompleted represents a task that has finished executing successfully.
	StatusCompleted = "completed"
	// StatusFailed represents a task that encountered an error during execution.
	StatusFailed = "failed"
	// StatusPending represents a task that has been created but has not yet begun execution.
	StatusPending = "pending"
	// StatusRunning represents a task that is currently being executed.
	StatusRunning = "running"
	// StatusNotExist represents a task that has either never been created or has been removed.
	StatusNotExist = "not_exist"
)

var TaskBinDir = "bin"

type TaskStorageType int

const (
	TaskStorageDB TaskStorageType = iota + 1
	TaskStorageStdout
	TaskStorageLocal
	TaskStorageDBJSON // JSON data
)

type TaskResult struct {
	TaskStatus Status
	TaskData   []byte
	TaskErr    error
}

type taskState struct {
	mu           sync.RWMutex
	status       Status
	error        error
	stdoutData   []byte
	deadlineTime time.Time
}

// task represents a unit of work to be executed.
type task struct {
	state      taskState
	id         string             // Unique identifier for the task.
	execBinary string             // Path to the executable file to run for this task.
	execArgs   []string           // Arguments to pass to the executable.
	storage    TaskStorageType    // Type of data produced by the task.
	cancelFunc context.CancelFunc // Written once before goroutine starts; safe to read without lock.
}

var (
	taskLifeTmpCache sync.Map
	// ErrTaskNotFound Error returned when a task is not found.
	ErrTaskNotFound = errors.New("task not found")
	// ErrTaskTimeout Error returned when a task times out.
	ErrTaskTimeout = errors.New("task timeout")
	// ErrTaskCanceled Error returned when a task is canceled.
	ErrTaskCanceled = errors.New("task canceled")
)

func init() {
	go tasksGarbageCollect()
}

func tasksGarbageCollect() {
	ticker := time.NewTicker(time.Second * 10)
	// defer ticker.Stop() no need, handle by goroutine on process exit

	for range ticker.C {
		now := time.Now()
		taskLifeTmpCache.Range(func(key, value any) bool {
			t := value.(*task)
			t.state.mu.RLock()
			status := t.state.status
			deadlineTime := t.state.deadlineTime
			t.state.mu.RUnlock()
			if (status == StatusCompleted || status == StatusFailed) && now.After(deadlineTime) {
				log.Infof("task %s deleted by timeout", key)
				taskLifeTmpCache.Delete(key)
			}
			return true
		})
	}
}

// AllocTaskID returns a fresh random identifier suitable for tasks and tracer
// records.
func AllocTaskID() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 16
	result := make([]byte, length)
	charsetLength := big.NewInt(int64(len(charset)))

	for i := range result {
		num, err := rand.Int(rand.Reader, charsetLength)
		if err != nil {
			panic("Failed to generate random number")
		}
		result[i] = charset[num.Int64()]
	}

	return string(result)
}

// NewTask creates a new task, allocates an ID, and starts it.
func NewTask(execBinary string, timeout time.Duration, storageType TaskStorageType, execArgs []string) string {
	taskID := AllocTaskID()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t := &task{
		id:         taskID,
		cancelFunc: cancel,
		execBinary: execBinary,
		storage:    storageType,
		execArgs:   execArgs,
		state:      taskState{status: StatusPending},
	}
	taskLifeTmpCache.Store(taskID, t)

	binDir := TaskBinDir // capture before goroutine to avoid racing with test teardown
	go runTask(ctx, t, binDir)

	return taskID
}

func runTask(ctx context.Context, t *task, binDir string) {
	t.state.mu.Lock()
	t.state.status = StatusRunning
	t.state.mu.Unlock()
	log.Infof("task %s %s started", t.execBinary, t.id)

	cmd := exec.CommandContext(ctx, path.Join(binDir, t.execBinary), t.execArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		contextErr := ctx.Err()
		t.state.mu.Lock()
		t.state.status = StatusFailed
		if errors.Is(contextErr, context.DeadlineExceeded) {
			t.state.error = ErrTaskTimeout
		} else if errors.Is(contextErr, context.Canceled) {
			t.state.error = ErrTaskCanceled
		} else {
			t.state.error = fmt.Errorf("task error: %s| cmd error: %s", err.Error(), string(output))
		}
		taskErr := t.state.error
		setDeadlineDefault(t)
		t.state.mu.Unlock()
		log.Infof("task %s %s failed: %s", t.execBinary, t.id, taskErr.Error())
		return
	}

	saveTaskOutputByType(t, time.Now(), output)

	t.state.mu.Lock()
	t.state.status = StatusCompleted
	setDeadlineDefault(t)
	t.state.mu.Unlock()
	log.Infof("task %s completed: %s", t.id, fmt.Sprint(t.execBinary, t.execArgs))
}

func saveTaskOutputByType(t *task, startAt time.Time, output []byte) {
	switch t.storage {
	case TaskStorageDB:
		if err := SaveTaskOutputText(&WriteRequest{
			TracerName: t.execBinary,
			TracerID:   t.id,
			TracerTime: startAt,
			TracerData: string(output),
		}); err != nil {
			log.Infof("save task output %s %s failed: %v", t.execBinary, t.id, err)
		}
	case TaskStorageDBJSON:
		if err := SaveTaskOutputJSON(&WriteRequest{
			TracerName: t.execBinary,
			TracerID:   t.id,
			TracerTime: startAt,
			TracerData: string(output),
		}); err != nil {
			log.Infof("save task json output %s %s failed: %v", t.execBinary, t.id, err)
		}
	case TaskStorageStdout:
		t.state.mu.Lock()
		t.state.stdoutData = append(t.state.stdoutData, output...)
		t.state.mu.Unlock()
	case TaskStorageLocal:
		// no-op: output written to local filesystem by the subprocess itself
	default:
		log.Warn("data storage type not supported")
	}
}

func setDeadlineDefault(t *task) {
	t.state.deadlineTime = time.Now().Add(10 * time.Minute)
}

// RunningTaskCount gets the number of running tasks.
func RunningTaskCount() int {
	count := 0
	taskLifeTmpCache.Range(func(key, value any) bool {
		t := value.(*task)
		t.state.mu.RLock()
		if t.state.status == StatusRunning {
			count++
		}
		t.state.mu.RUnlock()
		return true
	})
	return count
}

// Result returns the result of a task given its ID.
func Result(taskID string) *TaskResult {
	taskInterface, ok := taskLifeTmpCache.Load(taskID)
	if !ok {
		return &TaskResult{
			TaskStatus: StatusNotExist,
			TaskErr:    ErrTaskNotFound,
		}
	}

	t := taskInterface.(*task)
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	if t.state.status == StatusFailed || t.state.status == StatusCompleted {
		setDeadlineDefault(t)
	}
	return &TaskResult{
		TaskData:   t.state.stdoutData,
		TaskStatus: t.state.status,
		TaskErr:    t.state.error,
	}
}

// StopTask stops a running task given its ID.
func StopTask(taskID string) error {
	taskAny, ok := taskLifeTmpCache.Load(taskID)
	if !ok {
		return ErrTaskNotFound
	}

	t := taskAny.(*task)
	t.state.mu.RLock()
	running := t.state.status == StatusRunning
	t.state.mu.RUnlock()
	if running {
		t.cancelFunc()
	}
	taskLifeTmpCache.Delete(taskID)
	log.Infof("task %s stopped", t.id)
	return nil
}
