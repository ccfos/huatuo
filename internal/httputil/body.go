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

// Package httputil contains bounded HTTP response helpers shared by clients.
package httputil

import (
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	// DefaultResponseBodyLimit covers dense kubelet responses while bounding
	// memory used by local HTTP clients.
	DefaultResponseBodyLimit = 128 << 20
	// DefaultErrorBodyLimit keeps diagnostic text useful without echoing an
	// arbitrarily large response into an error.
	DefaultErrorBodyLimit = 8 << 10
)

// ReadLimitedBody reads at most limit+1 bytes so callers can distinguish an
// exact-limit response from a truncated one.
func ReadLimitedBody(body io.Reader, limit int64) ([]byte, bool, error) {
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, false, fmt.Errorf("invalid response byte limit %d", limit)
	}

	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	return data, int64(len(data)) > limit, nil
}

// ErrorPreview formats bounded response text and marks truncated diagnostics.
func ErrorPreview(body []byte, limit int64) string {
	truncated := int64(len(body)) > limit
	if truncated {
		body = body[:limit]
	}

	message := strings.TrimSpace(string(body))
	if truncated {
		message += fmt.Sprintf("... [truncated after %d bytes]", limit)
	}
	return message
}
