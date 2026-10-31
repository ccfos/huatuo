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

package profile

import (
	"context"
	"errors"
	"time"
)

var (
	ErrCollectionDurationElapsed = errors.New("collection duration elapsed")
	ErrStopRequested             = errors.New("collection stop requested")
)

// FinalizationTimeout bounds collector final output and publication.
const FinalizationTimeout = 30 * time.Second

// IsNormalStop accepts only control causes, never an error joined with a failure.
func IsNormalStop(ctx context.Context) bool {
	cause := context.Cause(ctx)
	return cause == ErrCollectionDurationElapsed || cause == ErrStopRequested //nolint:errorlint // Joined failures must not count as control causes.
}
