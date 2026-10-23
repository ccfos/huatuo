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

package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	promprocfs "github.com/prometheus/procfs"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/profiler/memray"
	pythonruntime "github.com/ccfos/huatuo/internal/profiler/runtime/python"
	targetsession "github.com/ccfos/huatuo/internal/profiler/target"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/internal/utils/netutil"
	"github.com/ccfos/huatuo/pkg/types"
)

type pythonMemoryTarget struct {
	pid        int
	bundlePath string
	identity   *memrayProcessIdentity
	labels     []types.ProfileLabel
}

type pythonMemoryProfiler struct {
	config       Config
	targets      []pythonMemoryTarget
	stackMode    memray.StackMode
	mergeThreads bool
}

const (
	memrayHeaderReadTimeout           = 10 * time.Second
	memrayShutdownGracePeriod         = 2 * time.Second
	maxMemrayControlResponseSizeBytes = 1 << 20
)

type memrayProcessIdentity struct {
	pid       int
	startTime uint64
	pidfd     int
	closeOnce sync.Once
}

func readMemrayProcessStartTime(pid int) (uint64, error) {
	process, err := promprocfs.NewProc(pid)
	if err != nil {
		return 0, err
	}
	stat, err := process.Stat()
	if err != nil {
		return 0, err
	}
	return stat.Starttime, nil
}

func captureMemrayProcessIdentity(pid int) (*memrayProcessIdentity, error) {
	identity := &memrayProcessIdentity{pid: pid, pidfd: -1}
	if pidfd, err := unix.PidfdOpen(pid, 0); err == nil {
		identity.pidfd = pidfd
	} else if errors.Is(err, unix.ESRCH) {
		return nil, fmt.Errorf("target PID %d exited before identity capture", pid)
	}

	startTime, err := readMemrayProcessStartTime(pid)
	if err != nil {
		identity.close()
		return nil, fmt.Errorf("read target PID %d start time: %w", pid, err)
	}
	identity.startTime = startTime
	if err := identity.validate(); err != nil {
		identity.close()
		return nil, err
	}
	return identity, nil
}

func (identity *memrayProcessIdentity) validate() error {
	if identity == nil {
		return errors.New("missing Memray target identity")
	}
	if identity.pidfd >= 0 {
		if err := unix.PidfdSendSignal(identity.pidfd, 0, nil, 0); err != nil {
			return fmt.Errorf("target PID %d is no longer running: %w", identity.pid, err)
		}
	}
	startTime, err := readMemrayProcessStartTime(identity.pid)
	if err != nil {
		return fmt.Errorf("revalidate target PID %d: %w", identity.pid, err)
	}
	if startTime != identity.startTime {
		return fmt.Errorf(
			"target PID %d identity changed: start time was %d, now %d",
			identity.pid,
			identity.startTime,
			startTime,
		)
	}
	return nil
}

func (identity *memrayProcessIdentity) close() {
	if identity == nil {
		return
	}
	identity.closeOnce.Do(func() {
		if identity.pidfd >= 0 {
			_ = unix.Close(identity.pidfd)
		}
	})
}

// An abstract Unix socket provides a cross-process lock without stale files or
// a writable /run directory. The target payload guards agents in other namespaces.
func (identity *memrayProcessIdentity) lockSession() (*net.UnixConn, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	pidNamespace, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return nil, fmt.Errorf("read profiler PID namespace: %w", err)
	}
	name := fmt.Sprintf("@huatuo-memray-%s-%d-%d", pidNamespace, identity.pid, identity.startTime)
	lock, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Net: "unixgram", Name: name})
	if err != nil {
		if errors.Is(err, unix.EADDRINUSE) {
			return nil, fmt.Errorf("Memray session already active for PID %d (start time %d)", identity.pid, identity.startTime)
		}
		return nil, fmt.Errorf("lock Memray session: %w", err)
	}
	if err := identity.validate(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func pyBoolLiteral(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

func closeMemrayConnectionOnContextDone(ctx context.Context, conn net.Conn) func() {
	stop := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	return func() {
		stop()
	}
}

func setMemrayHeaderReadDeadline(ctx context.Context, conn net.Conn) error {
	deadline := time.Now().Add(memrayHeaderReadTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return conn.SetReadDeadline(deadline)
}

// The shutdown bound must not depend on the flush loop reaching ctx.Done.
// Keep the connection open briefly for the target's final records and trailer.
func closeMemrayStreamOnContextDone(ctx context.Context, conn net.Conn, grace time.Duration) func() {
	finished := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			_ = conn.Close()
		}
	}()
	return func() {
		close(finished)
		<-done
	}
}

func targetHasDifferentNetNamespace(pid int) (bool, error) {
	selfNamespace, err := netutil.NetNamespaceInumByPID(os.Getpid())
	if err != nil {
		return false, fmt.Errorf("read profiler netns: %w", err)
	}
	targetNamespace, err := netutil.NetNamespaceInumByPID(pid)
	if err != nil {
		return false, fmt.Errorf("read target PID %d netns: %w", pid, err)
	}
	return selfNamespace != targetNamespace, nil
}

func dialLivePort(ctx context.Context, pid, port int, timeout time.Duration) (net.Conn, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	inTargetNetNS, err := targetHasDifferentNetNamespace(pid)
	if err != nil {
		return nil, err
	}
	if !inTargetNetNS {
		dialer := net.Dialer{Timeout: timeout}
		return dialer.DialContext(ctx, "tcp", addr)
	}
	return dialInNetNS(ctx, pid, addr, timeout)
}

func listenInNetNS(pid int, addr string) (net.Listener, error) {
	selfNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return nil, fmt.Errorf("open self netns: %w", err)
	}
	defer selfNS.Close()

	targetNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return nil, fmt.Errorf("open target netns: %w", err)
	}
	defer targetNS.Close()

	runtime.LockOSThread()
	restored := false
	defer func() {
		if restored {
			runtime.UnlockOSThread()
		}
	}()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		restored = true
		return nil, fmt.Errorf("setns to target: %w", err)
	}

	ln, err := net.Listen("tcp", addr)

	restoreErr := unix.Setns(int(selfNS.Fd()), unix.CLONE_NEWNET)
	if restoreErr != nil {
		if ln != nil {
			_ = ln.Close()
		}
		return nil, fmt.Errorf("restore netns: %w", restoreErr)
	}
	restored = true
	if err != nil {
		return nil, err
	}
	return ln, nil
}

func pickFreePortInNetNS(pid int) (int, error) {
	ln, err := listenInNetNS(pid, "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func dialInNetNS(ctx context.Context, pid int, addr string, timeout time.Duration) (net.Conn, error) {
	selfNS, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return nil, fmt.Errorf("open self netns: %w", err)
	}
	defer selfNS.Close()

	targetNS, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return nil, fmt.Errorf("open target netns: %w", err)
	}
	defer targetNS.Close()

	runtime.LockOSThread()
	restored := false
	defer func() {
		if restored {
			runtime.UnlockOSThread()
		}
	}()

	if err := unix.Setns(int(targetNS.Fd()), unix.CLONE_NEWNET); err != nil {
		restored = true
		return nil, fmt.Errorf("setns to target: %w", err)
	}

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)

	restoreErr := unix.Setns(int(selfNS.Fd()), unix.CLONE_NEWNET)
	if restoreErr != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, fmt.Errorf("restore netns: %w", restoreErr)
	}
	restored = true
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// invokeInjector loads memray _inject*.so into target via gdb and schedules memray client on the main thread.
// It returns the combined gdb output for diagnostics.
func invokeInjector(
	ctx context.Context,
	identity *memrayProcessIdentity,
	hostPythonDir string,
	injectorPath string,
	ctrlPort int,
) (string, string, error) {
	if err := identity.validate(); err != nil {
		return "", "", err
	}
	pid := identity.pid
	targetInjector := injectorPath
	scriptPath := filepath.Join(hostPythonDir, "memray", "commands", "_attach.gdb")
	useScheduleDirect := false
	if version, ok, err := pythonruntime.DetectTargetPythonVersion(pid); err != nil {
		return "", "", fmt.Errorf("detect target python version: %w", err)
	} else if !ok || (version.Major == 3 && version.Minor <= 6) {
		useScheduleDirect = true
	}
	scheduleSymbol := "memray_spawn_client"
	if useScheduleDirect {
		scheduleSymbol = "memray_schedule_client_direct"
	}

	origContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return "", scheduleSymbol, fmt.Errorf("read gdb script: %w", err)
	}
	adapted := adaptMemrayInjectorScript(string(origContent), scheduleSymbol)

	tmpScript, err := os.CreateTemp("", "memray_attach_schedule_*.gdb")
	if err != nil {
		return "", scheduleSymbol, fmt.Errorf("create temp gdb script: %w", err)
	}
	defer os.Remove(tmpScript.Name())
	if _, err := tmpScript.WriteString(adapted); err != nil {
		tmpScript.Close()
		return "", scheduleSymbol, fmt.Errorf("write temp gdb script: %w", err)
	}
	tmpScript.Close()
	scriptPath = tmpScript.Name()

	args := []string{
		"-batch",
		"-p", strconv.Itoa(pid),
		"-nx",
		"-nw",
		"-iex=set auto-solib-add off",
		fmt.Sprintf("-ex=set $rtld_now=(int)%d", gdbRTLDNow),
		fmt.Sprintf("-ex=set $target_libpath=%q", targetInjector),
		fmt.Sprintf("-ex=set $port=%d", ctrlPort),
		fmt.Sprintf("-x=%s", scriptPath),
	}

	if err := identity.validate(); err != nil {
		return "", scheduleSymbol, err
	}
	res, runErr := executil.Run(ctx, executil.Spec{Path: "gdb", Args: args})
	output := string(res.Stdout) + string(res.Stderr)
	if runErr != nil {
		if errors.Is(runErr, executil.ErrRunCanceled) {
			return output, scheduleSymbol, ctx.Err()
		}
		return output, scheduleSymbol, fmt.Errorf("run GDB injector: %w", runErr)
	}

	// The gdb script prints either "SUCCESS" or "FAILURE" when it reaches the injection breakpoint.
	// If neither appears, we likely never hit the breakpoint (idle target) or the script aborted early.
	if !strings.Contains(output, "MEMRAY: Attached to process.") {
		return output, scheduleSymbol, fmt.Errorf("gdb injector missing attach banner; output: %s", tailLines(output, 40))
	}
	if !strings.Contains(output, "SUCCESS") && !strings.Contains(output, "FAILURE") {
		return output, scheduleSymbol, fmt.Errorf("gdb injector did not reach injection breakpoint; output: %s", tailLines(output, 60))
	}
	if strings.Contains(output, "FAILURE") {
		return output, scheduleSymbol, fmt.Errorf("gdb injector reported failure; output: %s", tailLines(output, 60))
	}

	// Defer to live socket connection to confirm memray started and bound its live socket.
	return output, scheduleSymbol, nil
}

func adaptMemrayInjectorScript(original, scheduleSymbol string) string {
	adapted := original
	// In container scenarios gdb may fail to read symbols from the injected .so (mount namespaces),
	// so avoid relying on `sharedlibrary <path>` + direct symbol calls. Use dlsym() on the dlopen()
	// handle to resolve the injection function inside the target process.
	adapted = strings.ReplaceAll(adapted, "call (void*)dlopen($libpath, $rtld_now)", "set $handle = (void*)dlopen($target_libpath, $rtld_now)")
	adapted = strings.ReplaceAll(adapted, "eval \"sharedlibrary %s\", $libpath", "")
	adapted = strings.ReplaceAll(
		adapted,
		`p (int)memray_spawn_client($port) ? "FAILURE" : "SUCCESS"`,
		fmt.Sprintf(
			`set $fn = (void*)dlsym($handle, "%s")
    p (char*)dlerror()
    if $fn == 0
        p "FAILURE"
    else
        p ((int (*)(int))$fn)($port) ? "FAILURE" : "SUCCESS"
    end`,
			scheduleSymbol,
		),
	)
	return adapted
}

func tailLines(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func bestEffortProcessName(pid int) string {
	const fallback = "python"
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return fallback
	}
	base := filepath.Base(exe)
	if !hasExecutablePrefix(base, "python") {
		return fallback
	}

	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(cmdline) == 0 {
		return fallback
	}
	parts := bytesSplitNul(cmdline)
	if len(parts) < 2 {
		return fallback
	}
	for _, arg := range parts[1:] {
		if len(arg) == 0 {
			continue
		}
		if arg[0] == '-' {
			continue
		}
		return string(arg)
	}
	return fallback
}

func bytesSplitNul(b []byte) [][]byte {
	var res [][]byte
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				res = append(res, b[start:i])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		res = append(res, b[start:])
	}
	return res
}

func memrayCancelPath(pid int) string {
	return fmt.Sprintf(
		"/tmp/.huatuo-memray-cancel-%d-%d",
		pid,
		time.Now().UnixNano(),
	)
}

func signalMemrayCancellation(pid int, targetPath string) error {
	root, err := os.OpenRoot(fmt.Sprintf("/proc/%d/root", pid))
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile(strings.TrimPrefix(targetPath, "/"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

// runMemraySession starts memray attach (socket destination), reads the stream,
// and emits per-interval deltas to recordFn.
func (p *pythonMemoryProfiler) runMemraySession(
	identity *memrayProcessIdentity,
	ctx context.Context,
	hostPythonDir string,
	injectorPath string,
	pythonPath string,
	metric string,
	recordFn func([]string) error,
) error {
	if err := identity.validate(); err != nil {
		return err
	}
	pid := identity.pid
	inTargetNetNS, err := targetHasDifferentNetNamespace(pid)
	if err != nil {
		return err
	}

	// Prepare control server for memray injector side channel.
	var ctrlLn net.Listener
	if inTargetNetNS {
		if err := identity.validate(); err != nil {
			return err
		}
		ctrlLn, err = listenInNetNS(pid, "127.0.0.1:0")
	} else {
		ctrlLn, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return fmt.Errorf("listen control channel: %w", err)
	}
	defer ctrlLn.Close()

	ctrlPort := ctrlLn.Addr().(*net.TCPAddr).Port
	if ctrlPort == 0 {
		return fmt.Errorf("listen control channel: invalid port")
	}

	// Find a free port for SocketDestination (using helper).
	var livePort int
	if inTargetNetNS {
		if err := identity.validate(); err != nil {
			return err
		}
		livePort, err = pickFreePortInNetNS(pid)
	} else {
		var liveLn net.Listener
		liveLn, err = net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			livePort = liveLn.Addr().(*net.TCPAddr).Port
			liveLn.Close() // memray will bind; we only used it to pick the port.
		}
	}
	if err != nil {
		return fmt.Errorf("listen data channel: %w", err)
	}
	if livePort == 0 {
		return fmt.Errorf("listen data channel: invalid port")
	}

	log.Debugf(
		"memray attach pid=%d ctrlPort=%d livePort=%d differentNetNS=%v",
		pid,
		ctrlPort,
		livePort,
		inTargetNetNS,
	)

	// Build payload for the target process: Tracker with SocketDestination.
	stackMode := p.stackMode
	nativeTraces := stackMode != memray.StackModePython
	trackerCall := fmt.Sprintf(
		"memray.Tracker(destination=memray.SocketDestination(server_port=%d), native_traces=%v, follow_fork=%v, trace_python_allocators=%v)",
		livePort,
		pyBoolLiteral(nativeTraces),
		pyBoolLiteral(false),
		pyBoolLiteral(false),
	)

	if pythonPath == "" {
		return fmt.Errorf("empty memray python path")
	}
	if p.config.Duration < 0 {
		return fmt.Errorf("invalid duration: %s", p.config.Duration)
	}

	cancelTargetPath := memrayCancelPath(pid)
	cancelRequested := false
	sessionFinished := false
	trackerMayBeActive := false
	cancelTracker := func() {
		if cancelRequested {
			return
		}
		cancelRequested = true
		if err := identity.validate(); err != nil {
			log.Debugf("skip memray cancellation for stale PID %d: %v", pid, err)
			return
		}
		if err := signalMemrayCancellation(pid, cancelTargetPath); err != nil {
			log.Debugf("signal memray cancellation pid=%d: %v", pid, err)
			return
		}
	}
	defer func() {
		if !sessionFinished && (ctx.Err() != nil || trackerMayBeActive) {
			cancelTracker()
			return
		}
		if identity.validate() == nil {
			if root, err := os.OpenRoot(fmt.Sprintf("/proc/%d/root", pid)); err == nil {
				_ = root.Remove(strings.TrimPrefix(cancelTargetPath, "/"))
				_ = root.Close()
			}
		}
	}()
	payload := buildAttachPayload(
		trackerCall, int(p.config.Duration/time.Second), []string{pythonPath}, cancelTargetPath, livePort,
	)

	// Spawn goroutine to accept control channel and deliver payload.
	ctrlErrCh := make(chan error, 1)
	ctrlDoneCh := make(chan struct{})
	var ctrlConnMu sync.Mutex
	var ctrlConn net.Conn
	ctrlClosing := false
	closeControlChannel := func() {
		ctrlConnMu.Lock()
		ctrlClosing = true
		conn := ctrlConn
		ctrlConnMu.Unlock()

		_ = ctrlLn.Close()
		if conn != nil {
			_ = conn.Close()
		}
	}
	stopControlClose := context.AfterFunc(ctx, closeControlChannel)
	defer func() {
		stopControlClose()
		closeControlChannel()
		<-ctrlDoneCh
	}()
	go func() {
		defer close(ctrlDoneCh)
		conn, err := ctrlLn.Accept()
		if err != nil {
			ctrlErrCh <- err
			return
		}
		ctrlConnMu.Lock()
		if ctrlClosing {
			ctrlConnMu.Unlock()
			_ = conn.Close()
			ctrlErrCh <- net.ErrClosed
			return
		}
		ctrlConn = conn
		ctrlConnMu.Unlock()
		defer func() {
			_ = conn.Close()
			ctrlConnMu.Lock()
			if ctrlConn == conn {
				ctrlConn = nil
			}
			ctrlConnMu.Unlock()
		}()
		log.Debugf("memray control channel connected pid=%d", pid)
		if _, err := conn.Write([]byte(payload)); err != nil {
			ctrlErrCh <- err
			return
		}
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			ctrlErrCh <- err
			return
		}
		log.Debugf("memray control payload sent pid=%d", pid)
		limited := &io.LimitedReader{R: conn, N: maxMemrayControlResponseSizeBytes + 1}
		resp, err := io.ReadAll(limited)
		if err != nil {
			ctrlErrCh <- err
			return
		}
		if len(resp) > maxMemrayControlResponseSizeBytes {
			ctrlErrCh <- fmt.Errorf(
				"memray control response exceeds %d bytes",
				maxMemrayControlResponseSizeBytes,
			)
			return
		}
		if len(resp) > 0 {
			ctrlErrCh <- fmt.Errorf("memray control response: %s", resp)
			return
		}
		ctrlErrCh <- nil
	}()

	// Kick off injector via memray _inject*.so using gdb attach (async).
	gdbOutput, scheduleSymbol, err := invokeInjector(ctx, identity, hostPythonDir, injectorPath, ctrlPort)
	if err != nil {
		return err
	}
	trackerMayBeActive = true
	log.Debugf("memray gdb injector result pid=%d schedule=%s", pid, scheduleSymbol)
	log.Debugf("memray gdb injector output (tail):\n%s", tailLines(gdbOutput, 20))

	select {
	case err := <-ctrlErrCh:
		if err != nil {
			return fmt.Errorf("control channel error: %w", err)
		}
	default:
	}

	// Connect to live data socket and decode memray stream.
	var dataConn net.Conn
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-ctrlErrCh:
			if err != nil {
				return fmt.Errorf("control channel error: %w", err)
			}
		default:
		}
		if err := identity.validate(); err != nil {
			return err
		}
		dataConn, err = dialLivePort(ctx, pid, livePort, 2*time.Second)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err != nil {
		return fmt.Errorf("dial live port: %w, gdb output: %s", err, gdbOutput)
	}
	log.Debugf("memray live socket connected pid=%d port=%d", pid, livePort)
	defer dataConn.Close()
	stopHeaderContextClose := closeMemrayConnectionOnContextDone(ctx, dataConn)
	defer stopHeaderContextClose()

	select {
	case err := <-ctrlErrCh:
		if err != nil {
			return fmt.Errorf("control channel error: %w", err)
		}
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	reader := bufio.NewReader(dataConn)
	interval := p.config.AggregationInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if err := setMemrayHeaderReadDeadline(ctx, dataConn); err != nil {
		return fmt.Errorf("set Memray header read deadline: %w", err)
	}

	dec, header, err := memray.NewStreamDecoder(reader, memray.Options{
		MergeThreads:    p.mergeThreads,
		Separator:       ";",
		Metric:          metric,
		StackMode:       stackMode,
		NativeSymbolPID: int32(pid),
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("decode memray stream: %w", err)
	}
	// Header reads are canceled by closing the connection immediately. Once the
	// stream is established, session cancellation first gives the target time to
	// emit its trailer before the connection is force-closed.
	stopHeaderContextClose()
	if err := dataConn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear Memray stream read deadline: %w", err)
	}
	stopStreamContextClose := closeMemrayStreamOnContextDone(ctx, dataConn, memrayShutdownGracePeriod)
	defer stopStreamContextClose()
	log.Debugf(
		"memray stream header pid=%d fileFormat=%d nativeTraces=%v tracePyAlloc=%v mainTid=%d skipFrames=%d pyver=%d",
		header.Pid,
		header.FileFormat,
		header.NativeTraces,
		header.TracePyAlloc,
		header.MainTid,
		header.SkipFrames,
		header.PythonVersion,
	)
	if stackMode != memray.StackModePython && !header.NativeTraces {
		return errors.New("native/hybrid stacks requested but the stream has native traces disabled")
	}

	errCh := make(chan error, 1)
	readerDone := make(chan struct{})
	defer func() {
		_ = dataConn.Close()
		<-readerDone
	}()
	go func() {
		defer close(readerDone)
		for {
			ok, err := dec.NextRecord()
			if err != nil {
				errCh <- err
				return
			}
			if !ok {
				// Keep successful completion distinct from every decoder error.
				errCh <- nil
				return
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	flush := func() error {
		lines := dec.FlushDelta("")
		if len(lines) == 0 {
			return nil
		}
		return recordFn(lines)
	}

	ctxDone := ctx.Done()
	for {
		select {
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		case err := <-errCh:
			// The trailer confirms that pending frees were drained. A watchdog
			// close bounds shutdown, but cannot certify a retained-memory result.
			if err != nil {
				return fmt.Errorf("incomplete Memray capture before trailer: %w", err)
			}
			sessionFinished = true
			if ctx.Err() != nil && !profile.IsNormalStop(ctx) {
				return ctx.Err()
			}
			if err := flush(); err != nil {
				return err
			}
			ra, rf := dec.SkippedRanged()
			if bad := dec.BadParents(); bad > 0 {
				log.Debugf("memray malformed parent frames=%d pid=%d", bad, pid)
			}
			if ra > 0 || rf > 0 {
				log.Debugf("memray skipped ranged allocs=%d frees=%d pid=%d", ra, rf, pid)
			}
			return nil
		case <-ctxDone:
			cancelTracker()
			// Drain through the same completion path; the independent watchdog
			// closes a stalled stream even if flushing is temporarily blocked.
			ctxDone = nil
		}
	}
}

// buildAttachPayload renders the Python payload executed inside the target.
func buildAttachPayload(
	trackerCall string,
	duration int,
	pythonPaths []string,
	cancelPath string,
	livePort int,
) string {
	dur := "0"
	if duration > 0 {
		dur = strconv.Itoa(duration)
	}
	return fmt.Sprintf(
		attachPayloadTemplate,
		pythonPathsLiteral(pythonPaths),
		pythonStringLiteral(cancelPath),
		livePort,
		trackerCall,
		dur,
	)
}

// pythonPathsLiteral builds a Python list literal from paths.
func pythonPathsLiteral(paths []string) string {
	if len(paths) == 0 {
		return "[]"
	}
	encoded, _ := json.Marshal(paths)
	return string(encoded)
}

func pythonStringLiteral(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// Use RTLD_NOW | RTLD_GLOBAL to satisfy glibc; plain RTLD_NOW hit EINVAL in some environments.
const gdbRTLDNow = 0x102

// Each injection owns its tracker and timers. The target-side guard also covers
// profilers in different network namespaces, which do not share socket locks.
const attachPayloadTemplate = `
import atexit
import os
import socket
import sys
import threading
import time
from contextlib import suppress
PYTHON_PATHS = %s
CANCEL_PATH = %s
LIVE_PORT = %d
CONNECTION_TIMEOUT = 10
for _p in PYTHON_PATHS or []:
    if _p and _p not in sys.path:
        sys.path.insert(0, _p)
import memray

class BareExceptionMessage(Exception):
    def __repr__(self):
        return self.args[0]

class RepeatingTimer(threading.Thread):
    def __init__(self, interval, function):
        self._interval = interval
        self._function = function
        self._canceled = threading.Event()
        super().__init__()

    def cancel(self):
        self._canceled.set()

    def run(self):
        while not self._canceled.wait(self._interval):
            if self._function():
                break

class AttachSession:
    def __init__(self):
        self.lock = memray.__dict__.setdefault("_huatuo_attach_lock", threading.RLock())
        self.cancel_path = CANCEL_PATH
        self.tracker = None
        self.threads = []

    def deactivate(self):
        with self.lock:
            if getattr(memray, "_huatuo_attach_owner", None) is not self:
                return
            try:
                print("memray: Deactivating tracking", file=sys.stderr)
            except Exception:
                pass
            try:
                self.tracker.__exit__(None, None, None)
            finally:
                memray._last_tracker = None
                memray._huatuo_attach_owner = None
                self.tracker = None
                for thread in self.threads:
                    thread.cancel()
                self.threads = []
                atexit.unregister(self.deactivate)

    def cancel_if_requested(self):
        if not os.path.exists(self.cancel_path):
            return False
        with suppress(OSError):
            os.unlink(self.cancel_path)
        self.deactivate()
        return True

    def activate(self, duration):
        if not self.lock.acquire(blocking=False):
            raise BareExceptionMessage("Memray session already active for this process")
        try:
            if (getattr(memray, "_last_tracker", None) is not None or
                    getattr(memray, "_huatuo_attach_owner", None) is not None):
                raise BareExceptionMessage("Memray session already active for this process")
            # A previous profiler may have exited before its payload ran.
            if self.cancel_path and os.path.exists(self.cancel_path):
                with suppress(OSError):
                    os.unlink(self.cancel_path)
                raise BareExceptionMessage("Memray session canceled before activation")
            memray._huatuo_attach_owner = self
            startup_done = threading.Event()
            startup_aborted = threading.Event()
            deadline = time.monotonic() + CONNECTION_TIMEOUT

            def cancel_pending_connection():
                while not startup_done.wait(0.05):
                    if (not startup_aborted.is_set() and
                            not os.path.exists(self.cancel_path) and
                            time.monotonic() < deadline):
                        continue
                    startup_aborted.set()
                    # SocketDestination blocks in accept() before __enter__.
                    # Wake it without acquiring the activation lock. Retry
                    # until construction finishes, including late listeners.
                    with suppress(OSError):
                        with socket.create_connection(("127.0.0.1", LIVE_PORT), timeout=0.2):
                            pass

            startup_watcher = threading.Thread(target=cancel_pending_connection, daemon=True)
            startup_watcher.start()
            try:
                tracker = %s
            finally:
                startup_done.set()
                startup_watcher.join()
            if startup_aborted.is_set() or os.path.exists(self.cancel_path):
                with suppress(OSError):
                    os.unlink(self.cancel_path)
                # Never install allocator hooks for the cleanup connection.
                del tracker
                raise BareExceptionMessage("Memray connection canceled or timed out before activation")
            tracker.__enter__()
            self.tracker = tracker
            memray._last_tracker = tracker
            memray._huatuo_attach_owner = self
            try:
                atexit.register(self.deactivate)
                if duration > 0:
                    timer = threading.Timer(duration, self.deactivate)
                    timer.daemon = True
                    self.threads.append(timer)
                if self.cancel_path:
                    watcher = RepeatingTimer(0.2, self.cancel_if_requested)
                    watcher.daemon = True
                    self.threads.append(watcher)
                for thread in self.threads:
                    thread.start()
            except Exception:
                self.deactivate()
                raise
            try:
                print("memray: Tracking started", file=sys.stderr)
            except Exception:
                pass
        finally:
            if self.tracker is None and getattr(memray, "_huatuo_attach_owner", None) is self:
                memray._huatuo_attach_owner = None
            self.lock.release()

session = AttachSession()
session.activate(%s)
`

func (p *pythonMemoryProfiler) closeTargetIdentities() {
	for _, target := range p.targets {
		target.identity.close()
	}
	p.targets = nil
}

func (p *pythonMemoryProfiler) runMemrayTarget(
	ctx context.Context,
	target pythonMemoryTarget,
	recordFn func([]string) error,
) error {
	if err := target.identity.validate(); err != nil {
		return err
	}
	ownership, err := target.identity.lockSession()
	if err != nil {
		return err
	}
	defer ownership.Close()
	hostPythonDir, version, known, err := pythonruntime.ResolveMemrayPythonPath(
		target.pid,
		target.bundlePath,
	)
	if err != nil {
		return fmt.Errorf("resolve Memray runtime: %w", err)
	}

	injectorName, err := pythonruntime.SelectMemrayInjector(
		hostPythonDir,
		version,
		known,
	)
	if err != nil {
		return fmt.Errorf("select Memray injector: %w", err)
	}
	if err := target.identity.validate(); err != nil {
		return err
	}
	containerPythonDir, err := pythonruntime.EnsureMemrayPython(
		target.pid,
		hostPythonDir,
		"",
		injectorName,
		version,
		known,
	)
	if err != nil {
		return fmt.Errorf("prepare Memray runtime: %w", err)
	}

	if err := p.runMemraySession(
		target.identity,
		ctx,
		hostPythonDir,
		filepath.Join(containerPythonDir, "memray", injectorName),
		containerPythonDir,
		"bytes",
		recordFn,
	); err != nil {
		// RunSessions treats bare context.Canceled as a completed target. An
		// interrupted startup has no complete capture, even on a normal stop.
		return fmt.Errorf("collect Memray session: %w", err)
	}
	return nil
}

func (p *pythonMemoryProfiler) Start(ctx context.Context) error {
	if _, err := exec.LookPath("gdb"); err != nil {
		return fmt.Errorf("Python memory profiling requires gdb in PATH: %w", err)
	}
	if p.config.Duration <= 0 || p.config.Duration%time.Second != 0 {
		return fmt.Errorf("Python memory profiling duration must be positive whole seconds: %s", p.config.Duration)
	}
	p.closeTargetIdentities()
	p.mergeThreads = p.config.PythonMemoryMergeThreads
	stackMode, err := parsePythonMemoryStackMode(p.config.PythonMemoryStackMode)
	if err != nil {
		return err
	}
	p.stackMode = stackMode
	bundlePath, err := pythonruntime.ResolveMemrayBundlePath(p.config.ToolDir)
	if err != nil {
		return err
	}
	pids, err := resolvePythonMemoryPIDs(&p.config)
	if err != nil {
		return err
	}
	if err := validateResolvedPIDs("Python", pids); err != nil {
		return err
	}
	// Every target holds a slot for the shared collection window. Queueing
	// extra targets could omit them entirely while reporting a successful job.
	if err := validateMaxProfilerProcesses("Python", pids, p.config.MaxProfilerProcesses); err != nil {
		return err
	}
	if len(p.config.PIDs) > 0 {
		if err := validateProcessExecutables("Python", "python", pids); err != nil {
			return err
		}
		if err := validateExpectedExecPath(pids, p.config.ExecPath); err != nil {
			return err
		}
	}
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			p.closeTargetIdentities()
			return err
		}
		identity, err := captureMemrayProcessIdentity(pid)
		if err != nil {
			p.closeTargetIdentities()
			return err
		}
		labels, err := readProcessLabels(pid)
		if err == nil {
			labels = append(labels, types.ProfileStringLabel("python_entry", bestEffortProcessName(pid)))
			labels, err = types.NormalizeLabels(labels)
		}
		if err != nil {
			identity.close()
			p.closeTargetIdentities()
			return fmt.Errorf("read Python memory process %d metadata: %w", pid, err)
		}
		p.targets = append(p.targets, pythonMemoryTarget{
			pid:        pid,
			bundlePath: bundlePath,
			identity:   identity,
			labels:     labels,
		})
	}
	return nil
}

func (p *pythonMemoryProfiler) ReadDataLoop(
	ctx context.Context,
	enqueue func(*pipeline.SampleBatch) error,
) error {
	if len(p.targets) == 0 {
		return errors.New("read Python memory profile: profiler is not started")
	}
	defer p.closeTargetIdentities()
	sessions := make([]targetsession.Session, 0, len(p.targets))
	completed := make([]bool, len(p.targets))
	for index, target := range p.targets {
		sessions = append(sessions, targetsession.Session{
			PID: target.pid,
			Run: func(ctx context.Context) error {
				err := p.runMemrayTarget(
					ctx,
					target,
					func(lines []string) error {
						return enqueueMemrayDeltas(lines, target.labels, enqueue)
					},
				)
				completed[index] = err == nil
				return err
			},
		})
	}
	err := targetsession.RunSessions(ctx, sessions, p.config.MaxProfilerProcesses)
	if ctx.Err() != nil && !profile.IsNormalStop(ctx) {
		return errors.Join(err, context.Cause(ctx))
	}
	if err == nil {
		// Cancellation may prevent RunSessions from starting a selected PID.
		// Its missing trailer must not become a successful partial profile.
		for index, complete := range completed {
			if !complete {
				return fmt.Errorf("incomplete Memray capture for PID %d: session did not complete", sessions[index].PID)
			}
		}
	}
	return err
}

func (p *pythonMemoryProfiler) Stop() error {
	p.closeTargetIdentities()
	return nil
}

func resolvePythonMemoryPIDs(cfg *Config) ([]int, error) {
	if len(cfg.PIDs) > 0 {
		return cfg.PIDs, nil
	}
	cgroupPath, err := pod.ContainerCgroupPathByID(cfg.ContainerID)
	if err != nil {
		return nil, err
	}
	groups, err := findProcessesInCgroups(cgroupPath, executableFilter{
		ExecutablePrefix: "python",
		ExecutablePath:   cfg.ExecPath,
	})
	if err != nil {
		return nil, err
	}
	// Memray does not follow child processes, so attach to every matching PID.
	var pids []int
	for _, members := range groups {
		pids = append(pids, members...)
	}
	slices.Sort(pids)
	if len(pids) == 0 {
		return nil, fmt.Errorf("no Python processes found in container %q", cfg.ContainerID)
	}
	return pids, nil
}

func parsePythonMemoryStackMode(value string) (memray.StackMode, error) {
	switch value {
	case "", "python":
		return memray.StackModePython, nil
	case "native":
		return memray.StackModeNative, nil
	case "hybrid":
		return memray.StackModeHybrid, nil
	default:
		return memray.StackModePython, fmt.Errorf(
			"invalid Python memory stack mode %q",
			value,
		)
	}
}
