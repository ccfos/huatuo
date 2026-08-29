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

package container

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadBoundedBody(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		limit         int64
		want          string
		wantTruncated bool
	}{
		{name: "below limit", body: "short", limit: 8, want: "short"},
		{name: "exact limit", body: "12345678", limit: 8, want: "12345678"},
		{name: "over limit", body: "1234567890", limit: 8, want: "12345678", wantTruncated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated, err := readBoundedBody(strings.NewReader(tt.body), tt.limit)
			if err != nil {
				t.Fatalf("readBoundedBody() error = %v", err)
			}
			if string(got) != tt.want || truncated != tt.wantTruncated {
				t.Fatalf("readBoundedBody() = (%q, %t), want (%q, %t)",
					got, truncated, tt.want, tt.wantTruncated)
			}
		})
	}
}

func TestReadBoundedBodyPreservesReadError(t *testing.T) {
	wantErr := errors.New("read failed")
	_, _, err := readBoundedBody(io.MultiReader(strings.NewReader("ok"), errorReader{err: wantErr}), 8)
	if !errors.Is(err, wantErr) {
		t.Fatalf("readBoundedBody() error = %v, want %v", err, wantErr)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
