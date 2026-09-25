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

package python

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// errUnsupportedRuntime reports a CPython runtime that cannot be inspected.
var errUnsupportedRuntime = errors.New("CPython runtime is unsupported")

// Bound provider diagnostics before the shared snapshot output limit is applied.
// JSON encoding may expand each input byte to a six-byte escape.
const maxReasonBytes = 512

func unsupportedRuntime(reason string) error {
	return fmt.Errorf("%w: %s", errUnsupportedRuntime, reason)
}

// errNotCPythonModule lets discovery skip unrelated executables or DSOs and
// classify a process with no CPython runtime as unavailable rather than failed.
var errNotCPythonModule = errors.New("module does not expose a CPython runtime")

// Provider captures a CPython GC-tracked object census through the external
// reader.
type Provider struct {
	reader *reader
}

// New builds the production Python census provider.
func New() *Provider {
	return &Provider{reader: newReader("")}
}

// Snapshot counts CPython objects currently tracked by the cyclic garbage
// collector and reduces them to type aggregates.
// Unavailable or partial data is a snapshot; fatal read failures and cancellation
// return an error without a snapshot.
func (p *Provider) Snapshot(ctx context.Context,
	request memsnapshot.Request,
) (*memsnapshot.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	snapshot, err := p.reader.snapshot(ctx, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	return snapshotResult(snapshot, err)
}

func snapshotResult(snapshot *memsnapshot.Snapshot, err error) (*memsnapshot.Snapshot, error) {
	if errors.Is(err, errUnsupportedRuntime) {
		return memsnapshot.Unavailable(boundedReason(err.Error())), nil
	}
	if err != nil {
		return nil, boundedError{cause: err}
	}
	if snapshot == nil {
		return nil, errors.New("Python external census returned a nil response")
	}
	snapshot.Reason = boundedReason(snapshot.Reason)
	return snapshot, nil
}

// boundedError preserves the reader's cause without expanding persisted diagnostics.
type boundedError struct {
	cause error
}

func (e boundedError) Error() string {
	return boundedReason(e.cause.Error())
}

func (e boundedError) Unwrap() error {
	return e.cause
}

func boundedReason(reason string) string {
	if len(reason) > maxReasonBytes {
		reason = reason[:maxReasonBytes]
	}
	return strings.ToValidUTF8(reason, "")
}
