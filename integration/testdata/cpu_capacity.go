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
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/stats"
	v2 "huatuo-bamai/internal/cgroups/v2"
	"huatuo-bamai/internal/utils/cpuutil"

	"golang.org/x/sys/unix"
)

type skipError string

func (e skipError) Error() string { return string(e) }

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "--burn" {
		err = burn()
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = runCPUCapacity(ctx)
		stop()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		var skipped skipError
		if errors.As(err, &skipped) {
			os.Exit(77)
		}
		os.Exit(1)
	}
}

func runCPUCapacity(ctx context.Context) (retErr error) {
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) {
		return errors.New("usage: cpu-capacity ABSOLUTE_DELEGATED_CGROUP_ROOT")
	}
	root, err := filepath.EvalSymlinks(os.Args[1])
	if err != nil {
		return skipError(fmt.Sprintf("resolve delegated cgroup root: %v", err))
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(root, &filesystem); err != nil {
		return fmt.Errorf("inspect delegated cgroup filesystem: %w", err)
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return skipError("delegated root is not on a real cgroup v2 filesystem")
	}
	if filesystem.Flags&unix.ST_RDONLY != 0 {
		return skipError("delegated cgroup v2 mount is read-only")
	}
	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		return skipError(fmt.Sprintf("read delegated controllers: %v", err))
	}
	for _, controller := range []string{"cpu", "cpuset"} {
		if !slices.Contains(strings.Fields(string(controllers)), controller) {
			return skipError("delegated root must already enable cpu and cpuset in cgroup.subtree_control")
		}
	}
	online, err := os.ReadFile(cpuutil.SystemCPUOnlinePath)
	if err != nil {
		return fmt.Errorf("read online CPUs: %w", err)
	}
	// This is a real delegated mount subtree, not a regular-file fixture.
	paths.RootfsDefaultPath = root
	manager := &v2.CgroupV2{}
	capacity, err := manager.CpuCapacity("", string(online))
	if err != nil {
		return fmt.Errorf("read delegated root capacity: %w", err)
	}
	if capacity.Cores < 1 {
		return skipError("delegated root needs at least one visible CPU of capacity")
	}
	owned, err := os.MkdirTemp(root, "huatuo-cpu-capacity-")
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, unix.EROFS) {
			return skipError(fmt.Sprintf("create owned subtree in delegated root: %v", err))
		}
		return fmt.Errorf("create owned cgroup subtree: %w", err)
	}
	directories := []string{owned}
	defer func() {
		for i := len(directories) - 1; i >= 0; i-- {
			if err := os.Remove(directories[i]); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove owned cgroup %q: %w", directories[i], err))
			}
		}
	}()
	if err := writeControl(owned, "cpu.max", "100000 100000"); err != nil {
		return err
	}
	if err := writeControl(owned, "cgroup.subtree_control", "+cpu +cpuset"); err != nil {
		return err
	}
	parent := filepath.Join(owned, "parent")
	child := filepath.Join(parent, "child")
	sibling := filepath.Join(parent, "sibling")
	for _, dir := range []string{parent, child, sibling} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return fmt.Errorf("create owned cgroup %q: %w", dir, err)
		}
		directories = append(directories, dir)
		if dir == parent {
			if err := writeControl(parent, "cpu.max", "50000 100000"); err != nil {
				return err
			}
			if err := writeControl(parent, "cgroup.subtree_control", "+cpu +cpuset"); err != nil {
				return err
			}
		}
	}
	check := func(path string, want float64) (*stats.CpuCapacity, error) {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		got, err := manager.CpuCapacity(relative, string(online))
		if err != nil {
			return nil, fmt.Errorf("read capacity of %q: %w", relative, err)
		}
		if math.Abs(got.Cores-want) > 1e-9 {
			return nil, fmt.Errorf("capacity of %q: got %g CPUs, want %g", relative, got.Cores, want)
		}
		return got, nil
	}
	initial, err := check(child, 0.5)
	if err != nil {
		return err
	}
	if _, err := check(sibling, 0.5); err != nil {
		return err
	}
	for _, limit := range []struct {
		path, value string
		want        float64
	}{{child, "25000 100000", 0.25}, {sibling, "20000 50000", 0.4}} {
		if err := writeControl(limit.path, "cpu.max", limit.value); err != nil {
			return err
		}
		if _, err := check(limit.path, limit.want); err != nil {
			return err
		}
		if err := writeControl(limit.path, "cpu.max", "max 100000"); err != nil {
			return err
		}
	}
	for _, limit := range []struct {
		value string
		want  float64
	}{{"25000 100000", 0.25}, {"50000 100000", 0.5}, {"25000 50000", 0.5}} {
		if err := writeControl(parent, "cpu.max", limit.value); err != nil {
			return err
		}
		resized, err := check(child, limit.want)
		if err != nil {
			return err
		}
		if resized.ConfigID == initial.ConfigID {
			return fmt.Errorf("parent resize to %q did not change child ConfigID", limit.value)
		}
		if _, err := check(sibling, limit.want); err != nil {
			return err
		}
		initial = resized
	}
	if err := writeControl(parent, "cpu.max", "50000 100000"); err != nil {
		return err
	}
	initial, err = check(child, 0.5)
	if err != nil {
		return err
	}
	effective, err := os.ReadFile(filepath.Join(parent, "cpuset.cpus.effective"))
	if err != nil {
		return fmt.Errorf("read inherited effective cpuset: %w", err)
	}
	ids := strings.FieldsFunc(strings.TrimSpace(string(effective)), func(r rune) bool { return r == ',' || r == '-' })
	if len(ids) == 0 {
		return errors.New("owned parent has no effective CPUs")
	}
	if strings.TrimSpace(string(effective)) != ids[0] {
		if err := writeControl(parent, "cpuset.cpus", ids[0]); err != nil {
			return err
		}
		resized, err := check(child, 0.5)
		if err != nil {
			return err
		}
		if resized.ConfigID == initial.ConfigID {
			return errors.New("ancestor cpuset resize did not change child ConfigID")
		}
	} else {
		fmt.Println("[SKIP] cpuset resize: delegated tree has only one effective CPU")
	}
	fmt.Println("real hierarchy: shared ancestor ceiling, leaf limits, periods and resize ConfigID verified")
	return verifySharedBudget(ctx, root, parent, []string{child, sibling}, manager)
}

func writeControl(group, name, value string) error {
	path := filepath.Join(group, name)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open owned cgroup control %q: %w", path, err)
	}
	_, writeErr := io.WriteString(file, value+"\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write owned cgroup control %q: %w", path, err)
	}
	return nil
}

func verifySharedBudget(ctx context.Context, root, parent string, leaves []string, manager *v2.CgroupV2) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var commands []*exec.Cmd
	var gates []*os.File
	defer func() {
		for _, cmd := range commands {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}
		for _, gate := range gates {
			_ = gate.Close()
		}
	}()
	for _, leaf := range leaves {
		reader, writer, err := os.Pipe()
		if err != nil {
			return fmt.Errorf("create CPU worker gate: %w", err)
		}
		gates = append(gates, writer)
		cmd := exec.CommandContext(ctx, os.Args[0], "--burn")
		cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
		cmd.Stdin = reader
		cmd.Stderr = os.Stderr
		err = cmd.Start()
		_ = reader.Close()
		if err != nil {
			return fmt.Errorf("start bounded CPU worker: %w", err)
		}
		commands = append(commands, cmd)
		if err := writeControl(leaf, "cgroup.procs", strconv.Itoa(cmd.Process.Pid)); err != nil {
			return err
		}
	}
	parentPath, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	before, err := manager.CpuStatRaw(parentPath)
	if err != nil {
		return fmt.Errorf("read parent CPU counters before workload: %w", err)
	}
	started := time.Now()
	for _, gate := range gates {
		if _, err := gate.Write([]byte{1}); err != nil {
			return fmt.Errorf("release bounded CPU worker: %w", err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("bounded CPU worker: %w", err)
		}
	}
	elapsed := time.Since(started)
	var childrenUsage uint64
	for _, leaf := range leaves {
		path, err := filepath.Rel(root, leaf)
		if err != nil {
			return err
		}
		usage, err := manager.CpuUsage(path)
		if err != nil {
			return fmt.Errorf("read worker CPU usage: %w", err)
		}
		if usage.Usage == 0 {
			return fmt.Errorf("worker cgroup %q accumulated no CPU usage", path)
		}
		childrenUsage += usage.Usage
	}
	after, err := manager.CpuStatRaw(parentPath)
	if err != nil {
		return fmt.Errorf("read parent CPU counters after workload: %w", err)
	}
	if after["usage_usec"] < before["usage_usec"] || after["nr_throttled"] <= before["nr_throttled"] {
		return errors.New("shared parent did not accumulate monotonic usage and throttling; check external CPU restrictions")
	}
	// Allow period-boundary runtime; do not assert equal shares for siblings.
	budgetUS := float64(elapsed.Microseconds())*0.5 + 150000
	if float64(after["usage_usec"]-before["usage_usec"]) > budgetUS {
		return fmt.Errorf("parent consumed %d us over %s, exceeding 0.5 CPU budget plus period tolerance",
			after["usage_usec"]-before["usage_usec"], elapsed)
	}
	if childrenUsage > after["usage_usec"] {
		return errors.New("children CPU usage exceeds hierarchical parent usage")
	}
	fmt.Printf("real shared budget: child usage=%d us, parent usage=%d us, throttled periods=%d, elapsed=%s\n",
		childrenUsage, after["usage_usec"]-before["usage_usec"], after["nr_throttled"]-before["nr_throttled"], elapsed)
	return nil
}

var burnSink uint64

func burn() error {
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		return fmt.Errorf("wait for owned-cgroup placement: %w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	value := uint64(1)
	for time.Now().Before(deadline) {
		for i := 0; i < 16384; i++ {
			value = value*2862933555777941757 + 3037000493
		}
	}
	burnSink = value
	return nil
}
