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
	"os"
	"os/exec"
	"path/filepath"
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
    (void)fd; (void)command;
    return 0;
}
int main(void)
{
    return mock_task_trigger(-1, 0, 0, 0) || mock_task_trigger(-1, 0, 0, 1);
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
	if output, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("high CPU trigger: %v: %s", err, output)
	}
}
