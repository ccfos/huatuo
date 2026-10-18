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
	ctx             context.Context
	cgroupRoot      string
	controlPrefix   string
	directory       string
	nodes           string
	parent          string
	leaf            string
	parking         string
	retainResources bool
	cleanupDeadline time.Time
}

type ioCostKernelDisk struct {
	path        string
	name        string
	device      string
	backing     string
	modelPath   string
	qosPath     string
	index       int
	major       uint32
	minor       uint32
	removed     bool
	udevLock    *os.File
	costEnabled bool
}

type ioCostKernelSession struct {
	ctx     context.Context
	tracing *iocostTracing
	session *ioCostSession
	cancel  context.CancelFunc
	done    <-chan error
	stopped bool
}

type ioCostKernelWrite struct {
	ctx     context.Context
	command *exec.Cmd
	output  bytes.Buffer
	done    chan error
	waited  bool
}

func requireIOCostKernel(t *testing.T) *ioCostKernelFixture {
	t.Helper()
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("run make test for live IOCOST qualification")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		t.Skip("IOCOST kernel qualification requires Linux x86_64 and root")
	}
	require.NoError(t, os.MkdirAll("/run/huatuo-iocost-test", 0o755))
	lock, err := os.OpenFile("/run/huatuo-iocost-test/runner.lock", os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })
	require.NoError(
		t,
		unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB),
		"another IOCOST qualification is running",
	)
	f := &ioCostKernelFixture{cgroupRoot: ioCostKernelCgroupRoot, controlPrefix: "io"}
	if _, err := os.Stat(filepath.Join(f.cgroupRoot, "cgroup.controllers")); os.IsNotExist(err) {
		f.cgroupRoot = filepath.Join(f.cgroupRoot, "blkio")
		f.controlPrefix = "blkio"
	} else {
		require.NoError(t, err)
	}
	for _, file := range []string{"cost.model", "cost.qos"} {
		path := filepath.Join(f.cgroupRoot, f.controlPrefix+"."+file)
		if err := unix.Access(path, unix.W_OK); errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EROFS) || errors.Is(err, unix.EACCES) {
			t.Skipf("writable IOCOST interface is unavailable: %s: %v", path, err)
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
	ctx := ioControlKernelContext(t)
	f.ctx = ctx
	f.check(t)
	f.directory, err = os.MkdirTemp("/var/tmp", "huatuo-iocost-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if f.retainResources {
			t.Logf("IO has not exited; retaining owned resources: work=%s nodes=%s cgroup=%s", f.directory, f.nodes, f.parent)
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

	if f.controlPrefix == "io" {
		// Only v2 needs delegation. Undo our change after its children leave.
		controlPath := filepath.Join(f.cgroupRoot, "cgroup.subtree_control")
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
	}
	parent, err := os.MkdirTemp(f.cgroupRoot, "huatuo-iocost-")
	require.NoError(t, err)
	f.parent = parent
	t.Cleanup(func() {
		if !f.retainResources {
			require.NoError(t, os.Remove(parent))
		}
	})
	if f.controlPrefix == "io" {
		require.NoError(t, os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte("+io"), 0o600))
	}
	f.leaf = f.cgroup(t, "leaf")
	f.parking = f.cgroup(t, "parking")
	if os.Getenv("TEST_IOCOST_DIAGNOSTIC_OBJECT") == "" {
		t.Setenv("TEST_IOCOST_DIAGNOSTIC_OBJECT", compileIOCostDiagnosticObject(t))
	}
	t.Logf("IOCOST owned resources: work=%s nodes=%s cgroup=%s", f.directory, f.nodes, f.parent)
	return f
}

func (f *ioCostKernelFixture) check(t *testing.T) {
	t.Helper()
	require.NoError(t, f.ctx.Err(), "IOCOST qualification interrupted; work=%s nodes=%s cgroup=%s",
		f.directory, f.nodes, f.parent)
}

func (f *ioCostKernelFixture) wait(t *testing.T, description string, condition func() bool) {
	t.Helper()
	waitIOControlKernelState(t, f.ctx, description, func() bool {
		f.check(t)
		return condition()
	})
}

func (f *ioCostKernelFixture) cgroup(t *testing.T, name string) string {
	t.Helper()
	f.check(t)
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
	// Fresh groups already have the default IOCOST weight on both hierarchies.
	return path
}

func (f *ioCostKernelFixture) disk(t *testing.T) *ioCostKernelDisk {
	t.Helper()
	f.check(t)
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
	d := &ioCostKernelDisk{
		backing: backing, index: 256,
		modelPath: filepath.Join(f.cgroupRoot, f.controlPrefix+".cost.model"),
		qosPath:   filepath.Join(f.cgroupRoot, f.controlPrefix+".cost.qos"),
	}
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
	f.wait(t, "owned loop device node", func() bool {
		d.udevLock, err = os.Open(filepath.Join("/dev", d.name))
		if os.IsNotExist(err) {
			return false
		}
		require.NoError(t, err)
		return true
	})
	f.wait(t, "exclusive loop device lock", func() bool {
		err := unix.Flock(int(d.udevLock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false
		}
		require.NoError(t, err)
		return true
	})
	runIOControlKernelCommand(t, f.ctx, 15*time.Second, "losetup", "--direct-io=on", d.path, d.backing)
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
		require.NoError(t, os.WriteFile(d.qosPath, []byte(d.device+" enable=0"), 0o600))
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
	waitIOControlKernelState(t, context.Background(), "owned loop destruction", func() bool {
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
	require.NoError(t, os.WriteFile(d.modelPath, []byte(model), 0o600))
	qos := d.device + " enable=1 ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100"
	d.costEnabled = true
	require.NoError(t, os.WriteFile(d.qosPath, []byte(qos), 0o600))
}

func (f *ioCostKernelFixture) start(t *testing.T, constants map[string]any) *ioCostKernelSession {
	t.Helper()
	f.check(t)
	profile, err := loadIOCostKernelProfile()
	require.NoError(t, err)
	cpus, err := ebpf.PossibleCPU()
	require.NoError(t, err)
	load := loadIOControlBPF
	if constants != nil {
		load = func(_ string, values map[string]any) (bpf.BPF, error) {
			for name, value := range constants {
				values[name] = value
			}
			spec, err := ebpf.LoadCollectionSpec(os.Getenv("TEST_IOCOST_DIAGNOSTIC_OBJECT"))
			if err != nil {
				return nil, err
			}
			return loadIOControlBPFSpec("iocost_tracing_test.o", spec, values)
		}
	}
	ctx, cancel := context.WithCancel(f.ctx)
	s := &ioCostKernelSession{ctx: ctx, tracing: &iocostTracing{}, cancel: cancel}
	done := make(chan error, 1)
	s.done = done
	go func() {
		done <- s.tracing.startWithProfile(ctx, load, profile, cpus,
			func() (map[string]*pod.Container, error) { return nil, nil })
	}()
	t.Cleanup(func() { s.stop(t) })
	f.wait(t, "IOCOST session publication", func() bool {
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
	f.check(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 90*time.Second)
	w := &ioCostKernelWrite{ctx: ctx, done: make(chan error, 1)}
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
			deadline := time.Now().Add(10 * time.Second)
			if f.ctx.Err() != nil {
				// Parent cancellation kills all issuers together. Do not give
				// each of the 32 pressure writers another ten-second wait.
				if f.cleanupDeadline.IsZero() {
					f.cleanupDeadline = deadline
				}
				deadline = f.cleanupDeadline
			}
			select {
			case <-w.done:
				w.waited = true
				return
			default:
			}
			select {
			case <-w.done:
				w.waited = true
			case <-time.After(time.Until(deadline)):
				f.retainResources = true
				t.Errorf("IO child %d did not exit; keep its device until it does", w.command.Process.Pid)
			}
		}
	})
	f.wait(t, "prepared IOCOST IO child", func() bool {
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
	waitIOControlKernelState(t, w.ctx, fmt.Sprintf("%s (pid=%d)", description, w.command.Process.Pid), func() bool {
		require.NoError(t, w.ctx.Err(), "IOCOST IO canceled: pid=%d", w.command.Process.Pid)
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
	case <-w.ctx.Done():
		t.Fatalf("IOCOST IO canceled: pid=%d state=%c: %v", w.command.Process.Pid, w.state(), w.ctx.Err())
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

func ioCostKernelOwnsDevice(key ioControlWaitKey, disks []*ioCostKernelDisk) bool {
	for _, disk := range disks {
		if key.Major == disk.major && key.Minor == disk.minor {
			return true
		}
	}
	return false
}

func (s *ioCostKernelSession) drain(t *testing.T, disks ...*ioCostKernelDisk) {
	t.Helper()
	// Callers have joined their issuers. A wake return may still be accounting
	// on another CPU; unrelated devices need not drain for this fixture.
	// Pending has no disk identity, so it cannot establish a fixture boundary.
	waitIOControlKernelState(t, s.ctx, "owned IOCOST wake-frame drain", func() bool {
		require.NoError(t, s.ctx.Err(), "IOCOST session canceled")
		value, err := s.session.object.ReadMap(s.session.object.MapIDByName(ioCostWakeFrameMap), ioControlTestBytes(uint32(0)))
		require.NoError(t, err)
		frames, err := decodeIOCostWakeFrames(value, s.session.possibleCPUs)
		require.NoError(t, err)
		for _, frame := range frames {
			if frame.BioPtr != 0 && ioCostKernelOwnsDevice(frame.Key, disks) {
				return false
			}
		}
		return true
	})
}

func (s *ioCostKernelSession) update(t *testing.T, disks ...*ioCostKernelDisk) []*metric.Data {
	t.Helper()
	require.NotEmpty(t, disks)
	s.drain(t, disks...)
	previous := s.session.previous
	metrics, err := s.tracing.Update()
	require.NoError(t, err)
	// Compare the committed snapshot with its public interval. A separate
	// global walk could observe another generation of unrelated traffic.
	totals := make(map[string]ioControlWaitCounters)
	for key, lanes := range s.session.previous {
		if !ioCostKernelOwnsDevice(key, disks) {
			continue
		}
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
	statFile, writeStat, discardStat := "io.stat", "wios=1", "dios=1"
	if f.controlPrefix == "blkio" {
		statFile = "blkio.throttle.io_serviced"
		writeStat, discardStat = d.device+" Write 1\n", d.device+" Discard 1\n"
	}
	stat, err := os.ReadFile(filepath.Join(f.leaf, statFile))
	require.NoError(t, err)
	require.Contains(t, string(stat), writeStat, "direct IO must be charged to the private leaf")
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
	w := f.prepare(t, d, f.leaf, "discard", 4096)
	w.release(t)
	w.finish(t)
	stat, err = os.ReadFile(filepath.Join(f.leaf, statFile))
	require.NoError(t, err)
	require.Contains(t, string(stat), discardStat, "discard must reach the block layer")
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
	var averages []float64
	for _, rate := range []int{65536, 4096} {
		f.condition(t, d, f.leaf, rate, rate/4096)
		s.update(t, d)
		elapsed := f.write(t, d, f.leaf, 4096)
		lower, upper := ioCostKernelBounds(elapsed)
		averages = append(averages, requireIOCostKernelInterval(t, s.update(t, d), d.name, 1, lower, upper))
		requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
	}
	require.Greater(t, averages[1], averages[0])

	f.condition(t, d, f.leaf, 512, 1)
	s.update(t, d)
	w = f.prepare(t, d, f.leaf, "write", 4096)
	w.release(t)
	w.waitUntilBlocked(t, "IOCOST waiter before scrape")
	metrics, err := s.tracing.Update()
	require.NoError(t, err)
	requireIOCostKernelInterval(t, metrics, d.name, 0, 0, 0)
	elapsed := w.finish(t)
	lower, upper := ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 1, lower, upper)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
	s.stop(t)

	// An existing waiter must not become a partial sample when probes attach.
	f.condition(t, d, f.leaf, 512, 1)
	w = f.prepare(t, d, f.leaf, "write", 4096)
	w.release(t)
	w.waitUntilBlocked(t, "pre-attach IOCOST waiter")
	s = f.start(t, nil)
	require.Equal(t, byte('D'), w.state(), "waiter must span the attach boundary")
	w.finish(t)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
	s.stop(t)
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

func (s *ioCostKernelSession) diagnostics(t *testing.T, disk *ioCostKernelDisk) ioCostDiagnosticStatus {
	t.Helper()
	s.drain(t, disk)
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
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 1, lower, upper)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
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
			status := s.diagnostics(t, d)
			// Timer scheduling determines the number of insufficient-budget wakes.
			// Repeated -1 returns are driven explicitly by the native accounting test.
			t.Logf("observed insufficient-budget wakes: %d", status.WakeReturnMinusOne)
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
			s.update(t, d)
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
	status := s.diagnostics(t, d)
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
	s.update(t, d)
	owner := s.identity(t, d)
	s.stop(t)
	for _, test := range []struct {
		name   string
		mask   uint32
		reason uint32
		errno  int32
	}{
		{
			name: "capacity", mask: ioCostDiagnosticFaultMapFull,
			reason: ioCostFailurePendingInsert, errno: -int32(unix.E2BIG),
		},
		{name: "residual overwrite", mask: ioCostDiagnosticFaultCollision},
		{
			name: "delete", mask: ioCostDiagnosticFaultDeleteFailure,
			reason: ioCostFailurePendingDelete, errno: -int32(unix.ENOENT),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.condition(t, d, f.leaf, 4096, 1)
			s := f.diagnostic(t, d, owner, test.mask)
			f.write(t, d, f.leaf, 4096)
			diagnostic := s.diagnostics(t, d)
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
				s.update(t, d)
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
	t.Run("released cgroup final interval", func(t *testing.T) {
		elapsed := f.write(t, d, f.leaf, 4096)
		s.drain(t, d)
		owner := s.identity(t, d)
		identity := ioControlRetiredKey{
			CSS:       owner.CSS,
			CSSSerial: owner.CSSSerial,
			Major:     owner.Major,
			Minor:     owner.Minor,
		}
		require.NoError(t, os.Remove(f.leaf))
		// rmdir only starts asynchronous release. This case requires the real
		// pd_free boundary; expiry is not evidence of a collector failure.
		deadline := time.Now().Add(120 * time.Second)
		for {
			f.check(t)
			items, err := s.session.hashReader.dump(s.session.object, s.session.object.MapIDByName(ioCostOwnerStateMap))
			require.NoError(t, err)
			hasOwner := false
			for _, item := range items {
				var current ioCostOwnerState
				require.NoError(t, decodeBPFMapData(item.Value, &current))
				if current == owner {
					hasOwner = true
					break
				}
			}
			if !hasOwner {
				break
			}
			if time.Now().After(deadline) {
				t.Skip("old owner is still present; final retired interval was not exercised")
			}
			time.Sleep(100 * time.Millisecond)
		}
		// The marker precedes owner removal; observing removal first excludes
		// an in-progress publication and makes a missing marker a failure.
		retired, err := captureIOControlRetired(s.session.object, ioCostRetiredMap)
		require.NoError(t, err)
		require.Contains(t, retired, identity, "released owner must publish its retirement marker")
		metrics, err := s.tracing.Update()
		require.NoError(t, err)
		lower, upper := ioCostKernelBounds(elapsed)
		requireIOCostKernelInterval(t, metrics, d.name, 1, lower, upper)
		requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)
		retired, err = captureIOControlRetired(s.session.object, ioCostRetiredMap)
		require.NoError(t, err)
		require.NotContains(t, retired, identity)
	})
	// A kernel that has not reached pd_free still retains its completed IO.
	// Consume that window before the independent live-cgroup checks below.
	s.update(t, d)
	f.leaf = f.cgroup(t, "leaf")
	f.write(t, d, f.leaf, 4096)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 1, 0, 90000)
	f.write(t, d, f.parking, 4096)
	// rmdir does not synchronously release IOCGs. Verify public intervals while
	// the kernel owns any old references; never force its deferred release.
	require.NoError(t, os.Remove(f.leaf))
	f.leaf = f.cgroup(t, "leaf")
	f.write(t, d, f.leaf, 4096)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 2, 0, 90000)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)

	// Binding and detaching an unused loop must not discard other disks' IO.
	topology := f.disk(t)
	for _, bind := range []bool{false, true} {
		f.write(t, d, f.leaf, 4096)
		f.write(t, other, f.parking, 4096)
		if bind {
			f.attach(t, topology)
		} else {
			topology.unlock(t)
			runIOControlKernelCommand(t, f.ctx, 15*time.Second, "losetup", "--detach", topology.path)
		}
		metrics := s.update(t, d, other)
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
	metrics = s.update(t, d, other)
	requireIOCostKernelInterval(t, metrics, d.name, 1, 0, 90000)
	requireIOCostKernelInterval(t, metrics, other.name, 1, 0, 90000)
	s.stop(t)
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
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)

	// Fresh cgroups exercise owner growth. This cohort is deliberately serial,
	// unlike the preceding concurrent no-wait workload.
	d.rates(t, 65536, 16)
	var total time.Duration
	for index := range 64 {
		group := f.cgroup(t, fmt.Sprintf("short-%d", index))
		total += f.write(t, d, group, 4096)
	}
	lower, upper := ioCostKernelBounds(total / 64)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 64, lower, upper)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 0, 0, 0)

	f.condition(t, d, f.leaf, 4096, 1)
	s.update(t, d)
	elapsed := f.write(t, d, f.leaf, 4096)
	lower, upper = ioCostKernelBounds(elapsed)
	requireIOCostKernelInterval(t, s.update(t, d), d.name, 1, lower, upper)
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
	metrics := s.update(t, d, other)
	for _, disk := range []*ioCostKernelDisk{d, other} {
		requireIOCostKernelInterval(t, metrics, disk.name, 2, bounds[disk.name][0], bounds[disk.name][1])
	}
	metrics = s.update(t, d, other)
	for _, disk := range []*ioCostKernelDisk{d, other} {
		requireIOCostKernelInterval(t, metrics, disk.name, 0, 0, 0)
	}
}
