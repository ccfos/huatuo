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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise real trigger binaries without requiring modules or privileged ioctls.
func TestTriggerRequests(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc is required for the trigger contract test")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	wrapper := filepath.Join(workspace, "ioctl.c")
	if err := os.WriteFile(wrapper, []byte(triggerIOCTLWrapper), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(workspace, "oom-trigger")
	build := exec.Command("gcc", "-Wall", "-Wextra", "-Werror", "-I", root,
		filepath.Join(root, "memory_oom_kill_trigger.c"), wrapper,
		"-Wl,--wrap=open", "-Wl,--wrap=ioctl", "-Wl,--wrap=fopen", "-o", binary)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	var previousComm string
	t.Run("API CSS cannot identify a synthetic victim", func(t *testing.T) {
		output, err := exec.Command(binary, "--memory-css", "0x3000", "pointer-chain", "0x1ace").Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || len(output) != 0 {
			t.Fatalf("expected a usage error for synthetic container identity: %v: %s", err, output)
		}
	})
	for _, execution := range []struct {
		name      string
		args      []string
		container bool
	}{
		{name: "host"},
		{name: "host-proc", args: []string{"--host-proc", "/proc"}},
		{name: "container", args: []string{"--host-proc", "/host-proc", "--memory-css", "0x3000"}, container: true},
	} {
		t.Run(execution.name, func(t *testing.T) {
			for number, name := range []string{"same-task", "other-task", "memcg", "other-task-memcg", "null-control", "null-victim", "pointer-chain"} {
				if execution.container && name == "pointer-chain" {
					continue
				}
				t.Run(name, func(t *testing.T) {
					failures := []string{"", "run", "get-css"}
					if execution.container {
						failures = []string{"", "run", "host-proc", "invalid-tgid"}
						if name == "other-task" || name == "other-task-memcg" {
							failures = append(failures, "child-host-proc")
						}
					}
					for _, failure := range failures {
						t.Run("failure="+failure, func(t *testing.T) {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer cancel()
							args := append([]string{}, execution.args...)
							args = append(args, name, "0x1ace", "321")
							cmd := exec.CommandContext(ctx, binary, args...)
							cmd.Env = append(os.Environ(), "EXPECTED_SCENARIO="+strconv.Itoa(number), "FAIL_STEP="+failure,
								"API_CSS="+strconv.FormatBool(execution.container))
							var requestVictimPID bytes.Buffer
							cmd.Stderr = &requestVictimPID
							output, err := cmd.Output()
							if failure != "" {
								var exit *exec.ExitError
								if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(output) != 0 {
									t.Fatalf("failure must not publish expectations: err=%v output=%s", err, output)
								}
								return
							}
							if err != nil {
								t.Fatalf("trigger: %v: %s", err, requestVictimPID.String())
							}
							var expectedRaw triggerOutput
							if err := json.Unmarshal(output, &expectedRaw); err != nil {
								t.Fatalf("identity: %v: %s", err, output)
							}
							expectedTriggerPID, expectedTriggerCSS := uint32(cmd.Process.Pid), uint64(4096)
							if execution.container {
								expectedTriggerPID, expectedTriggerCSS = 9001, 12288
							}
							expectedVictimPID, expectedVictimCSS := expectedTriggerPID, expectedTriggerCSS
							switch name {
							case "other-task", "other-task-memcg":
								if execution.container {
									expectedVictimPID = 9002
								} else {
									if expectedRaw.VictimPID == 0 || expectedRaw.VictimPID == expectedTriggerPID {
										t.Fatalf("expected a separate live victim: %+v", expectedRaw)
									}
									inputPID, err := strconv.ParseUint(requestVictimPID.String(), 10, 32)
									if err != nil {
										t.Fatalf("read victim PID passed to RUN: %v", err)
									}
									expectedVictimPID = uint32(inputPID)
								}
							case "null-victim":
								expectedVictimPID, expectedVictimCSS = 0, 0
							case "pointer-chain":
								expectedVictimPID, expectedVictimCSS = 4242, 8192
							}
							if len(expectedRaw.TriggerComm) != 15 || !strings.HasPrefix(expectedRaw.TriggerComm, "mk-") ||
								expectedRaw.TriggerComm == previousComm || expectedRaw.TriggerPID != expectedTriggerPID ||
								expectedRaw.TriggerCSS != expectedTriggerCSS || expectedRaw.VictimPID != expectedVictimPID ||
								expectedRaw.VictimCSS != expectedVictimCSS {
								t.Fatalf("incorrect userspace PID or CSS source: %+v", expectedRaw)
							}
							previousComm = expectedRaw.TriggerComm
						})
					}
				})
			}
		})
	}
}

const triggerIOCTLWrapper = `#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>
#include "memory_oom_kill_mock.h"

static pid_t trigger_pid;

int __real_open(const char *path, int flags, ...);
int __wrap_open(const char *path, int flags, ...)
{
    if (strcmp(path, "/dev/memory_oom_kill_mock")) {
        errno = ENOENT;
        return -1;
    }
    trigger_pid = getpid();
    return __real_open("/dev/null", flags);
}

FILE *__real_fopen(const char *path, const char *mode);
FILE *__wrap_fopen(const char *path, const char *mode)
{
    const char *failure = getenv("FAIL_STEP");
    FILE *status;
    int tgid;

    if (strcmp(path, "/host-proc/thread-self/status"))
        return __real_fopen(path, mode);
    if (!strcmp(failure, "host-proc") ||
        (!strcmp(failure, "child-host-proc") && getpid() != trigger_pid)) {
        errno = ENOENT;
        return NULL;
    }
    status = tmpfile();
    if (!status)
        return NULL;
    tgid = getpid() == trigger_pid ? 9001 : 9002;
    if (!strcmp(failure, "invalid-tgid"))
        tgid = 0;
    fprintf(status, "Name:\toom-trigger\nTgid:\t%d\nPid:\t%d\n", tgid, tgid);
    rewind(status);
    return status;
}

int __wrap_ioctl(int fd, unsigned long command, ...)
{
    static int css_queried;
    const char *failure = getenv("FAIL_STEP");
    int api_css = !strcmp(getenv("API_CSS"), "true");
    unsigned expected = strtoul(getenv("EXPECTED_SCENARIO"), NULL, 10);
    va_list args;
    void *argument;
    (void)fd;
    va_start(args, command);
    argument = va_arg(args, void *);
    va_end(args);
    if (!argument)
        abort();
    if (command == HUATUO_OOM_MOCK_GET_CSS) {
        struct oom_mock_css_query *query = argument;
        if (api_css || css_queried || query->scenario != expected ||
            ((expected == OOM_MOCK_OTHER_TASK || expected == OOM_MOCK_OTHER_TASK_MEMCG) && !query->victim_pid))
            abort();
        if (!strcmp(failure, "get-css")) {
            errno = EIO;
            return -1;
        }
        query->trigger_css = 4096;
        query->victim_css = expected == OOM_MOCK_NULL_VICTIM ? 0 :
            expected == OOM_MOCK_POINTER_CHAIN ? 8192 : 4096;
        css_queried = 1;
        return 0;
    }
    if (command != HUATUO_OOM_MOCK_RUN || _IOC_DIR(command) != _IOC_WRITE)
        abort();
    const struct oom_mock_request *request = argument;
    if ((!css_queried && !api_css) || request->scenario != expected ||
        request->total_pages != 0x1ace || request->usage_pages != 321)
        abort();
    if ((expected == OOM_MOCK_OTHER_TASK || expected == OOM_MOCK_OTHER_TASK_MEMCG) &&
        (!request->victim_pid || kill(request->victim_pid, 0)))
        abort();
    if (expected == OOM_MOCK_OTHER_TASK || expected == OOM_MOCK_OTHER_TASK_MEMCG)
        fprintf(stderr, "%u", request->victim_pid);
    if (!strcmp(failure, "run")) {
        errno = EIO;
        return -1;
    }
    return 0;
}
`
