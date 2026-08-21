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

package procutil

import (
	"errors"
	"strings"
	"testing"
)

func TestParseThreadGroupID(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		want    int
		wantErr string
	}{
		{
			name:   "thread group",
			status: "Name:\tworker\nTgid:\t4242\nPid:\t4243\n",
			want:   4242,
		},
		{name: "missing", status: "Name:\tworker\nPid:\t4243\n", wantErr: "Tgid field not found"},
		{name: "invalid", status: "Tgid:\tzero\n", wantErr: `invalid Tgid value "zero"`},
		{name: "non-positive", status: "Tgid:\t0\n", wantErr: `invalid Tgid value "0"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseThreadGroupID(strings.NewReader(tt.status))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("parseThreadGroupID() error=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseThreadGroupID() error=%v", err)
			}
			if got != tt.want {
				t.Errorf("parseThreadGroupID()=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseThreadGroupIDReturnsReadError(t *testing.T) {
	wantErr := errors.New("read failed")
	_, err := parseThreadGroupID(errorReader{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("parseThreadGroupID() error=%v, want wrapped %v", err, wantErr)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
