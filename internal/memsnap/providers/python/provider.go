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

	"github.com/ccfos/huatuo/internal/memsnap"
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

// NewProvider builds the production Python census provider.
func NewProvider() *Provider {
	return &Provider{reader: newReader("")}
}

// Capture counts CPython objects currently tracked by the cyclic garbage
// collector and reduces them to type aggregates.
func (p *Provider) Capture(ctx context.Context,
	request memsnap.Request,
) (*memsnap.Snapshot, error) {
	response, err := p.reader.capture(ctx, request)
	return captureResult(response, err), nil
}

func captureResult(response *memsnap.Snapshot, err error) *memsnap.Snapshot {
	if errors.Is(err, errUnsupportedRuntime) {
		return memsnap.Unavailable(boundedReason(err.Error()))
	}
	if err != nil {
		return memsnap.Failed(boundedReason(err.Error()))
	}
	if response == nil {
		return memsnap.Failed("Python external census returned a nil response")
	}
	response.Reason = boundedReason(response.Reason)
	return response
}

func boundedReason(reason string) string {
	if len(reason) > maxReasonBytes {
		reason = reason[:maxReasonBytes]
	}
	return strings.ToValidUTF8(reason, "")
}
