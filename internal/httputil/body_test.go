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

package httputil

import (
	"errors"
	"strings"
	"testing"
)

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestReadLimitedBody(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		limit         int64
		wantBody      string
		wantTruncated bool
		wantErr       bool
	}{
		{name: "below limit", body: "abc", limit: 4, wantBody: "abc"},
		{name: "exact limit", body: "abcd", limit: 4, wantBody: "abcd"},
		{name: "over limit", body: "abcdef", limit: 4, wantBody: "abcde", wantTruncated: true},
		{name: "zero limit", body: "a", limit: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated, err := ReadLimitedBody(strings.NewReader(tt.body), tt.limit)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadLimitedBody() error = %v, wantErr %v", err, tt.wantErr)
			}
			if string(got) != tt.wantBody {
				t.Errorf("ReadLimitedBody() body = %q, want %q", got, tt.wantBody)
			}
			if truncated != tt.wantTruncated {
				t.Errorf("ReadLimitedBody() truncated = %v, want %v", truncated, tt.wantTruncated)
			}
		})
	}
}

func TestReadLimitedBodyPropagatesReadError(t *testing.T) {
	wantErr := errors.New("read failed")
	if _, _, err := ReadLimitedBody(failingReader{err: wantErr}, 8); !errors.Is(err, wantErr) {
		t.Fatalf("ReadLimitedBody() error = %v, want %v", err, wantErr)
	}
}

func TestErrorPreview(t *testing.T) {
	if got := ErrorPreview([]byte("  failure  \n"), 32); got != "failure" {
		t.Errorf("ErrorPreview() = %q, want %q", got, "failure")
	}
	want := "fail... [truncated after 4 bytes]"
	if got := ErrorPreview([]byte("failure"), 4); got != want {
		t.Errorf("ErrorPreview() = %q, want %q", got, want)
	}
}
