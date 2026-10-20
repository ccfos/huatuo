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

// Package executil owns one external command and its Linux process group.
// Paths and arguments are passed directly to os/exec; shell syntax requires an
// explicit shell command. Context arguments must be non-nil.
//
// Use Run for synchronous execution and owned output snapshots. Run always
// returns a non-nil Result, even for invalid configuration. errors.Is and
// errors.As inspect the underlying execution, output and cleanup failures.
// Run adds ErrRunCanceled or ErrRunTimeout only when cancellation or timeout has
// no independent execution, output or cleanup failure. Match these markers with
// errors.Is to recognize a normal context stop; matching context.Canceled alone
// does not exclude other failures. Process.Start does not add these markers
// because output collection and final cleanup are caller-owned. Start returns
// context.Canceled or context.DeadlineExceeded for launch cancellation;
// independent failures remain joined to the context error. Run cancellation
// markers also match their standard context error.
// Earlier stop failures remain visible after successful cleanup. ErrCleanupFailed
// identifies actual cleanup failures; Run errors do not retain a Process handle.
//
// Use New and Start for asynchronous execution. Start's context only controls
// launch. Wait or Done observes completion; canceling Start's context after it
// returns does not stop the command. Run controls the entire lifetime.
// Stop sends StopSignal (default SIGTERM), then SIGKILL at the earlier of
// StopGracePeriod (five seconds by default) and its context's cancellation. Run cancellation uses the configured
// grace period independently of the canceled run context. Close immediately
// forces termination. Neither cancellation nor Close can impose a hard return
// deadline on kernel reaping or a blocked caller-provided writer.
//
// The lifecycle is finish execution, read output, then Close. Stop and Wait
// preserve memfd output. Close releases owned files even if stopping fails;
// repeated Close is safe, signal failures can be retried, and file close errors
// are cached. Close before Start permanently closes the process and publishes
// ErrProcessClosed to Start and Wait; Close and Stop return nil. Close during
// Start waits for launch to settle. Done is open before Start and closes once
// both launch and reaping results are final. Wait returns the lifecycle result, including reaper group
// cleanup and output drain failures. Stop and Close return only their own
// operation failures; Close excludes cleanup failures already published by Start.
// Combine Wait and Close results when both execution and release errors matter.
// An expected signal stop makes Wait return ErrStopped without the original
// exec.ExitError.
// Other nonzero exits and external signal exits retain their exec.ExitError.
// Compare directly with ErrStopped to recognize a stop without independent
// failures; errors.Is may also match a stop marker inside an independent error.
// Concurrent Stop calls execute independently, and failed attempts can be retried.
// The zero Process is invalid; error-returning methods return
// ErrProcessNotInitialized, and Done returns nil. Start returns
// ErrProcessAlreadyStarted after a previous or unfinished launch.
// Wait before Start and Stop before completed launch return ErrProcessNotStarted.
// Match these process state errors with errors.Is.
//
// One internal reaper owns cmd.Wait. Process methods support concurrent use;
// configuration options apply only during New, and Process must not be copied.
// Stop rejects an unfinished Start. Stdout and Stderr return byte slices;
// MemfdOutput also reports read errors. Output methods return independent snapshots;
// wait for completion to obtain final output. Default stdout and stderr each
// retain a 64 KiB tail; Spec.MaxStdoutBytes and Spec.MaxStderrBytes override
// these limits independently. Older bytes are discarded without error. Use
// WithStdout or WithStderr with a caller-owned writer when complete output is required.
// Memfd limits bound reads, not file growth. Borrowed writers and ExtraFiles
// remain caller-owned.
//
// The leader's exit ends the command lifetime. Remaining group members are
// killed before reaping the leader to prevent signaling a reused PID. Descendants
// that leave the group are outside this boundary. Inherited output pipes have a
// one-second drain timeout, with the standard os/exec ErrWaitDelay semantics.
package executil
