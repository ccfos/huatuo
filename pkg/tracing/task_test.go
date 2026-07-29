// Copyright 2026 The HuaTuo Authors
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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearTaskCache() {
	taskLifeTmpCache.Range(func(key, value any) bool {
		taskLifeTmpCache.Delete(key)
		return true
	})
}

func createExecutableScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, name)
	if err := os.WriteFile(scriptPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error=%v", scriptPath, err)
	}
	if err := os.Chmod(scriptPath, 0o700); err != nil {
		t.Fatalf("Chmod(%q) error=%v", scriptPath, err)
	}
	return scriptPath
}

func waitTaskFinal(taskID string, timeout time.Duration) *TaskResult {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r := Result(taskID)
		if r.TaskStatus == StatusCompleted || r.TaskStatus == StatusFailed || r.TaskStatus == StatusNotExist {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	return Result(taskID)
}

func TestAllocTaskID(t *testing.T) {
	id, err := AllocTaskID()
	if err != nil {
		t.Fatalf("AllocTaskID() error=%v", err)
	}
	if len(id) != 16 {
		t.Errorf("AllocTaskID length=%d, want 16", len(id))
		return
	}
	for _, ch := range id {
		isDigit := ch >= '0' && ch <= '9'
		isLower := ch >= 'a' && ch <= 'z'
		isUpper := ch >= 'A' && ch <= 'Z'
		if !isDigit && !isLower && !isUpper {
			t.Errorf("AllocTaskID contains invalid char %q", ch)
			return
		}
	}
}

func TestNewTaskWithIDIsIdempotent(t *testing.T) {
	clearTaskCache()
	t.Cleanup(clearTaskCache)

	const taskID = "job-idempotent-2026"
	if _, err := NewTaskWithID(taskID, "missing-profiler", time.Second, TaskStorageStdout, nil); err != nil {
		t.Fatalf("first NewTaskWithID() error=%v", err)
	}
	first, ok := taskLifeTmpCache.Load(taskID)
	if !ok {
		t.Fatal("first NewTaskWithID() did not store task")
	}
	if _, err := NewTaskWithID(taskID, "different-profiler", time.Second, TaskStorageStdout, nil); err != nil {
		t.Fatalf("second NewTaskWithID() error=%v", err)
	}
	second, ok := taskLifeTmpCache.Load(taskID)
	if !ok || first != second {
		t.Fatal("second NewTaskWithID() replaced the existing task")
	}
	if result := waitTaskFinal(taskID, time.Second); result.TaskStatus != StatusFailed {
		t.Fatalf("idempotent task status=%s, want failed", result.TaskStatus)
	}
}

func TestNewTaskWithIDLimitAllowsRetryButRejectsNewTask(t *testing.T) {
	clearTaskCache()
	t.Cleanup(clearTaskCache)
	existing := &task{status: StatusPending}
	taskLifeTmpCache.Store("existing-2026", existing)

	if _, err := NewTaskWithIDLimit(
		"existing-2026", "profiler", time.Second, TaskStorageStdout, nil, 1,
	); err != nil {
		t.Fatalf("retry existing task error=%v", err)
	}
	if _, err := NewTaskWithIDLimit(
		"new-2026", "profiler", time.Second, TaskStorageStdout, nil, 1,
	); !errors.Is(err, ErrTaskLimitExceeded) {
		t.Fatalf("new task error=%v, want ErrTaskLimitExceeded", err)
	}
}

func TestResultNotFound(t *testing.T) {
	clearTaskCache()

	r := Result("task-20250226")
	if r.TaskStatus != StatusNotExist {
		t.Errorf("Result().TaskStatus=%s, want %s", r.TaskStatus, StatusNotExist)
		return
	}
	if !errors.Is(r.TaskErr, ErrTaskNotFound) {
		t.Errorf("Result().TaskErr=%v, want %v", r.TaskErr, ErrTaskNotFound)
	}
}

func TestNewTaskExecution(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*testing.T) (string, time.Duration, TaskStorageType, []string)
		validate func(*testing.T, *TaskResult)
	}{
		{
			name: "stdout success",
			setup: func(t *testing.T) (string, time.Duration, TaskStorageType, []string) {
				clearTaskCache()
				tmp := t.TempDir()
				origBinDir := TaskBinDir
				TaskBinDir = tmp
				t.Cleanup(func() { TaskBinDir = origBinDir })

				script := createExecutableScript(t, tmp, "ok.sh", "#!/bin/sh\necho huatuo\n")
				return filepath.Base(script), 2 * time.Second, TaskStorageStdout, nil
			},
			validate: func(t *testing.T, r *TaskResult) {
				if r.TaskStatus != StatusCompleted {
					t.Errorf("TaskStatus=%s, want %s", r.TaskStatus, StatusCompleted)
					return
				}
				if !strings.Contains(string(r.TaskData), "huatuo") {
					t.Errorf("TaskData=%q, expected contains %q", string(r.TaskData), "huatuo")
				}
			},
		},
		{
			name: "command timeout",
			setup: func(t *testing.T) (string, time.Duration, TaskStorageType, []string) {
				clearTaskCache()
				tmp := t.TempDir()
				origBinDir := TaskBinDir
				TaskBinDir = tmp
				t.Cleanup(func() { TaskBinDir = origBinDir })

				script := createExecutableScript(t, tmp, "timeout.sh", "#!/bin/sh\nsleep 1\necho done\n")
				return filepath.Base(script), 100 * time.Millisecond, TaskStorageStdout, nil
			},
			validate: func(t *testing.T, r *TaskResult) {
				if r.TaskStatus != StatusFailed {
					t.Errorf("TaskStatus=%s, want %s", r.TaskStatus, StatusFailed)
					return
				}
				if !errors.Is(r.TaskErr, ErrTaskTimeout) {
					t.Errorf("TaskErr=%v, want %v", r.TaskErr, ErrTaskTimeout)
				}
			},
		},
		{
			name: "binary not found",
			setup: func(t *testing.T) (string, time.Duration, TaskStorageType, []string) {
				clearTaskCache()
				tmp := t.TempDir()
				origBinDir := TaskBinDir
				TaskBinDir = tmp
				t.Cleanup(func() { TaskBinDir = origBinDir })
				return "not_exists.sh", 500 * time.Millisecond, TaskStorageStdout, nil
			},
			validate: func(t *testing.T, r *TaskResult) {
				if r.TaskStatus != StatusFailed {
					t.Errorf("TaskStatus=%s, want %s", r.TaskStatus, StatusFailed)
					return
				}
				if r.TaskErr == nil {
					t.Errorf("TaskErr=nil, want non-nil")
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			execBinary, timeout, storageType, execArgs := tests[i].setup(t)
			taskID := NewTask(execBinary, timeout, storageType, execArgs)
			r := waitTaskFinal(taskID, 2*time.Second)
			tests[i].validate(t, r)
		})
	}
}

func TestStopTask(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*testing.T) string
		validate func(*testing.T, string, error)
	}{
		{
			name: "task not found",
			setup: func(t *testing.T) string {
				clearTaskCache()
				return "task-404"
			},
			validate: func(t *testing.T, taskID string, err error) {
				if !errors.Is(err, ErrTaskNotFound) {
					t.Errorf("StopTask(%q) err=%v, want %v", taskID, err, ErrTaskNotFound)
				}
			},
		},
		{
			name: "stop running task",
			setup: func(t *testing.T) string {
				clearTaskCache()
				tmp := t.TempDir()
				origBinDir := TaskBinDir
				TaskBinDir = tmp
				t.Cleanup(func() { TaskBinDir = origBinDir })

				script := createExecutableScript(t, tmp, "long.sh", "#!/bin/sh\nsleep 2\necho done\n")
				return NewTask(filepath.Base(script), 3*time.Second, TaskStorageStdout, nil)
			},
			validate: func(t *testing.T, taskID string, err error) {
				if err != nil {
					t.Errorf("StopTask(%q) err=%v, want nil", taskID, err)
					return
				}
				r := waitTaskFinal(taskID, 3*time.Second)
				if r.TaskStatus == StatusRunning {
					t.Errorf("Result(%q).TaskStatus=%s, want not running", taskID, r.TaskStatus)
				}
			},
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			taskID := tests[i].setup(t)
			if taskID == "" {
				t.Fatalf("setup returned empty task id")
			}
			err := StopTask(taskID)
			tests[i].validate(t, taskID, err)
		})
	}
}

func TestRunningTaskCount(t *testing.T) {
	clearTaskCache()

	taskLifeTmpCache.Store("task-running", &task{status: StatusRunning})
	taskLifeTmpCache.Store("task-pending", &task{status: StatusPending})
	taskLifeTmpCache.Store("task-completed", &task{status: StatusCompleted})

	got := RunningTaskCount()
	if got != 1 {
		t.Errorf("RunningTaskCount()=%d, want 1", got)
	}
}

func TestListTasks(t *testing.T) {
	clearTaskCache()
	t.Cleanup(clearTaskCache)

	taskLifeTmpCache.Store("task-z", &task{
		id:         "task-z",
		execBinary: "mem",
		status:     StatusFailed,
	})
	taskLifeTmpCache.Store("task-a", &task{
		id:         "task-a",
		execBinary: "cpu",
		status:     StatusRunning,
	})

	got := ListTasks()
	if len(got) != 2 {
		t.Fatalf("ListTasks() len=%d, want 2", len(got))
	}

	want := []TaskInfo{
		{TaskID: "task-a", TracerName: "cpu", Status: StatusRunning},
		{TaskID: "task-z", TracerName: "mem", Status: StatusFailed},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListTasks()[%d]=%+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestResultReturnsDataCopy(t *testing.T) {
	clearTaskCache()
	t.Cleanup(clearTaskCache)

	taskLifeTmpCache.Store("task-copy", &task{
		status:     StatusCompleted,
		stdoutData: []byte("output"),
	})

	result := Result("task-copy")
	result.TaskData[0] = 'X'

	if got := string(Result("task-copy").TaskData); got != "output" {
		t.Fatalf("Result().TaskData=%q, want %q", got, "output")
	}
}

func TestSetDeadlineDefault(t *testing.T) {
	task := &task{}
	before := time.Now()
	setDeadlineDefault(task)
	if !task.deadlineTime.After(before) {
		t.Errorf("deadlineTime=%v should be after %v", task.deadlineTime, before)
	}
}
