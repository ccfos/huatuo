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

//go:build integration

package bpfmock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyFoldedProfile(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		expected       int64
		wantError      bool
	}{
		{"bytes", "comm=\"trigger\";pid=\"42\";do_mmap_mock 4096\ncomm=\"trigger\";pid=\"42\";do_mmap_mock 8192\n", 12288, false},
		{"zero", "", 0, false},
		{"wrong total", "comm=\"trigger\";pid=\"42\";do_mmap_mock 4096\n", 8192, true},
		{"wrong PID", "comm=\"trigger\";pid=\"43\";do_mmap_mock 4096\n", 4096, true},
		{"wrong comm", "comm=\"other\";pid=\"42\";do_mmap_mock 4096\n", 4096, true},
		{"real hook", "comm=\"trigger\";pid=\"42\";do_mmap 4096\n", 4096, true},
		{"negative", "comm=\"trigger\";pid=\"42\";do_mmap_mock -4096\n", 0, true},
		{"invalid", "garbage\n", 0, true},
		{"non-numeric", "comm=\"trigger\";pid=\"42\";do_mmap_mock bytes\n", 0, true},
		{"unexpected sample", "comm=\"trigger\";pid=\"42\";do_mmap_mock 4096\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "perf_1.folded"), []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			err := VerifyFoldedProfile(dir, 42, "trigger", tc.expected, "do_mmap_mock")
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMissingProfileDirectory(t *testing.T) {
	if err := VerifyFoldedProfile(filepath.Join(t.TempDir(), "missing"), 42, "trigger", 0, "do_mmap_mock"); err == nil {
		t.Fatal("missing profile directory accepted as an empty profile")
	}
}

func TestReadProfileInput(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, identity, pageSize string
		wantError                bool
	}{
		{"valid", `{"pid":42,"comm":"trigger"}`, "4096", false},
		{"malformed identity", `{`, "4096", true},
		{"missing PID", `{"comm":"trigger"}`, "4096", true},
		{"missing comm", `{"pid":42}`, "4096", true},
		{"zero page size", `{"pid":42,"comm":"trigger"}`, "0", true},
		{"negative page size", `{"pid":42,"comm":"trigger"}`, "-4096", true},
		{"invalid page size", `{"pid":42,"comm":"trigger"}`, "invalid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "identity.json")
			if err := os.WriteFile(path, []byte(tc.identity), 0o600); err != nil {
				t.Fatal(err)
			}
			input, err := ReadProfileInput([]string{dir, path, "mixed", tc.pageSize})
			if (err != nil) != tc.wantError {
				t.Fatalf("input=%+v error=%v", input, err)
			}
			if !tc.wantError && input != (ProfileInput{
				Directory: dir, Scenario: "mixed", PageSize: 4096,
				ExpectedPID: 42, ExpectedComm: "trigger",
			}) {
				t.Fatalf("unexpected input: %+v", input)
			}
		})
	}
	for _, args := range [][]string{nil, {dir, filepath.Join(dir, "missing"), "mixed", "4096"}, {dir, "identity.json", "mixed", "4096", "extra"}} {
		if _, err := ReadProfileInput(args); err == nil {
			t.Fatalf("invalid inputs accepted: %v", args)
		}
	}
}
