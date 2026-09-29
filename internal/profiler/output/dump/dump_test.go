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

package dump

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestFormatterWritesTextDump(t *testing.T) {
	formatter := New(Options{ShowCount: true})
	if err := formatter.Add(&output.Sample{
		Frames:     []string{"root", "worker"},
		Count:      3,
		ThreadID:   "t1",
		ThreadName: "worker",
		PID:        42,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	var buf bytes.Buffer
	if err := formatter.Write(&buf); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	const want = "Thread t1 (pid=42) \"worker\" [count=3]\n    root\n    worker\n\n"
	if got := buf.String(); got != want {
		t.Fatalf("text dump = %q, want %q", got, want)
	}
}

func TestFormatterWritesJSONDump(t *testing.T) {
	formatter := New(Options{JSON: true})
	if err := formatter.Add(&output.Sample{
		Frames:     []string{"root"},
		Count:      2,
		ThreadID:   "t1",
		ThreadName: "worker",
		PID:        42,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	var buf bytes.Buffer
	if err := formatter.Write(&buf); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	var entries []dumpEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal JSON dump: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}

	entry := entries[0]
	if entry.ThreadID != "t1" || entry.ThreadName != "worker" || entry.PID != 42 || entry.Count != 2 {
		t.Errorf("entry metadata = %+v", entry)
	}
	if len(entry.Frames) != 1 || entry.Frames[0] != "root" {
		t.Errorf("entry frames = %v, want [root]", entry.Frames)
	}
}

func TestFormatterSkipsFrameLessSamplesAndResets(t *testing.T) {
	formatter := New(Options{})
	if formatter.opts.Indent != "    " {
		t.Fatalf("default indent = %q, want four spaces", formatter.opts.Indent)
	}

	if err := formatter.Add(&output.Sample{Count: 5}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !formatter.IsEmpty() {
		t.Fatal("frame-less sample must not be dumped")
	}

	if err := formatter.Add(&output.Sample{Frames: []string{"root"}, Count: 1}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if formatter.IsEmpty() {
		t.Fatal("formatter with a stack must not be empty")
	}

	formatter.Reset()
	if !formatter.IsEmpty() {
		t.Fatal("Reset() must clear accumulated samples")
	}
}
