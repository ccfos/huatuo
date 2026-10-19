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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeMemoryTriggerProtocol(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("gcc is required")
	}
	for _, fixture := range []struct {
		feature   string
		scenarios []string
	}{
		{"native_physical_alloc", []string{"mixed"}},
		{"native_physical_usage", []string{"retained", "balanced", "partial", "untracked"}},
		{"native_virtual_alloc", []string{"mixed", "file-only"}},
	} {
		t.Run(fixture.feature, func(t *testing.T) {
			dir := t.TempDir()
			wrapper := filepath.Join(dir, "wrapper.c")
			binary := filepath.Join(dir, "trigger")
			source := fmt.Sprintf(`#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "%s_mock.h"
int __real_open(const char *, int, ...);
FILE *__real_fopen(const char *, const char *);
int __wrap_open(const char *path, int flags, ...) {
 if (strcmp(path, "/dev/%s_mock")) { errno=EINVAL; return -1; }
 if (getenv("FAIL_OPEN")) { errno=ENOENT; return -1; }
 return __real_open("/dev/null", flags);
}
FILE *__wrap_fopen(const char *path, const char *mode) {
 if (!strcmp(path, "/host-proc/thread-self/status")) return __real_fopen(getenv("STATUS_FILE"),mode);
 return __real_fopen(path,mode);
}
int __wrap_ioctl(int fd, unsigned long command, ...) {
 va_list args; const struct %s_request *request;
 va_start(args, command); request=va_arg(args, const struct %s_request *);va_end(args);
 if (command != %s_RUN || _IOC_DIR(command) != _IOC_WRITE || request->scenario != (unsigned int)atoi(getenv("SCENARIO"))) { errno=EINVAL; return -1; }
 if (getenv("FAIL_IOCTL")) { errno=EIO;return -1; }
 return 0;
}
`, fixture.feature, fixture.feature, fixture.feature, fixture.feature, strings.ToUpper(fixture.feature))
			if err := os.WriteFile(wrapper, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			fixtureDir := filepath.Join("..", fixture.feature)
			output, err := exec.Command(gcc, "-Wall", "-Wextra", "-Wno-unused-parameter", "-Werror", "-pthread", "-I", fixtureDir,
				filepath.Join(fixtureDir, fixture.feature+"_trigger.c"), wrapper, "-Wl,--wrap=open", "-Wl,--wrap=ioctl", "-Wl,--wrap=fopen", "-o", binary).CombinedOutput()
			if err != nil {
				t.Fatalf("compile: %v: %s", err, output)
			}
			for index, scenario := range fixture.scenarios {
				for _, caller := range []string{"main", "thread"} {
					t.Run(scenario+"/"+caller, func(t *testing.T) {
						for _, hostProc := range []bool{false, true} {
							t.Run(fmt.Sprintf("host-proc=%t", hostProc), func(t *testing.T) {
								runNativeTrigger(t, binary, dir, scenario, caller, index, hostProc, false)
							})
						}
					})
				}
			}
			t.Run("ioctl failure", func(t *testing.T) { runNativeTrigger(t, binary, dir, fixture.scenarios[0], "main", 0, false, true) })
			for _, args := range [][]string{{"unknown", "main"}, {fixture.scenarios[0], "invalid"}} {
				if out, err := exec.Command(binary, args...).CombinedOutput(); err == nil {
					t.Fatalf("invalid input accepted: %s", out)
				}
			}
		})
	}
}

func runNativeTrigger(t *testing.T, binary, dir, scenario, caller string, index int, hostProc, failIoctl bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{scenario, caller}
	if hostProc {
		args = append(args, "/host-proc")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("SCENARIO=%d", index))
	if failIoctl {
		cmd.Env = append(cmd.Env, "FAIL_IOCTL=1")
	}
	if hostProc {
		tid := 9001
		if caller == "thread" {
			tid = 9002
		}
		status := filepath.Join(dir, "status")
		if err := os.WriteFile(status, []byte(fmt.Sprintf("Tgid:\t9001\nPid:\t%d\n", tid)), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd.Env = append(cmd.Env, "STATUS_FILE="+status)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("no readiness record: %s", stderr.String())
	}
	var task struct {
		PID  int    `json:"pid"`
		TID  int    `json:"tid"`
		Comm string `json:"comm"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if hostProc && (task.PID != 9001 || (caller == "thread" && task.TID != 9002)) {
		t.Fatalf("wrong initial namespace identity: %+v", task)
	}
	if !hostProc && task.PID != cmd.Process.Pid {
		t.Fatalf("unexpected PID: %+v", task)
	}
	if task.TID <= 0 || (caller == "thread") != (task.TID != task.PID) || task.Comm == "" {
		t.Fatalf("invalid task: %+v", task)
	}
	if caller == "thread" && task.Comm != "mock-worker" {
		t.Fatalf("worker name: %s", task.Comm)
	}
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if failIoctl {
		if scanner.Scan() {
			t.Fatalf("failure emitted success: %s", scanner.Text())
		}
		if err := cmd.Wait(); err == nil {
			t.Fatal("ioctl error lost")
		}
		return
	}
	if !scanner.Scan() || scanner.Text() != `{"triggered":true}` {
		t.Fatalf("missing completion: %s", stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("trigger exit: %v: %s", err, stderr.String())
	}
}
