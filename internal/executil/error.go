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

package executil

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrProcessNotInitialized reports use of a Process not created with New.
	ErrProcessNotInitialized = errors.New("executil: process is not initialized")
	// ErrProcessNotStarted reports an operation that requires a completed launch.
	ErrProcessNotStarted = errors.New("executil: process has not been started")
	// ErrProcessClosed reports that Close permanently prevented command launch.
	ErrProcessClosed = errors.New("executil: process is closed")
	// ErrProcessAlreadyStarted reports a repeated Start, including a failed or unfinished launch.
	ErrProcessAlreadyStarted = errors.New("executil: process has already been started")
)

var (
	// ErrStopped is returned directly for an expected signal exit without independent failures.
	ErrStopped = errors.New("executil: command stopped")
	// ErrRunCanceled reports Run cancellation without independent failures.
	ErrRunCanceled = fmt.Errorf("executil: command canceled: %w", context.Canceled)
	// ErrRunTimeout reports Run timeout without independent failures.
	ErrRunTimeout = fmt.Errorf("executil: command timed out: %w", context.DeadlineExceeded)
)

var (
	// ErrStopFailed reports that process group termination failed.
	ErrStopFailed = errors.New("executil: stop failed")
	// ErrCleanupFailed reports an actual process group, pipe or owned-file cleanup failure.
	ErrCleanupFailed = errors.New("executil: cleanup failed")
)

// ErrOutputLimitExceeded reports that memfd data exceeds its read limit.
var ErrOutputLimitExceeded = errors.New("executil: output limit exceeded")
