//go:build linux && integration

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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/procfs"
)

func busyTarget(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("yes")
	null, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = null
	if err := cmd.Start(); err != nil {
		_ = null.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if err := null.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", cmd.Process.Pid))
		if err == nil && filepath.Base(executable) == "yes" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("busy target did not exec yes")
		}
		time.Sleep(time.Millisecond)
	}
	return cmd.Process.Pid
}

// Independent parsing is intentionally confined to this accuracy check.
func rawCounters(t *testing.T, pid int) (float64, map[string]uint64) {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data[strings.LastIndex(string(data), ") ")+2:]))
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	system, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	hz, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := strconv.ParseFloat(strings.TrimSpace(string(hz)), 64)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]uint64)
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		key, value, found := strings.Cut(line, ": ")
		if !found {
			t.Fatalf("invalid process I/O line %q", line)
		}
		count, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		counts[key] = count
	}
	return float64(user+system) / ticks, counts
}

func TestReadSampleAgainstProcAndPS(t *testing.T) {
	pid := busyTarget(t)
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		t.Fatal(err)
	}
	proc, err := fs.Proc(pid)
	if err != nil {
		t.Fatal(err)
	}
	// CPU and I/O are bracketed by independent counter reads, not different
	// top/pidstat windows. This avoids comparing unrelated intervals.
	beforeCPU, beforeIO := rawCounters(t, pid)
	s, err := readSample(context.Background(), proc)
	if err != nil {
		t.Fatal(err)
	}
	afterCPU, afterIO := rawCounters(t, pid)
	if s.cpuSeconds < beforeCPU || s.cpuSeconds > afterCPU {
		t.Fatalf("CPU seconds %v outside kernel counters [%v, %v]", s.cpuSeconds, beforeCPU, afterCPU)
	}
	for name, value := range map[string]uint64{
		"read_bytes": s.io.ReadBytes, "write_bytes": s.io.WriteBytes,
		"rchar": s.io.RChar, "wchar": s.io.WChar,
	} {
		if value < beforeIO[name] || value > afterIO[name] {
			t.Fatalf("%s = %d outside kernel counters [%d, %d]", name, value, beforeIO[name], afterIO[name])
		}
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil || int64(len(fds)) != s.fds {
		t.Fatalf("FDs %d, want %d; error = %v", s.fds, len(fds), err)
	}
	rss, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(rss)), 10, 64)
	if err != nil || math.Abs(float64(kb*1024-s.rssBytes)) > float64(os.Getpagesize()) {
		t.Fatalf("RSS %d, ps RSS %d KiB; error = %v", s.rssBytes, kb, err)
	}
	t.Logf("CPU=%gs RSS=%dB FDs=%d I/O=%+v agree with independent proc/ps reads", s.cpuSeconds, s.rssBytes, s.fds, s.io)
}

func TestCaptureWindowAndAbort(t *testing.T) {
	pid := busyTarget(t)
	for _, abort := range []bool{false, true} {
		t.Run(fmt.Sprintf("abort=%t", abort), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			directory := t.TempDir()
			stopCh := make(chan os.Signal, 1)
			done := make(chan error, 1)
			joined := false
			go func() { done <- capture(ctx, options{pid: pid, outputDir: directory}, stopCh) }()
			defer func() {
				cancel()
				if !joined {
					<-done
				}
			}()
			for {
				if _, err := os.Stat(filepath.Join(directory, "ready.json")); err == nil {
					break
				}
				select {
				case err := <-done:
					joined = true
					t.Fatalf("capture failed before ready: %v", err)
				case <-ctx.Done():
					t.Fatal("capture never became ready")
				case <-time.After(time.Millisecond):
				}
			}
			// A resource observation duration, not a readiness delay.
			time.Sleep(500 * time.Millisecond)
			if abort {
				cancel()
			} else {
				stopCh <- syscall.SIGUSR1
			}
			err := <-done
			joined = true
			if (err != nil) != abort {
				t.Fatalf("capture error = %v, want abort=%t", err, abort)
			}
			data, err := os.ReadFile(filepath.Join(directory, "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result captureResult
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Complete == abort || result.Quality.UsableForPeak == abort {
				t.Fatalf("unexpected completeness/quality: %s", data)
			}
			if !abort && (result.CPUSeconds == nil || *result.CPUSeconds <= 0 || result.IO.WChar.Delta == 0) {
				t.Fatalf("busy target's CPU or logical I/O was not captured: %s", data)
			}
			t.Logf("capture result: %s", data)
		})
	}
}

func TestCaptureDeadlineIsFailure(t *testing.T) {
	pid := busyTarget(t)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	directory := t.TempDir()
	if err := capture(ctx, options{pid: pid, outputDir: directory}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture error = %v, want deadline exceeded", err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result captureResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Quality.UsableForPeak || len(result.Errors) == 0 {
		t.Fatalf("deadline became a successful endpoint: %s", data)
	}
}
