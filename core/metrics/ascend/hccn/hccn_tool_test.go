// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package hccn

import (
	"strings"
	"testing"
)

func TestGetInfoFromHccnToolInvalidArguments(t *testing.T) {
	output, err := getInfoFromHccnTool("\x00")
	if err == nil {
		t.Fatalf("getInfoFromHccnTool() error = %v, want configuration error", err)
	}
	if output != "" {
		t.Fatalf("getInfoFromHccnTool() output = %q, want empty output", output)
	}
	for _, want := range []string{"command argument 0 contains a null byte", `stdout=""`, `stderr=""`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("getInfoFromHccnTool() error = %v, want containing %q", err, want)
		}
	}
}

func TestParseStatInfo(t *testing.T) {
	// real hccn_tool -stat -g output: spaces around the colon, header lines,
	// and values that themselves contain colons
	out := "netdev macro stat:\n" +
		"Rx packets : 306399\n" +
		"Rx errors : 0\n" +
		"Last reset : 12:34:56\n" +
		"Tx packets:42"

	got := parseStatInfo(out)
	want := map[string]int{
		"Rx packets": 306399,
		"Rx errors":  0,
		"Tx packets": 42,
	}

	if len(got) != len(want) {
		t.Fatalf("parseStatInfo() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parseStatInfo()[%q] = %d, want %d", k, got[k], v)
		}
	}
}
