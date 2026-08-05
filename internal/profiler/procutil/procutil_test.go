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
	"strings"
	"testing"

	"huatuo-bamai/internal/bpf"
)

func TestCommToString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "nul padded", input: "worker", want: "worker"},
		{name: "empty", want: ""},
		{name: "full length", input: strings.Repeat("x", bpf.TaskCommLen), want: strings.Repeat("x", bpf.TaskCommLen)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var comm [bpf.TaskCommLen]byte
			copy(comm[:], tt.input)
			if got := CommToString(comm); got != tt.want {
				t.Errorf("CommToString() = %q, want %q", got, tt.want)
			}
		})
	}
}
