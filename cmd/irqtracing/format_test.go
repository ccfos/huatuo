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

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler"
)

func TestCollapsedWriter(t *testing.T) {
	snapshot := &irqTracingSnapshot{
		result: &IRQTracingResult{},
		stacks: []*profiler.TreeItem{
			{Stack: [][]byte{[]byte("source"), []byte("raise;net\nrx")}, Value: 3},
			{Stack: [][]byte{[]byte("source"), []byte("raise:net rx")}, Value: 2},
		},
	}

	for _, format := range []string{outputText, outputCollapsed} {
		t.Run(format, func(t *testing.T) {
			var destination bytes.Buffer
			outputWriter, err := newWriter(&destination, format, nil)
			if err != nil {
				t.Fatalf("newWriter() error = %v", err)
			}
			if err := outputWriter.Write(snapshot); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if got := destination.String(); got != "source;raise:net rx 5\n" {
				t.Fatalf("output = %q", got)
			}
		})
	}
}

func TestFlameGraphWriter(t *testing.T) {
	snapshot := &irqTracingSnapshot{
		stacks: []*profiler.TreeItem{
			{Stack: [][]byte{[]byte("source"), []byte("raise_softirq_[k]")}, Value: 3},
		},
	}

	for _, format := range []string{outputFlameGraph, outputSVG} {
		t.Run(format, func(t *testing.T) {
			var destination bytes.Buffer
			outputWriter, err := newWriter(&destination, format, nil)
			if err != nil {
				t.Fatalf("newWriter() error = %v", err)
			}
			if err := outputWriter.Write(snapshot); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if got := destination.String(); !strings.Contains(got, "<svg") ||
				!strings.Contains(got, "raise_softirq_[k]") {
				t.Fatalf("output does not contain an SVG flame graph: %q", got)
			}
		})
	}
}

func TestJSONWriter(t *testing.T) {
	snapshot := &irqTracingSnapshot{result: &IRQTracingResult{NMissed: 3}}
	var output bytes.Buffer

	outputWriter, err := newWriter(&output, outputJSON, nil)
	if err != nil {
		t.Fatalf("newWriter() error = %v", err)
	}
	if err := outputWriter.Write(snapshot); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	var result IRQTracingResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.NMissed != 3 {
		t.Fatalf("nmissed = %d, want 3", result.NMissed)
	}
}
