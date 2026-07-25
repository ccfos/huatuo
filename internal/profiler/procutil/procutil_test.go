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
)

func TestParseThreadGroupID(t *testing.T) {
	tgid, err := parseThreadGroupID(strings.NewReader(
		"Name:\tworker\nPid:\t4243\nTgid:\t4242\n",
	))
	if err != nil {
		t.Fatalf("parseThreadGroupID() error = %v", err)
	}
	if tgid != 4242 {
		t.Fatalf("parseThreadGroupID() = %d, want 4242", tgid)
	}
}

func TestParseThreadGroupIDRejectsMalformedStatus(t *testing.T) {
	for _, status := range []string{
		"Name:\tworker\n",
		"Tgid:\tinvalid\n",
		"Tgid:\t0\n",
	} {
		if _, err := parseThreadGroupID(strings.NewReader(status)); err == nil {
			t.Fatalf("parseThreadGroupID(%q) succeeded", status)
		}
	}
}
