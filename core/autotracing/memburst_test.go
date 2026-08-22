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

package autotracing

import (
	"strings"
	"testing"
)

func TestReadMemInfoFromRequiresEveryRequestedKey(t *testing.T) {
	required := map[string]bool{
		"Active(anon)":   true,
		"Inactive(anon)": true,
	}

	values, err := readMemInfoFrom(strings.NewReader(
		"Inactive(anon): 23 kB\nMemFree: 99 kB\nActive(anon): 17 kB\n",
	), required)
	if err != nil {
		t.Fatalf("readMemInfoFrom() error = %v", err)
	}
	if values["Active(anon)"] != 17 || values["Inactive(anon)"] != 23 {
		t.Fatalf("readMemInfoFrom() = %v, want requested values", values)
	}

	_, err = readMemInfoFrom(strings.NewReader("Active(anon): 17 kB\n"), required)
	if err == nil || !strings.Contains(err.Error(), "Inactive(anon)") {
		t.Fatalf("readMemInfoFrom() error = %v, want missing-key error", err)
	}
}

func TestReadMemInfoFromRejectsMalformedRequestedValue(t *testing.T) {
	_, err := readMemInfoFrom(
		strings.NewReader("MemTotal: unknown kB\n"),
		map[string]bool{"MemTotal": true},
	)
	if err == nil || !strings.Contains(err.Error(), "parse MemTotal") {
		t.Fatalf("readMemInfoFrom() error = %v, want parse error", err)
	}
}
