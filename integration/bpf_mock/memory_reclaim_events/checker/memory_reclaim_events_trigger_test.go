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
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReclaimTrigger(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc is required for the trigger contract test")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	wrapper := filepath.Join(workspace, "ioctl.c")
	if err := os.WriteFile(wrapper, []byte(reclaimIOCTLWrapper), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(workspace, "reclaim-trigger")
	build := exec.Command("gcc", "-Wall", "-Wextra", "-Werror", "-pthread", "-I", root,
		filepath.Join(root, "memory_reclaim_events_trigger.c"), wrapper, "-Wl,--wrap=open", "-Wl,--wrap=ioctl", "-Wl,--wrap=fopen", "-o", binary)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	for _, scenario := range []string{"main", "thread"} {
		for _, failure := range []string{"", "ioctl", "proc", "missing-tid", "negative-tid"} {
			t.Run(scenario+"/failure="+failure, func(t *testing.T) {
				cmd := exec.Command(binary, scenario, "/host-proc")
				cmd.Env = append(os.Environ(), "FAIL_STEP="+failure)
				output, err := cmd.Output()
				if failure != "" {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(output) != 0 {
						t.Fatalf("failure published expectations: %v: %s", err, output)
					}
					return
				}
				if err != nil {
					t.Fatalf("trigger: %v", err)
				}
				var expectedRaw triggerOutput
				if err := json.Unmarshal(output, &expectedRaw); err != nil {
					t.Fatal(err)
				}
				expectedTID := uint32(9001)
				if scenario == "thread" {
					expectedTID = 9002
				}
				if expectedRaw.PID != 9001 || expectedRaw.TID != expectedTID ||
					len(expectedRaw.TriggerComm) != 15 || !strings.HasPrefix(expectedRaw.TriggerComm, "mk-") || expectedRaw.ElapsedNS < 20000000 {
					t.Fatalf("incorrect userspace expectations: %+v", expectedRaw)
				}
			})
		}
	}
}

const reclaimIOCTLWrapper = `#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>
#include "memory_reclaim_events_mock.h"

int __real_open(const char *path, int flags, ...);
int __wrap_open(const char *path, int flags, ...)
{
    if (strcmp(path, "/dev/memory_reclaim_events_mock"))
        abort();
    return __real_open("/dev/null", flags);
}
FILE *__real_fopen(const char *path, const char *mode);
FILE *__wrap_fopen(const char *path, const char *mode)
{
    FILE *status;
    if (strcmp(path, "/host-proc/thread-self/status"))
        return __real_fopen(path, mode);
    status = tmpfile();
    if (!status)
        return NULL;
    const char *failure = getenv("FAIL_STEP");
    if (!strcmp(failure, "missing-tid")) {
        fputs("Tgid:\t9001\n", status);
        rewind(status);
        return status;
    }
    fprintf(status, "Tgid:\t%d\nPid:\t%d\n", !strcmp(getenv("FAIL_STEP"), "proc") ? 0 : 9001,
        !strcmp(failure, "negative-tid") ? -1 : syscall(SYS_gettid) == getpid() ? 9001 : 9002);
    rewind(status);
    return status;
}
int __wrap_ioctl(int fd, unsigned long command, ...)
{
    va_list args;
    const struct reclaim_mock_request *request;
    (void)fd;
    va_start(args, command);
    request = va_arg(args, const struct reclaim_mock_request *);
    va_end(args);
    if (command != HUATUO_RECLAIM_MOCK_RUN || _IOC_DIR(command) != _IOC_WRITE ||
        !request || request->scenario != RECLAIM_MOCK_CURRENT_TASK)
        abort();
    if (!strcmp(getenv("FAIL_STEP"), "ioctl")) {
        errno = EIO;
        return -1;
    }
    return usleep(RECLAIM_MOCK_DELAY_MS * 1000);
}
`
