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

//go:build integration

package bpfmock

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskOnHighCPU(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc is required for the trigger helper test")
	}
	includeDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	const source = `
#define _GNU_SOURCE
#include "task.h"
#include <stdarg.h>

int __wrap_sched_getcpu(void) { return 2048; }
int __wrap_sched_setaffinity(pid_t pid, size_t size, const cpu_set_t *mask)
{
    (void)pid;
    if (size < CPU_ALLOC_SIZE(2049) || !CPU_ISSET_S(2048, size, mask) || CPU_COUNT_S(size, mask) != 1) {
        errno = EINVAL;
        return -1;
    }
    return 0;
}
int __wrap_ioctl(int fd, unsigned long command, ...)
{
    va_list args;
    const int *request;
    (void)fd; (void)command;
    va_start(args, command);
    request = va_arg(args, const int *);
    va_end(args);
    if (!request || *request != 123) {
        errno = EINVAL;
        return -1;
    }
    return 0;
}
int main(void)
{
    const int request = 123;
    struct mock_task_options options = {.scenario="test", .pin_cpu=1};
    if (set_mock_task_name()) return 1;
    if (mock_task_trigger(-1, 0, &request, &options)) return 1;
    options.threaded=1;
    return mock_task_trigger(-1, 0, &request, &options);
}
`
	path := filepath.Join(workspace, "task.c")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(workspace, "task")
	cmd := exec.Command("gcc", "-Wall", "-Wextra", "-Werror", "-pthread", "-I", includeDir, path,
		"-Wl,--wrap=sched_getcpu", "-Wl,--wrap=sched_setaffinity", "-Wl,--wrap=ioctl", "-o", binary)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build task test: %v: %s", err, output)
	}
	var previousComm string
	for range 2 {
		output, err := exec.Command(binary).CombinedOutput()
		if err != nil {
			t.Fatalf("high CPU trigger: %v: %s", err, output)
		}
		var main, thread struct {
			PID, TID, CPU int
			Comm          string
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		if err := decoder.Decode(&main); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&thread); err != nil {
			t.Fatal(err)
		}
		if main.CPU != 2048 || thread.CPU != 2048 || main.PID != main.TID ||
			thread.PID != main.PID || thread.TID == thread.PID {
			t.Fatalf("incorrect main/worker task identity: %s", output)
		}
		if len(main.Comm) != 15 || !strings.HasPrefix(main.Comm, "mk-") ||
			thread.Comm != main.Comm || main.Comm == previousComm {
			t.Fatalf("random task name was lost or reused: %s", output)
		}
		previousComm = main.Comm
	}
}
