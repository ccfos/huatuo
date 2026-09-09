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
	"strconv"
	"strings"
	"sync"
	"time"

	promprocfs "github.com/prometheus/procfs"
	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/memray"
	"huatuo-bamai/internal/profiler/registry"
	pythonruntime "huatuo-bamai/internal/profiler/runtime/python"
	targetsession "huatuo-bamai/internal/profiler/target"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/internal/utils/netutil"
	"huatuo-bamai/pkg/profiling"
)

type pythonMemoryTarget struct {
	pid        int
	bundlePath string
	identity   *memrayProcessIdentity
}

type pythonMemoryProfiler struct {
	pctx         *pcontext.ProfilerContext
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

func init() {
	impl := &pythonMemoryProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeMemory,
		Implementation: profiling.ImplementationPython,
		Description:    "Python memory profiler using Memray",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
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
	res := executil.ExecCmd(ctx, pid, "gdb", args...)
	output := string(res.Stdout) + string(res.Stderr)

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
	if !strings.HasPrefix(base, "python") {
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

func memrayCancelPath(pid int) (targetPath, hostPath string) {
	targetPath = fmt.Sprintf(
		"/tmp/.huatuo-memray-cancel-%d-%d",
		pid,
		time.Now().UnixNano(),
	)
	return targetPath, fmt.Sprintf("/proc/%d/root%s", pid, targetPath)
}

func signalMemrayCancellation(hostPath string) error {
	file, err := os.OpenFile(hostPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
	recordFn func(profiler.SampleOutput),
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
	if p.pctx.Duration < 0 {
		return fmt.Errorf("invalid duration: %d", p.pctx.Duration)
	}

	cancelTargetPath, cancelHostPath := memrayCancelPath(pid)
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
		if err := signalMemrayCancellation(cancelHostPath); err != nil {
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
			_ = os.Remove(cancelHostPath)
		}
	}()
	payload := buildAttachPayload(
		trackerCall, p.pctx.Duration, []string{pythonPath}, cancelTargetPath,
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
	interval := time.Duration(p.pctx.AggrInterval) * time.Second
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

	// Build header frame: process pid:name (best-effort)
	headerFrame := fmt.Sprintf("process %d:%s", pid, bestEffortProcessName(pid))

	errCh := make(chan error, 1)
	go func() {
		for {
			ok, err := dec.NextRecord()
			if err != nil {
				errCh <- err
				return
			}
			if !ok {
				errCh <- io.EOF
				return
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	flush := func() {
		lines := dec.FlushDelta(headerFrame)
		if len(lines) == 0 {
			return
		}
		recordFn(profiler.SampleOutput{
			PID:    pid,
			Output: strings.Join(lines, "\n"),
		})
	}

	for {
		select {
		case <-ticker.C:
			flush()
		case err := <-errCh:
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("decode memray stream: %w", err)
			}
			sessionFinished = true
			flush()
			ra, rf := dec.SkippedRanged()
			if bad := dec.BadParents(); bad > 0 {
				log.Debugf("memray malformed parent frames=%d pid=%d", bad, pid)
			}
			if ra > 0 || rf > 0 {
				log.Debugf("memray skipped ranged allocs=%d frees=%d pid=%d", ra, rf, pid)
			}
			return nil
		case <-ctx.Done():
			cancelTracker()
			select {
			case err := <-errCh:
				if err != nil && !errors.Is(err, io.EOF) {
					log.Debugf("memray reader exit after cancel: %v", err)
				}
			case <-time.After(memrayShutdownGracePeriod):
				_ = dataConn.Close()
				log.Debugf("memray reader did not exit within timeout after cancel")
			}
			_ = dataConn.Close()
			flush()
			return ctx.Err()
		}
	}
}

// buildAttachPayload renders the Python payload executed inside the target.
func buildAttachPayload(
	trackerCall string,
	duration int,
	pythonPaths []string,
	cancelPath string,
) string {
	mode := "ACTIVATE"
	dur := "0"
	if duration > 0 {
		mode = "FOR_DURATION"
		dur = strconv.Itoa(duration)
	}
	return fmt.Sprintf(
		attachPayloadTemplate,
		pythonPathsLiteral(pythonPaths),
		pythonStringLiteral(cancelPath),
		trackerCall,
		mode,
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

// TODO: Coordinate Memray ownership across profiler processes using the target
// PID and process start time. A second attach currently replaces the
// process-global tracker below and can terminate an active session.
const attachPayloadTemplate = `
import atexit
import os
import sys
import threading
from contextlib import suppress
PYTHON_PATHS = %s
CANCEL_PATH = %s
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

def deactivate_last_tracker():
    tracker = getattr(memray, "_last_tracker", None)
    if not tracker:
        return
    try:
        print("memray: Deactivating tracking", file=sys.stderr)
    except Exception:
        pass
    memray._last_tracker = None
    try:
        tracker.__exit__(None, None, None)
    finally:
        del tracker
    for thread in memray.__dict__.pop("_attach_event_threads", []):
        thread.cancel()

def activate_tracker():
    deactivate_last_tracker()
    try:
        print("memray: Activating tracking", file=sys.stderr)
    except Exception:
        pass
    tracker = %s
    try:
        tracker.__enter__()
        try:
            print("memray: Tracking started", file=sys.stderr)
        except Exception:
            pass
        memray._last_tracker = tracker
    finally:
        del tracker
    memray._attach_event_threads = []

def track_for_duration(duration=5):
    try:
        print("memray: Tracking for", duration, "seconds", file=sys.stderr)
    except Exception:
        pass
    activate_tracker()
    def deactivate_because_timer_elapsed():
        deactivate_last_tracker()
    thread = threading.Timer(duration, deactivate_because_timer_elapsed)
    thread.daemon = False
    thread.start()
    memray._attach_event_threads.append(thread)

def deactivate_if_cancel_requested():
    if not os.path.exists(CANCEL_PATH):
        return False
    with suppress(OSError):
        os.unlink(CANCEL_PATH)
    deactivate_last_tracker()
    return True

def start_cancel_watcher():
    if not CANCEL_PATH:
        return
    thread = RepeatingTimer(0.2, deactivate_if_cancel_requested)
    thread.daemon = True
    thread.start()
    memray._attach_event_threads.append(thread)

if not hasattr(memray, "_last_tracker"):
    atexit.register(deactivate_last_tracker)

mode = %q
if mode == "ACTIVATE":
    activate_tracker()
elif mode == "DEACTIVATE":
    if not getattr(memray, "_last_tracker", None):
        raise BareExceptionMessage("no previous memray attach call detected")
    deactivate_last_tracker()
elif mode == "FOR_DURATION":
    track_for_duration(%s)

if mode in ("ACTIVATE", "FOR_DURATION"):
    start_cancel_watcher()
`

func (p *pythonMemoryProfiler) NewAggregator(
	pctx *pcontext.ProfilerContext,
) (aggregator.Aggregator, error) {
	return newPythonMemoryAggregator(pctx)
}

func (p *pythonMemoryProfiler) closeTargetIdentities() {
	for _, target := range p.targets {
		target.identity.close()
	}
	p.targets = nil
}

func (p *pythonMemoryProfiler) runMemrayTarget(
	ctx context.Context,
	target pythonMemoryTarget,
	recordFn func(profiler.SampleOutput),
) error {
	if err := target.identity.validate(); err != nil {
		return err
	}
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

	return p.runMemraySession(
		target.identity,
		ctx,
		hostPythonDir,
		filepath.Join(containerPythonDir, "memray", injectorName),
		containerPythonDir,
		"bytes",
		recordFn,
	)
}

func (p *pythonMemoryProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if _, err := exec.LookPath("gdb"); err != nil {
		return fmt.Errorf("Python memory profiling requires gdb in PATH: %w", err)
	}

	p.closeTargetIdentities()
	p.pctx = pctx
	p.mergeThreads = pctx.PythonMemoryMergeThreads

	stackMode, err := parsePythonMemoryStackMode(pctx.PythonMemoryStackMode)
	if err != nil {
		return err
	}
	p.stackMode = stackMode

	bundlePath, err := pythonruntime.ResolveMemrayBundlePath(pctx.ToolPath)
	if err != nil {
		return err
	}

	pids, err := resolvePythonPids(pctx)
	if err != nil {
		return err
	}
	if err := validateResolvedPIDs("Python", pids); err != nil {
		return err
	}
	for _, pid := range pids {
		identity, err := captureMemrayProcessIdentity(pid)
		if err != nil {
			p.closeTargetIdentities()
			return err
		}
		p.targets = append(p.targets, pythonMemoryTarget{
			pid:        pid,
			bundlePath: bundlePath,
			identity:   identity,
		})
	}
	if len(pctx.PIDs) > 0 {
		if err := validateProcessExecutables("Python", "python", pids); err != nil {
			p.closeTargetIdentities()
			return err
		}
		if err := validateExpectedExecPath(pids, pctx.ExecPath); err != nil {
			p.closeTargetIdentities()
			return err
		}
	}

	return nil
}

func (p *pythonMemoryProfiler) ReadDataLoop(
	ctx context.Context,
	enqueue func(any),
) error {
	if len(p.targets) == 0 {
		return errors.New("read Python memory profile: profiler is not started")
	}
	defer p.closeTargetIdentities()

	sessions := make([]targetsession.Session, 0, len(p.targets))
	for _, target := range p.targets {
		target := target
		sessions = append(sessions, targetsession.Session{
			PID: target.pid,
			Run: func(ctx context.Context) error {
				return p.runMemrayTarget(
					ctx,
					target,
					func(sample profiler.SampleOutput) { enqueue(sample) },
				)
			},
		})
	}
	return targetsession.RunSessions(
		ctx,
		sessions,
		p.pctx.MaxProfilerProcesses,
	)
}

func (p *pythonMemoryProfiler) Stop(_ *pcontext.ProfilerContext) error {
	p.closeTargetIdentities()
	return nil
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
