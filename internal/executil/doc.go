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
// Use Run for synchronous execution and owned output snapshots. A non-nil
// RunError distinguishes cancellation, execution, output and cleanup failures.
// IsCancellation permits business layers to recognize a pure cancellation;
// errors.Is(err, context.Canceled) alone does not exclude other failures.
// Earlier stop failures remain visible after successful cleanup. If final Close
// fails, RunError.Process transfers ownership of any further cleanup attempts.
//
// Use New and Start for asynchronous execution. Start's context only controls
// launch. Wait or Done observes completion; canceling Start's context after it
// returns does not stop the command. Process.Run controls the entire lifetime.
// Stop sends SIGTERM, then SIGKILL at the earlier of StopGracePeriod (five seconds
// by default) and its context's cancellation. Run cancellation uses the configured
// grace period independently of the canceled run context. Close immediately
// forces termination. Neither cancellation nor Close can impose a hard return
// deadline on kernel reaping or a blocked caller-provided writer.
//
// The lifecycle is finish execution, read output, then Close. Stop and Wait
// preserve memfd output. Close releases owned files even if stopping fails;
// repeated Close is safe, signal failures can be retried, and file close errors
// are cached. Close before Start permanently closes the process and publishes
// os.ErrClosed to Wait. Close during Start waits for launch to settle. Done is
// open before Start and closes once both launch and reaping results are final.
// The zero Process is invalid; its Done returns nil.
//
// One internal reaper owns cmd.Wait. Process methods support concurrent use;
// configuration options apply only during New, and Process must not be copied.
// Stop rejects an unfinished Start. Output methods return independent snapshots;
// wait for completion to obtain final output. Default stdout retains a 64 KiB
// prefix and reports overflow; stderr retains a 64 KiB tail. Memfd limits bound
// reads, not file growth. Borrowed writers and ExtraFiles remain caller-owned.
//
// The leader's exit ends the command lifetime. Remaining group members are
// killed before reaping the leader to prevent signaling a reused PID. Descendants
// that leave the group are outside this boundary. Inherited output pipes have a
// one-second drain timeout, with the standard os/exec ErrWaitDelay semantics.
package executil
