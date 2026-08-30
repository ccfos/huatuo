// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !didi

// IOCOST kernel tests exercise production sessions directly. Only helper faults
// and wake observations use the separate diagnostic object; IO remains real.
package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const ioCostKernelCgroupRoot = "/sys/fs/cgroup"

type ioCostKernelFixture struct {
	root            string
	directory       string
	nodes           string
	parent          string
	leaf            string
	parking         string
	retainResources bool
}

type ioCostKernelDisk struct {
	path        string
	name        string
	device      string
	backing     string
	index       int
	major       uint32
	minor       uint32
	removed     bool
	udevLock    *os.File
	costEnabled bool
}

type ioCostKernelSession struct {
	tracing *iocostTracing
	session *ioCostSession
	cancel  context.CancelFunc
	done    <-chan error
	stopped bool
}

type ioCostKernelWrite struct {
	command *exec.Cmd
	output  bytes.Buffer
	done    chan error
	waited  bool
}

func requireIOCostKernel(t *testing.T) *ioCostKernelFixture {
	t.Helper()
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("run make integration for live IOCOST qualification")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		t.Skip("IOCOST kernel qualification requires Linux x86_64 and root")
	}
	for _, file := range []string{"io.cost.model", "io.cost.qos", "cgroup.controllers"} {
		if _, err := os.Stat(filepath.Join(ioCostKernelCgroupRoot, file)); os.IsNotExist(err) {
			t.Skipf("IOCOST cgroup v2 interface is unavailable: %s", file)
		} else {
			require.NoError(t, err)
		}
	}
	if _, err := loadIOCostKernelProfile(); errors.Is(err, types.ErrNotSupported) {
		t.Skip(err)
	} else {
		require.NoError(t, err)
	}
	for _, name := range []string{"losetup", "udevadm"} {
		_, err := exec.LookPath(name)
		require.NoError(t, err, "IOCOST qualification requires %s", name)
	}
	if _, err := os.Stat("/dev/loop-control"); os.IsNotExist(err) {
		output, err := exec.CommandContext(t.Context(), "modprobe", "loop").CombinedOutput()
		if err != nil {
			t.Skipf("loop capability is unavailable: %v: %s", err, output)
		}
	}
	control, err := unix.Open("/dev/loop-control", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.ENOENT) {
		t.Skipf("loop control is unavailable: %v", err)
	}
	require.NoError(t, err)
	require.NoError(t, unix.Close(control))
	require.NoError(t, bpf.Init(&bpf.Option{}))
	root := filepath.Join("..", "..")
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join(root, "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir; bpf.Shutdown() })
	f := &ioCostKernelFixture{root: root}
	f.directory, err = os.MkdirTemp("/var/tmp", "huatuo-iocost-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if f.retainResources {
			t.Logf("IO has not exited; retaining owned devices, cgroups and backing files in %s", f.directory)
			return
		}
		require.NoError(t, os.RemoveAll(f.directory))
	})
	// /tmp may be mounted nodev. Private /dev nodes also avoid udev watches
	// on canonical disk nodes that would inject background probe IO.
	f.nodes, err = os.MkdirTemp("/dev", "huatuo-iocost-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if !f.retainResources {
			require.NoError(t, os.RemoveAll(f.nodes))
		}
	})

	// Only undo delegation added by this test, after all its children leave.
	controlPath := filepath.Join(ioCostKernelCgroupRoot, "cgroup.subtree_control")
	enabled, err := os.ReadFile(controlPath)
	require.NoError(t, err)
	if !slices.Contains(strings.Fields(string(enabled)), "io") {
		require.NoError(t, os.WriteFile(controlPath, []byte("+io"), 0o600))
		t.Cleanup(func() {
			if !f.retainResources {
				require.NoError(t, os.WriteFile(controlPath, []byte("-io"), 0o600))
			}
		})
	}
	parent, err := os.MkdirTemp(ioCostKernelCgroupRoot, "huatuo-iocost-")
	require.NoError(t, err)
	f.parent = parent
	t.Cleanup(func() {
		if !f.retainResources {
			require.NoError(t, os.Remove(parent))
		}
	})
	require.NoError(t, os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+io"), 0o600))
	f.leaf = f.cgroup(t, "leaf")
	f.parking = f.cgroup(t, "parking")
	return f
}

func (f *ioCostKernelFixture) cgroup(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(f.parent, name)
	require.NoError(t, os.Mkdir(path, 0o755))
	t.Cleanup(func() {
		if f.retainResources {
			return
		}
		if err := os.Remove(path); !os.IsNotExist(err) {
			require.NoError(t, err, "remove owned cgroup %s", path)
		}
	})
	require.NoError(t, os.WriteFile(filepath.Join(path, "io.weight"), []byte("default 100"), 0o600))
	return path
}

func (f *ioCostKernelFixture) disk(t *testing.T) *ioCostKernelDisk {
	t.Helper()
	directory, err := os.MkdirTemp(f.directory, "loop-")
	require.NoError(t, err)
	backing := filepath.Join(directory, "backing.img")
	file, err := os.OpenFile(backing, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(1<<30))
	require.NoError(t, file.Close())
	control, err := unix.Open("/dev/loop-control", unix.O_RDWR|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(control)
	// LOOP_CTL_ADD, unlike GET_FREE, reserves a new disk owned by this test.
	d := &ioCostKernelDisk{backing: backing, index: 256}
	for ; d.index < 65536; d.index++ {
		err = unix.IoctlSetInt(control, unix.LOOP_CTL_ADD, d.index)
		if !errors.Is(err, unix.EEXIST) {
			break
		}
	}
	require.NoError(t, err)
	require.Less(t, d.index, 65536)
	d.name = fmt.Sprintf("loop%d", d.index)
	// Ownership is recorded before node creation or binding can fail.
	t.Cleanup(func() {
		if !f.retainResources {
			d.remove(t)
		}
	})
	sysfs := filepath.Join("/sys/block", d.name)
	device, err := os.ReadFile(filepath.Join(sysfs, "dev"))
	require.NoError(t, err)
	d.device = strings.TrimSpace(string(device))
	_, err = fmt.Sscanf(d.device, "%d:%d", &d.major, &d.minor)
	require.NoError(t, err)
	d.path = filepath.Join(f.nodes, d.name)
	require.NoError(t, unix.Mknod(d.path, unix.S_IFBLK|0o600, int(unix.Mkdev(d.major, d.minor))))
	f.attach(t, d)
	return d
}

func (f *ioCostKernelFixture) attach(t *testing.T, d *ioCostKernelDisk) {
	t.Helper()
	// udev locks the canonical node, not our private alias. Hold its lock before
	// binding so background filesystem probes cannot become IOCOST wait samples.
	var err error
	waitIOControlKernelState(t, "owned loop device node", func() bool {
		d.udevLock, err = os.Open(filepath.Join("/dev", d.name))
		if os.IsNotExist(err) {
			return false
		}
		require.NoError(t, err)
		return true
	})
	waitIOControlKernelState(t, "exclusive loop device lock", func() bool {
		err := unix.Flock(int(d.udevLock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false
		}
		require.NoError(t, err)
		return true
	})
	runIOControlKernelCommand(t, 15*time.Second, "losetup", "--direct-io=on", d.path, d.backing)
	value, err := os.ReadFile(filepath.Join("/sys/block", d.name, "loop/dio"))
	require.NoError(t, err)
	require.Equal(t, "1", strings.TrimSpace(string(value)))
}

func (d *ioCostKernelDisk) unlock(t *testing.T) {
	t.Helper()
	if d.udevLock != nil {
		require.NoError(t, d.udevLock.Close())
		d.udevLock = nil
	}
}

func (d *ioCostKernelDisk) remove(t *testing.T) {
	t.Helper()
	if d.removed {
		return
	}
	// Deferred udev probes must not retain a low-rate waiter during teardown.
	if d.costEnabled {
		require.NoError(t, os.WriteFile(filepath.Join(ioCostKernelCgroupRoot, "io.cost.qos"),
			[]byte(d.device+" enable=0"), 0o600))
	}
	// An open device would defer LOOP_CLR_FD until the last reference closes.
	d.unlock(t)
	// The backing identity also finds a successful bind followed by a failed command.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "losetup", "--associated", d.backing,
		"--list", "--noheadings", "--output", "NAME").CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, loop := range strings.Fields(string(output)) {
		require.Equal(t, d.name, filepath.Base(loop), "private backing was rebound")
		require.NoError(t, cleanupIOControlKernelCommand("losetup", "--detach", loop))
	}
	control, err := unix.Open("/dev/loop-control", unix.O_RDWR|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(control)
	waitIOControlKernelState(t, "owned loop destruction", func() bool {
		err = unix.IoctlSetInt(control, unix.LOOP_CTL_REMOVE, d.index)
		if errors.Is(err, unix.EBUSY) {
			return false
		}
		require.NoError(t, err)
		return true
	})
	d.removed = true
}

func (d *ioCostKernelDisk) rates(t *testing.T, bps, iops int) {
	t.Helper()
	model := fmt.Sprintf("%s ctrl=user model=linear rbps=%d rseqiops=%d rrandiops=%d wbps=%d wseqiops=%d wrandiops=%d",
		d.device, bps, iops, iops, bps, iops, iops)
	require.NoError(t, os.WriteFile(filepath.Join(ioCostKernelCgroupRoot, "io.cost.model"), []byte(model), 0o600))
	qos := d.device + " enable=1 ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100"
	d.costEnabled = true
	require.NoError(t, os.WriteFile(filepath.Join(ioCostKernelCgroupRoot, "io.cost.qos"), []byte(qos), 0o600))
}

func (f *ioCostKernelFixture) start(t *testing.T, constants map[string]any) *ioCostKernelSession {
	t.Helper()
	profile, err := loadIOCostKernelProfile()
	require.NoError(t, err)
	cpus, err := ebpf.PossibleCPU()
	require.NoError(t, err)
	load := bpf.LoadBPF
	if constants != nil {
		load = func(_ string, values map[string]any) (bpf.BPF, error) {
			for name, value := range constants {
				values[name] = value
			}
			data, err := os.ReadFile(filepath.Join(f.root, "_output/test-bpf/iocost_tracing_test.o"))
			if err != nil {
				return nil, err
			}
			return bpf.LoadBPFFromBytes("iocost_tracing_test.o", data, values)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	s := &ioCostKernelSession{tracing: &iocostTracing{}, cancel: cancel}
	done := make(chan error, 1)
	s.done = done
	go func() {
		done <- s.tracing.startWithProfile(ctx, load, profile, cpus,
			func() (map[string]*pod.Container, error) { return nil, nil })
	}()
	t.Cleanup(func() { s.stop(t) })
	waitIOControlKernelState(t, "IOCOST session publication", func() bool {
		select {
		case err := <-done:
			s.stopped = true
			t.Fatalf("IOCOST stopped before publication: %v", err)
		default:
		}
		s.tracing.mu.Lock()
		defer s.tracing.mu.Unlock()
		s.session = s.tracing.session
		return s.session != nil
	})
	return s
}

func (s *ioCostKernelSession) stop(t *testing.T) {
	t.Helper()
	if s.stopped {
		return
	}
	s.cancel()
	select {
	case err := <-s.done:
		s.stopped = true
		if err != nil && !errors.Is(err, types.ErrTracingStopped) && !errors.Is(err, errIOControlDevicesChanged) {
			t.Errorf("stop IOCOST session: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("timeout stopping IOCOST session")
	}
}

// The child opens and allocates outside the IO cgroup. Its measured interval
// covers only real IO and close, not test-binary startup or parent polling.
func TestIOCostKernelWorkload(t *testing.T) {
	if os.Getenv("HUATUO_IOCOST_WRITER") == "" {
		t.Skip("helper process for owned IOCOST devices")
	}
	path := os.Getenv("HUATUO_IOCOST_WRITER")
	operation := os.Getenv("HUATUO_IOCOST_OPERATION")
	size, err := strconv.Atoi(os.Getenv("HUATUO_IOCOST_BYTES"))
	require.NoError(t, err)
	require.Greater(t, size, 0)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_DIRECT|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	buffer, err := unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	require.NoError(t, err)
	// Stop the issuer itself before it can submit IO outside the private group.
	runtime.LockOSThread()
	err = unix.Tgkill(os.Getpid(), unix.Gettid(), unix.SIGSTOP)
	runtime.UnlockOSThread()
	require.NoError(t, err)
	started := time.Now()
	if operation == "discard" {
		region := [2]uint64{0, uint64(size)}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x1277, uintptr(unsafe.Pointer(&region)))
		require.Zero(t, errno)
	} else {
		written, err := unix.Write(fd, buffer)
		require.NoError(t, err)
		require.Equal(t, size, written)
	}
	require.NoError(t, unix.Close(fd))
	fmt.Printf("iocost_io_ns=%d\n", time.Since(started).Nanoseconds())
	require.NoError(t, unix.Munmap(buffer))
}

func (f *ioCostKernelFixture) prepare(t *testing.T, d *ioCostKernelDisk, cgroup, operation string, size int) *ioCostKernelWrite {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	w := &ioCostKernelWrite{done: make(chan error, 1)}
	w.command = exec.CommandContext(ctx, executable, "-test.run=^TestIOCostKernelWorkload$")
	w.command.Env = append(os.Environ(), "HUATUO_IOCOST_WRITER="+d.path,
		"HUATUO_IOCOST_OPERATION="+operation, "HUATUO_IOCOST_BYTES="+strconv.Itoa(size))
	w.command.Stdout = &w.output
	w.command.Stderr = &w.output
	require.NoError(t, w.command.Start())
	go func() { w.done <- w.command.Wait() }()
	t.Cleanup(func() {
		cancel()
		if !w.waited {
			_ = w.command.Process.Kill()
			select {
			case <-w.done:
				w.waited = true
			case <-time.After(10 * time.Second):
				f.retainResources = true
				t.Errorf("IO child %d did not exit; keep its device until it does", w.command.Process.Pid)
			}
		}
	})
	waitIOControlKernelState(t, "prepared IOCOST IO child", func() bool {
		select {
		case err := <-w.done:
			w.waited = true
			t.Fatalf("IO child exited before preparation: %v: %s", err, w.output.String())
		default:
		}
		return w.state() == 'T'
	})
	require.NoError(t, os.WriteFile(filepath.Join(cgroup, "cgroup.procs"), []byte(strconv.Itoa(w.command.Process.Pid)), 0o600))
	return w
}

func (w *ioCostKernelWrite) state() byte {
	pid := strconv.Itoa(w.command.Process.Pid)
	tasks := filepath.Join("/proc", pid, "task")
	entries, err := os.ReadDir(tasks)
	if err != nil {
		return 0
	}
	var leader byte
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(tasks, entry.Name(), "stat"))
		if err != nil {
			continue // Threads can exit while their proc entries are enumerated.
		}
		fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
		if len(fields) == 0 {
			continue
		}
		state := fields[0][0]
		// Go may run the IO goroutine on a non-leader OS thread.
		if state == 'D' {
			return state
		}
		if entry.Name() == pid {
			leader = state
		}
	}
	return leader
}

func (w *ioCostKernelWrite) waitUntilBlocked(t *testing.T, description string) {
	t.Helper()
	waitIOControlKernelState(t, fmt.Sprintf("%s (pid=%d)", description, w.command.Process.Pid), func() bool {
		select {
		case err := <-w.done:
			w.waited = true
			t.Fatalf("IO child exited before %s: %v: %s", description, err, w.output.String())
		default:
		}
		return w.state() == 'D'
	})
}

func (w *ioCostKernelWrite) release(t *testing.T) {
	t.Helper()
	require.NoError(t, w.command.Process.Signal(unix.SIGCONT))
}

func (w *ioCostKernelWrite) finish(t *testing.T) time.Duration {
	t.Helper()
	select {
	case err := <-w.done:
		w.waited = true
		require.NoError(t, err, "%s", w.output.String())
	case <-time.After(90 * time.Second):
		t.Fatalf("IOCOST IO timed out: pid=%d state=%c", w.command.Process.Pid, w.state())
	}
	for _, line := range strings.Split(w.output.String(), "\n") {
		if value, ok := strings.CutPrefix(line, "iocost_io_ns="); ok {
			ns, err := strconv.ParseInt(value, 10, 64)
			require.NoError(t, err)
			return time.Duration(ns)
		}
	}
	t.Fatalf("IO child did not report its write duration: %s", w.output.String())
	return 0
}

func (f *ioCostKernelFixture) write(t *testing.T, d *ioCostKernelDisk, group string, size int) time.Duration {
	t.Helper()
	w := f.prepare(t, d, group, "write", size)
	w.release(t)
	return w.finish(t)
}

func (f *ioCostKernelFixture) condition(t *testing.T, d *ioCostKernelDisk, group string, bps, iops int) {
	t.Helper()
	d.rates(t, 1<<30, 262144)
	time.Sleep(time.Second)
	// Spend idle credit without depending on how a later 4 KiB IO is split.
	d.rates(t, 262144, 64)
	f.write(t, d, group, 1<<20)
	d.rates(t, bps, iops)
}

func (s *ioCostKernelSession) drain(t *testing.T) {
	t.Helper()
	waitIOControlKernelState(t, "IOCOST pending and wake-frame drain", func() bool {
		items, err := s.session.object.DumpMapByName(ioCostPendingMap)
		// Deleting an iterator's cursor can restart a HASH walk. Never infer
		// drain from a partial or duplicate walk while a completion is running.
		if errors.Is(err, ebpf.ErrIterationAborted) {
			return false
		}
		require.NoError(t, err)
		if len(items) != 0 {
			return false
		}
		value, err := s.session.object.ReadMap(s.session.object.MapIDByName(ioCostWakeFrameMap), ioControlTestBytes(uint32(0)))
		require.NoError(t, err)
		frames, err := decodeIOCostWakeFrames(value, s.session.possibleCPUs)
		require.NoError(t, err)
		for _, frame := range frames {
			if frame.BioPtr != 0 {
				return false
			}
		}
		return true
	})
}

func (s *ioCostKernelSession) update(t *testing.T) []*metric.Data {
	t.Helper()
	s.drain(t)
	current, err := s.session.captureRawSnapshot()
	require.NoError(t, err)
	previous := s.session.previous
	metrics, err := s.tracing.Update()
	require.NoError(t, err)
	// Verify raw packed CPU counters against the public host/other intervals,
	// without a second result format or a separate assertion process.
	totals := make(map[string]ioControlWaitCounters)
	for key, lanes := range current {
		require.Equal(t, uint32(1), key.Operation, "unexpected non-write IOCOST series: %+v", key)
		delta, err := deltaIOControlWaitCounters(previous[key], lanes)
		require.NoError(t, err)
		disk := ioControlDeviceName(key.Major, key.Minor)
		totals[disk] = addIOControlWaitCounters(totals[disk], delta)
	}
	for device, total := range totals {
		name, err := os.Readlink(filepath.Join("/sys/dev/block", device))
		require.NoError(t, err)
		average := total.averageMilliseconds()
		requireIOCostKernelInterval(t, metrics, filepath.Base(name), int(total.IOCount), average, average)
	}
	status, err := readIOControlStatus(s.session.object)
	require.NoError(t, err)
	require.Equal(t, ioControlStatus{}, status)
	return metrics
}

func requireIOCostKernelInterval(t *testing.T, metrics []*metric.Data, disk string, count int, lower, upper float64) float64 {
	t.Helper()
	values := make(map[string]map[string]float64)
	for _, data := range metrics {
		if data.Labels()["disk"] != disk || data.Labels()["operation"] != "write" {
			continue
		}
		require.Equal(t, metric.MetricTypeGauge, data.Type())
		scope := data.Labels()["scope"]
		require.Contains(t, []string{ioControlHostScope, ioControlOtherScope}, scope)
		if values[scope] == nil {
			values[scope] = make(map[string]float64)
		}
		require.NotContains(t, values[scope], data.Name(), "duplicate public series")
		values[scope][data.Name()] = data.Value
	}
	for _, scope := range []string{ioControlHostScope, ioControlOtherScope} {
		if count > 0 {
			require.Contains(t, values, scope)
			require.Contains(t, values[scope], ioCostWaitCountName)
			require.Contains(t, values[scope], ioCostAverageWaitName)
		}
		require.Equal(t, float64(count), values[scope][ioCostWaitCountName])
		require.GreaterOrEqual(t, values[scope][ioCostAverageWaitName], lower)
		require.LessOrEqual(t, values[scope][ioCostAverageWaitName], upper)
	}
	require.Equal(t, values[ioControlHostScope], values[ioControlOtherScope])
	return values[ioControlHostScope][ioCostAverageWaitName]
}

func ioCostKernelBounds(elapsed time.Duration) (float64, float64) {
	ms := float64(elapsed) / float64(time.Millisecond)
	return max(1, ms-200), ms + 200
}

func TestIOCostKernelFunctional(t *testing.T) {
	f := requireIOCostKernel(t)
	d := f.disk(t)
	d.rates(t, 1<<30, 262144)
	s := f.start(t, nil)
	f.write(t, d, f.leaf, 4096)
	stat, err := os.ReadFile(filepath.Join(f.leaf, "io.stat"))
	require.NoError(t, err)
	require.Contains(t, string(stat), "wios=1", "direct IO must be charged to the private leaf")
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)
	w := f.prepare(t, d, f.leaf, "discard", 4096)
	w.release(t)
	w.finish(t)
	stat, err = os.ReadFile(filepath.Join(f.leaf, "io.stat"))
	require.NoError(t, err)
	require.Contains(t, string(stat), "dios=1", "discard must reach the block layer")
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)
	var averages []float64
	for _, rate := range []int{65536, 4096} {
		f.condition(t, d, f.leaf, rate, rate/4096)
		s.update(t)
		elapsed := f.write(t, d, f.leaf, 4096)
		lower, upper := ioCostKernelBounds(elapsed)
		averages = append(averages, requireIOCostKernelInterval(t, s.update(t), d.name, 1, lower, upper))
		requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)
	}
	require.Greater(t, averages[1], averages[0])

	f.condition(t, d, f.leaf, 512, 1)
	s.update(t)
	w = f.prepare(t, d, f.leaf, "write", 4096)
	w.release(t)
	w.waitUntilBlocked(t, "IOCOST waiter before scrape")
	metrics, err := s.tracing.Update()
	require.NoError(t, err)
	requireIOCostKernelInterval(t, metrics, d.name, 0, 0, 0)
	elapsed := w.finish(t)
	lower, upper := ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, s.update(t), d.name, 1, lower, upper)
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)
	s.stop(t)

	// An existing waiter must not become a partial sample when probes attach.
	f.condition(t, d, f.leaf, 512, 1)
	w = f.prepare(t, d, f.leaf, "write", 4096)
	w.release(t)
	w.waitUntilBlocked(t, "pre-attach IOCOST waiter")
	s = f.start(t, nil)
	require.Equal(t, byte('D'), w.state(), "waiter must span the attach boundary")
	w.finish(t)
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)
	s.stop(t)
	t.Run("HTTP metrics", func(t *testing.T) { f.httpMetrics(t, d) })
}

func (f *ioCostKernelFixture) httpMetrics(t *testing.T, d *ioCostKernelDisk) {
	t.Helper()
	scrape := startIOControlKernelDaemon(t, "iocost")
	f.condition(t, d, f.leaf, 4096, 1)
	_, healthy := scrape()
	require.True(t, healthy)
	elapsed := f.write(t, d, f.leaf, 4096)
	metrics, healthy := scrape()
	require.True(t, healthy)
	for _, data := range metrics {
		require.Len(t, data.Labels(), 5)
	}
	lower, upper := ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, metrics, d.name, 1, lower, upper)
	metrics, healthy = scrape()
	require.True(t, healthy)
	requireIOCostKernelInterval(t, metrics, d.name, 0, 0, 0)
	// Exercise framework-owned recovery as well as the direct session checks.
	// The same daemon must survive a topology change and publish only fresh IO.
	f.write(t, d, f.leaf, 4096)
	topology := f.disk(t)
	topology.remove(t)
	metrics, _ = scrape()
	requireIOCostKernelInterval(t, metrics, d.name, 0, 0, 0)
	require.Eventually(t, func() bool {
		_, healthy := scrape()
		return healthy
	}, 30*time.Second, 100*time.Millisecond, "IOCOST HTTP recovery after loop destruction")
	elapsed = f.write(t, d, f.leaf, 4096)
	metrics, healthy = scrape()
	require.True(t, healthy)
	lower, upper = ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, metrics, d.name, 1, lower, upper)
	metrics, healthy = scrape()
	require.True(t, healthy)
	requireIOCostKernelInterval(t, metrics, d.name, 0, 0, 0)
}

func (s *ioCostKernelSession) identity(t *testing.T, d *ioCostKernelDisk) ioCostOwnerState {
	t.Helper()
	items, err := s.session.hashReader.dump(s.session.object, s.session.object.MapIDByName(ioCostOwnerStateMap))
	require.NoError(t, err)
	var target ioCostOwnerState
	for _, item := range items {
		var owner ioCostOwnerState
		require.NoError(t, decodeBPFMapData(item.Value, &owner))
		if owner.Major == d.major && owner.Minor == d.minor {
			require.Zero(t, target.CSSSerial, "diagnostic target must have a single owner")
			target = owner
		}
	}
	require.NotZero(t, target.CSS)
	require.NotZero(t, target.CSSSerial)
	return target
}

func (f *ioCostKernelFixture) diagnostic(t *testing.T, d *ioCostKernelDisk, owner ioCostOwnerState, fault uint32) *ioCostKernelSession {
	t.Helper()
	return f.start(t, map[string]any{
		"iocost_diag_css_serial":  owner.CSSSerial,
		"iocost_diag_major":       d.major,
		"iocost_diag_first_minor": d.minor,
		"iocost_diag_fault_mask":  fault,
	})
}

func (s *ioCostKernelSession) diagnostics(t *testing.T) ioCostDiagnosticStatus {
	t.Helper()
	s.drain(t)
	data, err := s.session.object.ReadMap(s.session.object.MapIDByName(ioCostDiagnosticStatusMap), ioControlTestBytes(uint32(0)))
	require.NoError(t, err)
	status, err := decodeIOCostDiagnosticStatus(data)
	require.NoError(t, err)
	return status
}

func TestIOCostKernelCompatibility(t *testing.T) {
	f := requireIOCostKernel(t)
	d := f.disk(t)
	f.condition(t, d, f.leaf, 4096, 1)
	s := f.start(t, nil)
	elapsed := f.write(t, d, f.leaf, 4096)
	lower, upper := ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, s.update(t), d.name, 1, lower, upper)
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)
	owner := s.identity(t, d)
	s.stop(t)
	for _, size := range []int{4096, 1 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			d.rates(t, 1<<30, 262144)
			time.Sleep(time.Second)
			s := f.diagnostic(t, d, owner, 0)
			// Small IO proves one admission; large IO covers split bios.
			d.rates(t, 262144, 64)
			if size == 4096 {
				d.rates(t, 4096, 1)
			}
			f.write(t, d, f.leaf, size)
			status := s.diagnostics(t)
			require.GreaterOrEqual(t, status.WakeReturnMinusOne, uint64(2))
			require.Greater(t, status.SettledCount, uint64(0))
			require.Equal(t, status.StartGuardPasses, status.SettledCount)
			require.Equal(t, status.WakeReturnZero, status.SettledCount)
			require.Equal(t, status.WakeEntryHits, status.WakeReturnZero+status.WakeReturnMinusOne)
			require.Zero(t, status.MapFullInjections)
			require.Zero(t, status.CollisionInjections)
			require.Zero(t, status.DeleteFailureInjections)
			if size == 4096 {
				require.Equal(t, uint64(1), status.SettledCount)
			}
			s.update(t)
		})
	}
	// Offline callbacks are selected by capability, not release numbers.
	symbols, err := os.ReadFile("/proc/kallsyms")
	require.NoError(t, err)
	if !strings.Contains(string(symbols), " ioc_pd_offline\n") {
		t.Log("offline wake callback is unavailable")
		return
	}
	f.condition(t, d, f.leaf, 1024, 1)
	s = f.diagnostic(t, d, owner, 0)
	w := f.prepare(t, d, f.leaf, "write", 4096)
	w.release(t)
	w.waitUntilBlocked(t, "offline IOCOST waiter")
	require.NoError(t, os.WriteFile(filepath.Join(f.parking, "cgroup.procs"), []byte(strconv.Itoa(w.command.Process.Pid)), 0o600))
	require.NoError(t, os.Remove(f.leaf))
	w.finish(t)
	status := s.diagnostics(t)
	require.Greater(t, status.SettledCount, uint64(0))
	require.Equal(t, status.StartGuardPasses, status.SettledCount)
	require.Equal(t, status.WakeReturnZero, status.SettledCount)
	require.Equal(t, status.WakeEntryHits, status.WakeReturnZero+status.WakeReturnMinusOne)
}

func TestIOCostKernelFaults(t *testing.T) {
	f := requireIOCostKernel(t)
	d := f.disk(t)
	f.condition(t, d, f.leaf, 4096, 1)
	s := f.start(t, nil)
	f.write(t, d, f.leaf, 4096)
	s.update(t)
	owner := s.identity(t, d)
	s.stop(t)
	for _, test := range []struct {
		name   string
		mask   uint32
		reason uint32
		errno  int32
	}{
		{"capacity", ioCostDiagnosticFaultMapFull, ioCostFailurePendingInsert, -int32(unix.E2BIG)},
		{"residual overwrite", ioCostDiagnosticFaultCollision, 0, 0},
		{"delete", ioCostDiagnosticFaultDeleteFailure, ioCostFailurePendingDelete, -int32(unix.ENOENT)},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.condition(t, d, f.leaf, 4096, 1)
			s := f.diagnostic(t, d, owner, test.mask)
			f.write(t, d, f.leaf, 4096)
			diagnostic := s.diagnostics(t)
			require.Equal(t, uint64(1), diagnostic.StartGuardPasses)
			status, err := readIOControlStatus(s.session.object)
			require.NoError(t, err)
			require.Equal(t, ioControlTestStatus(test.reason, test.errno), status)
			switch test.mask {
			case ioCostDiagnosticFaultMapFull:
				require.Equal(t, uint64(1), diagnostic.MapFullInjections)
				require.Zero(t, diagnostic.CollisionInjections)
				require.Zero(t, diagnostic.DeleteFailureInjections)
				require.Zero(t, diagnostic.WakeEntryHits)
				require.Zero(t, diagnostic.WakeReturnZero)
				require.Zero(t, diagnostic.WakeReturnMinusOne)
				require.Zero(t, diagnostic.SettledCount)
			case ioCostDiagnosticFaultCollision:
				require.Equal(t, uint64(1), diagnostic.CollisionInjections)
				require.Zero(t, diagnostic.MapFullInjections)
				require.Zero(t, diagnostic.DeleteFailureInjections)
				require.Equal(t, uint64(1), diagnostic.SettledCount)
				require.Equal(t, uint64(1), diagnostic.WakeReturnZero)
				require.Equal(t, diagnostic.WakeEntryHits, diagnostic.WakeReturnZero+diagnostic.WakeReturnMinusOne)
				s.update(t)
			case ioCostDiagnosticFaultDeleteFailure:
				require.Equal(t, uint64(1), diagnostic.DeleteFailureInjections)
				require.Zero(t, diagnostic.MapFullInjections)
				require.Zero(t, diagnostic.CollisionInjections)
				require.Equal(t, uint64(1), diagnostic.WakeEntryHits)
				require.Zero(t, diagnostic.WakeReturnZero)
				require.Zero(t, diagnostic.WakeReturnMinusOne)
				require.Zero(t, diagnostic.SettledCount)
			}
			if test.reason != 0 {
				_, err := s.tracing.Update()
				require.ErrorIs(t, err, types.ErrTracingStopped)
			}
		})
	}
}

func TestIOCostKernelLifecycle(t *testing.T) {
	f := requireIOCostKernel(t)
	d := f.disk(t)
	other := f.disk(t)
	f.condition(t, d, f.leaf, 4096, 1)
	f.condition(t, other, f.parking, 4096, 1)
	s := f.start(t, nil)
	f.write(t, d, f.leaf, 4096)
	requireIOCostKernelInterval(t, s.update(t), d.name, 1, 0, 90000)
	f.write(t, d, f.parking, 4096)
	// rmdir does not synchronously release IOCGs. Verify public intervals while
	// the kernel owns any old references; never force its deferred release.
	require.NoError(t, os.Remove(f.leaf))
	f.leaf = f.cgroup(t, "leaf")
	f.write(t, d, f.leaf, 4096)
	requireIOCostKernelInterval(t, s.update(t), d.name, 2, 0, 90000)
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)

	// Binding and detaching an unused loop must not discard other disks' IO.
	topology := f.disk(t)
	for _, bind := range []bool{false, true} {
		f.write(t, d, f.leaf, 4096)
		f.write(t, other, f.parking, 4096)
		if bind {
			f.attach(t, topology)
		} else {
			topology.unlock(t)
			runIOControlKernelCommand(t, 15*time.Second, "losetup", "--detach", topology.path)
		}
		metrics := s.update(t)
		requireIOCostKernelInterval(t, metrics, d.name, 1, 0, 90000)
		requireIOCostKernelInterval(t, metrics, other.name, 1, 0, 90000)
	}
	f.write(t, d, f.leaf, 4096)
	f.write(t, other, f.parking, 4096)
	topology.remove(t)
	metrics, err := s.tracing.Update()
	require.ErrorIs(t, err, errIOControlDevicesChanged)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	requireIOCostKernelInterval(t, metrics, d.name, 0, 0, 0)
	requireIOCostKernelInterval(t, metrics, other.name, 0, 0, 0)
	s.stop(t)
	s = f.start(t, nil)
	f.write(t, d, f.leaf, 4096)
	f.write(t, other, f.parking, 4096)
	metrics = s.update(t)
	requireIOCostKernelInterval(t, metrics, d.name, 1, 0, 90000)
	requireIOCostKernelInterval(t, metrics, other.name, 1, 0, 90000)
	s.stop(t)
	t.Run("SCSI rebind", func(t *testing.T) { f.scsiRebind(t, other) })
}

func TestIOCostKernelPressure(t *testing.T) {
	f := requireIOCostKernel(t)
	d := f.disk(t)
	d.rates(t, 1<<30, 262144)
	time.Sleep(time.Second)
	s := f.start(t, nil)
	var writers []*ioCostKernelWrite
	for range 32 {
		writers = append(writers, f.prepare(t, d, f.leaf, "write", 4096))
	}
	for _, w := range writers {
		w.release(t)
	}
	for _, w := range writers {
		w.finish(t)
	}
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)

	// Fresh cgroups exercise owner growth. This cohort is deliberately serial,
	// unlike the preceding concurrent no-wait workload.
	d.rates(t, 65536, 16)
	var total time.Duration
	for index := range 64 {
		group := f.cgroup(t, fmt.Sprintf("short-%d", index))
		total += f.write(t, d, group, 4096)
	}
	lower, upper := ioCostKernelBounds(total / 64)
	requireIOCostKernelInterval(t, s.update(t), d.name, 64, lower, upper)
	requireIOCostKernelInterval(t, s.update(t), d.name, 0, 0, 0)

	f.condition(t, d, f.leaf, 4096, 1)
	s.update(t)
	elapsed := f.write(t, d, f.leaf, 4096)
	lower, upper = ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, s.update(t), d.name, 1, lower, upper)
	s.stop(t)

	other := f.disk(t)
	f.condition(t, d, f.leaf, 65536, 16)
	f.condition(t, other, f.parking, 65536, 16)
	s = f.start(t, nil)
	bounds := make(map[string][2]float64)
	for index, disk := range []*ioCostKernelDisk{d, other} {
		var elapsed time.Duration
		for peer := range 2 {
			group := f.cgroup(t, fmt.Sprintf("multi-%d-%d", index, peer))
			elapsed += f.write(t, disk, group, 4096)
		}
		lower, upper := ioCostKernelBounds(elapsed / 2)
		bounds[disk.name] = [2]float64{lower, upper}
	}
	metrics := s.update(t)
	for _, disk := range []*ioCostKernelDisk{d, other} {
		requireIOCostKernelInterval(t, metrics, disk.name, 2, bounds[disk.name][0], bounds[disk.name][1])
	}
	metrics = s.update(t)
	for _, disk := range []*ioCostKernelDisk{d, other} {
		requireIOCostKernelInterval(t, metrics, disk.name, 0, 0, 0)
	}
}

func (f *ioCostKernelFixture) scsiRebind(t *testing.T, survivor *ioCostKernelDisk) {
	t.Helper()
	if _, err := os.Stat("/sys/module/scsi_debug"); err == nil {
		t.Skip("scsi_debug is already owned by another test")
	}
	output, err := exec.CommandContext(t.Context(), "modprobe", "--show-depends", "scsi_debug").CombinedOutput()
	if err != nil && strings.Contains(string(output), "not found") {
		t.Skipf("scsi_debug module is unavailable: %s", output)
	}
	require.NoError(t, err, "%s", output)
	marker, err := os.ReadFile("/proc/sys/kernel/random/uuid")
	require.NoError(t, err)
	owner := strings.SplitN(strings.TrimSpace(string(marker)), "-", 2)[0]
	owned := func() bool {
		vendor, err := os.ReadFile("/sys/module/scsi_debug/parameters/inq_vendor")
		return err == nil && strings.TrimSpace(string(vendor)) == owner
	}
	// A racing loader or a timeout must not let cleanup adopt another test's module.
	t.Cleanup(func() {
		if f.retainResources {
			return
		}
		if owned() {
			require.NoError(t, cleanupIOControlKernelCommand("modprobe", "-r", "scsi_debug"))
		}
	})
	runIOControlKernelCommand(t, 15*time.Second, "modprobe", "--first-time", "scsi_debug",
		"dev_size_mb=64", "add_host=1", "inq_vendor="+owner)
	require.True(t, owned(), "cannot verify scsi_debug module ownership")
	find := func() *ioCostKernelDisk {
		entries, err := os.ReadDir("/sys/block")
		require.NoError(t, err)
		var disk *ioCostKernelDisk
		for _, entry := range entries {
			model, err := os.ReadFile(filepath.Join("/sys/block", entry.Name(), "device/model"))
			if err != nil || strings.TrimSpace(string(model)) != "scsi_debug" {
				continue
			}
			require.Nil(t, disk, "scsi_debug must expose exactly one owned disk")
			disk = &ioCostKernelDisk{name: entry.Name()}
			value, err := os.ReadFile(filepath.Join("/sys/block", entry.Name(), "dev"))
			require.NoError(t, err)
			disk.device = strings.TrimSpace(string(value))
			_, err = fmt.Sscanf(disk.device, "%d:%d", &disk.major, &disk.minor)
			require.NoError(t, err)
			nodeDirectory, err := os.MkdirTemp(f.nodes, "scsi-")
			require.NoError(t, err)
			disk.path = filepath.Join(nodeDirectory, entry.Name())
			require.NoError(t, unix.Mknod(disk.path, unix.S_IFBLK|0o600, int(unix.Mkdev(disk.major, disk.minor))))
		}
		return disk
	}
	var disk *ioCostKernelDisk
	waitIOControlKernelState(t, "owned scsi_debug disk", func() bool { disk = find(); return disk != nil })
	runIOControlKernelCommand(t, 15*time.Second, "udevadm", "info", "--wait-for-initialization=10", "--name=/dev/"+disk.name)
	path, err := filepath.EvalSymlinks(filepath.Join("/sys/block", disk.name, "device"))
	require.NoError(t, err)
	address := filepath.Base(path)
	oldName := disk.name
	f.condition(t, disk, f.leaf, 4096, 1)
	scrape := startIOControlKernelDaemon(t, "iocost")
	f.write(t, disk, f.leaf, 4096)
	f.write(t, survivor, f.parking, 4096)
	disk.rates(t, 1<<30, 262144)
	require.NoError(t, os.WriteFile("/sys/bus/scsi/drivers/sd/unbind", []byte(address), 0o600))
	require.NoError(t, os.WriteFile("/sys/bus/scsi/drivers/sd/bind", []byte(address), 0o600))
	metrics, _ := scrape()
	requireIOCostKernelInterval(t, metrics, oldName, 0, 0, 0)
	requireIOCostKernelInterval(t, metrics, survivor.name, 0, 0, 0)
	waitIOControlKernelState(t, "rebound scsi_debug disk", func() bool { disk = find(); return disk != nil })
	runIOControlKernelCommand(t, 15*time.Second, "udevadm", "info", "--wait-for-initialization=10", "--name=/dev/"+disk.name)
	f.condition(t, disk, f.leaf, 4096, 1)
	require.Eventually(t, func() bool {
		_, healthy := scrape()
		return healthy
	}, 30*time.Second, 100*time.Millisecond, "IOCOST HTTP recovery after SCSI rebind")
	peer := f.cgroup(t, "scsi-peer")
	first := f.prepare(t, disk, f.leaf, "write", 4096)
	second := f.prepare(t, disk, peer, "write", 4096)
	first.release(t)
	second.release(t)
	first.finish(t)
	second.finish(t)
	f.write(t, survivor, f.parking, 4096)
	metrics, healthy := scrape()
	require.True(t, healthy)
	requireIOCostKernelInterval(t, metrics, disk.name, 2, 0, 90000)
	requireIOCostKernelInterval(t, metrics, survivor.name, 1, 0, 90000)
	if oldName != disk.name {
		requireIOCostKernelInterval(t, metrics, oldName, 0, 0, 0)
	}
	metrics, healthy = scrape()
	require.True(t, healthy)
	requireIOCostKernelInterval(t, metrics, disk.name, 0, 0, 0)
	requireIOCostKernelInterval(t, metrics, survivor.name, 0, 0, 0)
}
