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

// Package executil starts and manages operating-system process groups.
// Context arguments must be non-nil.
package executil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrStopped reports that Stop terminated the command with a signal.
	ErrStopped = errors.New("executil: command stopped")
	// ErrStopFailed reports that process group termination failed.
	ErrStopFailed = errors.New("executil: stop failed")
	// ErrOutputLimitExceeded reports that stdout or memfd data exceeds its limit.
	ErrOutputLimitExceeded = errors.New("executil: output limit exceeded")

	errProcessNotInitialized = errors.New("process is not initialized")
)

const (
	defaultStopGracePeriod = 5 * time.Second
	defaultMaxOutputBytes  = 64 << 10
	outputDrainTimeout     = time.Second
)

// Spec describes one external command invocation.
type Spec struct {
	Path string
	Args []string
	// Env replaces the child environment. A nil Env inherits the parent environment.
	Env []string
	// StopGracePeriod controls when Run escalates from SIGTERM to SIGKILL.
	// A zero value uses five seconds.
	StopGracePeriod time.Duration
	// MaxOutputBytes limits retained standard output. A zero value uses 64 KiB.
	MaxOutputBytes int
}

// Option configures a process before it starts.
type Option func(*Process)

// WithExtraFiles passes files to the child as descriptors starting at 3.
// The caller owns the files and must keep them open until Start returns.
func WithExtraFiles(files ...*os.File) Option {
	return func(process *Process) {
		process.extraFiles = slices.Clone(files)
	}
}

// WithStdout redirects standard output instead of retaining it for Stdout.
// A nil writer preserves capture. The caller owns the writer until Wait returns.
// Write must return promptly; the process cannot interrupt a blocked writer.
func WithStdout(writer io.Writer) Option {
	return func(process *Process) {
		process.stdoutWriter = writer
	}
}

// WithStderr redirects standard error instead of retaining it for Stderr.
// A nil writer preserves capture. The caller owns the writer until Wait returns.
// Write must return promptly; the process cannot interrupt a blocked writer.
func WithStderr(writer io.Writer) Option {
	return func(process *Process) {
		process.stderrWriter = writer
	}
}

type processState uint8

const (
	processStateInvalid processState = iota
	processStateNew
	processStateStarting
	processStateRunning
	processStateExited
	processStateStartFailed
)

type memfdCleanup bool

const (
	preserveMemfd memfdCleanup = false
	releaseMemfd  memfdCleanup = true
)

// Closing done publishes the immutable error to every waiter.
type lifecycleResult struct {
	done chan struct{}
	err  error
}

// Process owns one command lifecycle and its process group. On natural exit,
// remaining group members are killed before the leader is reaped. Stop allows
// group members to finish within its grace period before escalating.
// Process must be created with New and cannot be restarted; methods on its zero
// value return an initialization error, except Stderr, which returns no data.
type Process struct {
	spec         Spec
	output       outputBuffer
	stderr       tailBuffer
	extraFiles   []*os.File
	stdoutWriter io.Writer
	stderrWriter io.Writer
	memfd        *memfdOutput

	mu              sync.Mutex
	state           processState
	pid             int
	start           lifecycleResult
	wait            lifecycleResult
	groupErr        error
	isStopRequested bool
	stopAttempt     *lifecycleResult
	groupStopDone   chan struct{}
}

// New validates and snapshots a command specification without starting it.
func New(spec Spec, options ...Option) (*Process, error) { //nolint:gocritic // Spec is within the 80-byte value limit.
	process := &Process{spec: spec}
	for _, option := range options {
		option(process)
	}

	if err := process.spec.validate(); err != nil {
		return nil, fmt.Errorf("new command: %w", err)
	}

	if process.memfd != nil {
		if process.memfd.argsForOutput == nil {
			return nil, errors.New("new command: output argument builder must not be nil")
		}

		if process.memfd.limit <= 0 {
			return nil, errors.New("new command: maximum memfd output bytes must be positive")
		}
	}

	if process.spec.StopGracePeriod == 0 {
		process.spec.StopGracePeriod = defaultStopGracePeriod
	}

	if process.spec.MaxOutputBytes == 0 {
		process.spec.MaxOutputBytes = defaultMaxOutputBytes
	}

	process.spec.Args = slices.Clone(process.spec.Args)
	process.spec.Env = slices.Clone(process.spec.Env)
	process.output = outputBuffer{limit: process.spec.MaxOutputBytes}
	process.state = processStateNew
	process.start = lifecycleResult{done: make(chan struct{})}
	process.wait = lifecycleResult{done: make(chan struct{})}

	return process, nil
}

func (s *Spec) validate() error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("command path must not be empty")
	}

	if strings.IndexByte(s.Path, 0) >= 0 {
		return fmt.Errorf("command path %q contains a null byte", s.Path)
	}

	if err := validateArgs(s.Args); err != nil {
		return err
	}

	for index, value := range s.Env {
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("command environment entry %d contains a null byte", index)
		}

		if strings.IndexByte(value, '=') <= 0 {
			return fmt.Errorf(
				"command environment entry %d must use a non-empty KEY=VALUE form",
				index,
			)
		}
	}

	if s.StopGracePeriod < 0 {
		return errors.New("stop grace period must not be negative")
	}

	if s.MaxOutputBytes < 0 {
		return errors.New("maximum output bytes must not be negative")
	}

	return nil
}

func validateArgs(args []string) error {
	for index, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("command argument %d contains a null byte", index)
		}
	}

	return nil
}

// Start launches the command and starts its single internal reaper.
// The context controls launch only; canceling it after Start returns does not
// stop the process. WithMemfdOutput creates a file owned by Process; read it
// with MemfdOutput before calling Stop. Failed starts close the created file.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("start command: %w", errProcessNotInitialized)
	}

	if p.state != processStateNew {
		p.mu.Unlock()
		return fmt.Errorf("start command %q: process has already been started", p.spec.Path)
	}

	p.state = processStateStarting
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
	}

	cmd := exec.Command(p.spec.Path, p.spec.Args...)
	// Descendants outside our group may retain a pipe after the leader exits.
	cmd.WaitDelay = outputDrainTimeout
	cmd.Env = p.spec.Env
	cmd.ExtraFiles = p.extraFiles
	cmd.Stdout = &p.output
	if p.stdoutWriter != nil {
		cmd.Stdout = p.stdoutWriter
	}

	cmd.Stderr = &p.stderr
	if p.stderrWriter != nil {
		cmd.Stderr = p.stderrWriter
	}

	if p.memfd != nil {
		if err := p.memfd.prepare(cmd); err != nil {
			return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
		}
	}

	if err := ctx.Err(); err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
	}

	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
	}

	p.mu.Lock()
	p.pid = cmd.Process.Pid
	launchErr := ctx.Err()
	if launchErr != nil {
		p.isStopRequested = true
	}

	go p.reap(cmd)
	if launchErr == nil {
		p.state = processStateRunning
		close(p.start.done)
	}

	p.mu.Unlock()

	if launchErr == nil {
		return nil
	}

	return p.finishCanceledStart(launchErr)
}

func (p *Process) finishCanceledStart(launchErr error) error {
	forceErr := wrapStopFailure(p.forceStopAndWait(p.wait.done))

	p.mu.Lock()
	var waitErr error
	if forceErr == nil && !errors.Is(p.wait.err, ErrStopped) {
		waitErr = p.wait.err
	}

	p.start.err = errors.Join(
		fmt.Errorf("start command %q: %w", p.spec.Path, launchErr),
		forceErr,
		waitErr,
	)
	if forceErr == nil {
		p.state = processStateStartFailed
	} else {
		select {
		case <-p.wait.done:
			p.state = processStateExited
		default:
			p.state = processStateRunning
		}
	}

	if p.memfd != nil {
		p.start.err = errors.Join(p.start.err, p.memfd.close())
	}

	close(p.start.done)
	startErr := p.start.err
	p.mu.Unlock()
	return startErr
}

func (p *Process) failStart(err error) error {
	p.mu.Lock()
	if p.memfd != nil {
		err = errors.Join(err, p.memfd.close())
	}

	p.state = processStateStartFailed
	p.start.err = err
	p.wait.err = err
	close(p.start.done)
	close(p.wait.done)
	p.mu.Unlock()
	return err
}

func (p *Process) reap(cmd *exec.Cmd) {
	exitErr := waitForCommandExit(cmd.Process.Pid)
	p.mu.Lock()
	stopDone := p.groupStopDone
	p.mu.Unlock()
	if stopDone != nil && exitErr == nil {
		// Stop owns the grace period; keep the leader waitable until it has
		// observed or terminated the remaining group members.
		<-stopDone
	}
	p.mu.Lock()
	if exitErr != nil {
		p.groupErr = errors.Join(p.groupErr, wrapStopFailure(
			fmt.Errorf("observe command %q exit: %w", p.spec.Path, exitErr)))
	} else if err := forceStopProcessGroup(p.pid); err != nil && !processGroupMissing(err) {
		p.groupErr = errors.Join(p.groupErr, wrapStopFailure(
			wrapSignalError("clean up", p.spec.Path, err)))
	}
	// WNOWAIT pins the leader PID through the final group signal. Retire it
	// before Wait can release it for reuse; later Stop calls only read results.
	p.pid = 0
	p.mu.Unlock()

	err := cmd.Wait()

	p.mu.Lock()
	isExpectedStop := p.groupErr == nil && p.isStopRequested
	if isExpectedStop && isStoppedExit(err) {
		err = fmt.Errorf(
			"%w: command %q exited after a stop signal: %w",
			ErrStopped,
			p.spec.Path,
			err,
		)
	} else if err != nil {
		err = fmt.Errorf("wait for command %q: %w", p.spec.Path, err)
	}

	p.wait.err = err
	if p.state == processStateRunning {
		p.state = processStateExited
	}

	close(p.wait.done)
	p.mu.Unlock()
}

// Wait waits for an in-progress Start and then for the command reaper. Multiple
// callers receive the same stored result. Inherited output pipes have one second
// to close after the leader is reaped; an incomplete drain follows os/exec's
// ErrWaitDelay semantics. Wait preserves memfd output and does not report
// output size limits; retrieve those errors from Stdout and MemfdOutput.
func (p *Process) Wait() error {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("wait for command: %w", errProcessNotInitialized)
	}

	if p.state == processStateNew {
		p.mu.Unlock()
		return fmt.Errorf("wait for command %q: process has not been started", p.spec.Path)
	}

	p.mu.Unlock()

	// Failed starts also publish both results once no process remains to reap.
	<-p.start.done
	<-p.wait.done
	return p.processResult()
}

func (p *Process) processResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := p.wait.err
	if p.start.err != nil {
		err = p.start.err
	}

	if p.groupErr == nil || errors.Is(err, p.groupErr) {
		return err
	}

	return errors.Join(err, p.groupErr)
}

// Stop sends SIGTERM to the process group. It waits for all group members
// within the context deadline before escalating to SIGKILL.
// Reaping and bounded output draining remain internal to Process.
// Stop closes the memfd, even after natural exit or a stop
// failure. Finish reading before calling Stop. Repeated calls close it only once;
// failed process group termination can be retried.
func (p *Process) Stop(ctx context.Context) error {
	return p.stop(ctx, releaseMemfd)
}

func (p *Process) stop(ctx context.Context, cleanup memfdCleanup) (err error) {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("stop command: %w", errProcessNotInitialized)
	}

	switch p.state {
	case processStateNew, processStateStarting:
		p.mu.Unlock()
		return fmt.Errorf("stop command %q: process has not been started", p.spec.Path)
	}

	if cleanup == releaseMemfd && p.memfd != nil {
		defer func() { err = errors.Join(err, p.closeMemfd()) }()
	}

	if p.state == processStateStartFailed || p.state == processStateExited {
		p.mu.Unlock()
		return p.stopResult()
	}

	if attempt := p.stopAttempt; attempt != nil {
		select {
		case <-attempt.done:
			if attempt.err == nil {
				p.mu.Unlock()
				return nil
			}
		default:
			p.mu.Unlock()
			return p.waitForStopAttempt(ctx, attempt)
		}
	}

	p.isStopRequested = true
	attempt := &lifecycleResult{done: make(chan struct{})}
	p.stopAttempt = attempt
	p.groupStopDone = make(chan struct{})
	groupStopDone := p.groupStopDone
	p.mu.Unlock()

	err = wrapStopFailure(p.stopProcessGroup(ctx))
	if err != nil {
		p.mu.Lock()
		p.groupErr = errors.Join(p.groupErr, err)
		p.mu.Unlock()
	}
	close(groupStopDone)
	if err == nil {
		<-p.wait.done
		err = p.stopResult()
	}

	p.mu.Lock()
	attempt.err = err
	close(attempt.done)
	p.mu.Unlock()
	return err
}

func (p *Process) waitForStopAttempt(ctx context.Context, attempt *lifecycleResult) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return fmt.Errorf("wait for command %q stop: %w", p.spec.Path, ctx.Err())
	}
}

func (p *Process) stopProcessGroup(ctx context.Context) error {
	gracefulErr := p.signalProcessGroup(gracefulStopProcessGroup)
	if processGroupMissing(gracefulErr) {
		return nil
	}

	if gracefulErr != nil {
		forceErr := p.forceStopGroup()
		if forceErr != nil {
			return errors.Join(
				wrapSignalError("gracefully stop", p.spec.Path, gracefulErr),
				forceErr,
			)
		}
		return p.waitForGroupExit(context.Background())
	}

	if err := p.waitForGroupExit(ctx); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if err := p.forceStopGroup(); err != nil {
			return err
		}
		return p.waitForGroupExit(context.Background())
	}
	return nil
}

func (p *Process) waitForGroupExit(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		pid := p.pid
		p.mu.Unlock()
		if pid == 0 {
			return nil
		}
		running, err := processGroupRunning(pid)
		if err != nil {
			return fmt.Errorf("inspect command %q process group: %w", p.spec.Path, err)
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Process) forceStopGroup() error {
	err := p.signalProcessGroup(forceStopProcessGroup)
	if processGroupMissing(err) {
		return nil
	}
	return wrapSignalError("force stop", p.spec.Path, err)
}

func (p *Process) forceStopAndWait(waitDone <-chan struct{}) error {
	err := p.signalProcessGroup(forceStopProcessGroup)
	if processGroupMissing(err) {
		err = nil
	}

	if err != nil {
		return wrapSignalError("force stop", p.spec.Path, err)
	}

	<-waitDone
	return p.stopResult()
}

func (p *Process) signalProcessGroup(signal func(int) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pid == 0 {
		return nil
	}

	return signal(p.pid)
}

func (p *Process) stopResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if errors.Is(p.wait.err, exec.ErrWaitDelay) {
		return errors.Join(p.groupErr, p.wait.err)
	}

	return p.groupErr
}

func wrapSignalError(action, path string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf(
		"%s command %q process group: %w",
		action,
		path,
		err,
	)
}

func wrapStopFailure(err error) error {
	if err == nil || errors.Is(err, ErrStopFailed) {
		return err
	}

	if errors.Is(err, exec.ErrWaitDelay) {
		return err
	}

	return fmt.Errorf("%w: %w", ErrStopFailed, err)
}

// Run starts the command and waits for it. If ctx is canceled after launch,
// Spec.StopGracePeriod controls when Stop escalates from SIGTERM to SIGKILL.
// Run preserves memfd output, including after cancellation. Read outputs and
// call Stop to release the file. Output size errors come from the output methods.
func (p *Process) Run(ctx context.Context) error {
	if err := p.Start(ctx); err != nil {
		return err
	}

	select {
	case <-p.wait.done:
		return p.processResult()
	case <-ctx.Done():
		select {
		case <-p.wait.done:
			return p.processResult()
		default:
		}

		stopCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			p.spec.StopGracePeriod,
		)
		stopErr := p.stop(stopCtx, preserveMemfd)
		cancel()
		runErr := fmt.Errorf("run command %q: %w", p.spec.Path, ctx.Err())
		if stopErr != nil {
			return errors.Join(runErr, stopErr)
		}
		waitErr := p.processResult()
		if errors.Is(waitErr, ErrStopped) {
			waitErr = nil
		}
		return errors.Join(runErr, waitErr)
	}
}

// Stdout returns a copy of the retained standard output.
// It is empty when WithStdout redirects output to a non-nil writer. Exceeding
// Spec.MaxOutputBytes returns the retained prefix and ErrOutputLimitExceeded.
// Wait first for complete output; snapshots remain available after Stop.
func (p *Process) Stdout() ([]byte, error) {
	// The limit is immutable after New and distinguishes an uninitialized Process.
	if p.output.limit == 0 {
		return nil, fmt.Errorf("read command stdout: %w", errProcessNotInitialized)
	}

	data, exceeded := p.output.Snapshot()
	if exceeded {
		return data, fmt.Errorf(
			"%w: command %q stdout exceeds %d bytes",
			ErrOutputLimitExceeded,
			p.spec.Path,
			p.spec.MaxOutputBytes,
		)
	}

	return data, nil
}

// Stderr returns a copy of the newest 64 KiB written to standard error.
// It is empty when WithStderr redirects output to a non-nil writer.
func (p *Process) Stderr() []byte {
	return p.stderr.Bytes()
}
