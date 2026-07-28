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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cilium/ebpf/btf"
)

func TestGoName(t *testing.T) {
	tests := map[string]string{
		"bpf_debug_event":    "BPFDebugEvent",
		"n_missed":           "NMissed",
		"net_rx_latency":     "NetRXLatency",
		"pid_tgid":           "PIDTGID",
		"total_n_missed":     "TotalNMissed",
		"unnamed_plain_name": "UnnamedPlainName",
	}
	for input, want := range tests {
		if got := goName(input); got != want {
			t.Errorf("goName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDomainForTypeRejectsAmbiguousPrefix(t *testing.T) {
	domains := []domain{
		{name: "net", prefix: "net_"},
		{name: "net_rx", prefix: "net_rx_"},
	}

	if _, ok := domainForType(domains, "net_rx_event"); ok {
		t.Fatal("domainForType accepted a type matching two domains")
	}
}

func TestDiscoverDomainsMapsPaths(t *testing.T) {
	root := t.TempDir()
	headerDir := filepath.Join(root, "bpf", "include", "abi")
	if err := os.MkdirAll(headerDir, 0o755); err != nil {
		t.Fatalf("mkdir headers: %v", err)
	}
	header := filepath.Join(headerDir, "sample_types.h")
	if err := os.WriteFile(header, nil, 0o600); err != nil {
		t.Fatalf("write header: %v", err)
	}

	domains, err := discoverDomains(root)
	if err != nil {
		t.Fatalf("discoverDomains: %v", err)
	}
	if len(domains) != 1 {
		t.Fatalf("domain count = %d, want 1", len(domains))
	}
	got := domains[0]
	if got.name != "sample" || got.prefix != "sample_" ||
		got.headerPath != header ||
		got.outputPath != filepath.Join(
			root,
			"internal",
			"bpf",
			"abi",
			"sample_types_generated.go",
		) {
		t.Errorf("unexpected domain mapping: %+v", got)
	}
}

func TestValidateCandidateCoverageReportsMissingDomain(t *testing.T) {
	domains := []domain{{
		name:       "sample",
		prefix:     "sample_",
		headerPath: "sample_types.h",
	}}
	err := validateCandidateCoverage(domains, nil)
	if err == nil || !strings.Contains(err.Error(), `"sample_" btf anchors`) {
		t.Fatalf("validateCandidateCoverage error = %v", err)
	}
}

func TestGenerateDomainPreservesNestedLayoutAndPadding(t *testing.T) {
	u8 := &btf.Int{Name: "u8", Size: 1}
	u32 := &btf.Int{Name: "u32", Size: 4}
	nested := &btf.Struct{
		Name: "sample_nested",
		Size: 8,
		Members: []btf.Member{
			{Name: "tag", Type: u8, Offset: 0},
			{
				Name:   "value",
				Type:   &btf.Typedef{Name: "sample_value", Type: u32},
				Offset: 32,
			},
		},
	}
	event := &btf.Struct{
		Name: "sample_event",
		Size: 16,
		Members: []btf.Member{
			{Name: "nested", Type: nested, Offset: 0},
			{
				Name:   "bytes",
				Type:   &btf.Array{Type: u8, Index: u32, Nelems: 3},
				Offset: 64,
			},
		},
	}
	types, err := mergeCandidates([]candidate{
		{cName: nested.Name, typ: nested, objectPath: "sample.o"},
		{cName: event.Name, typ: event, objectPath: "sample.o"},
	})
	if err != nil {
		t.Fatalf("mergeCandidates: %v", err)
	}

	output, err := generateDomain(types)
	if err != nil {
		t.Fatalf("generateDomain: %v", err)
	}
	for _, fragment := range []string{
		"type SampleEvent struct",
		"Nested SampleNested",
		"Bytes  [3]uint8",
		"_      [5]byte",
		"const SampleEventSize = 16",
		"unsafe.Offsetof(SampleEvent{}.Bytes)",
	} {
		if !bytes.Contains(output, []byte(fragment)) {
			t.Errorf("generated output does not contain %q:\n%s", fragment, output)
		}
	}
	second, err := generateDomain(types)
	if err != nil {
		t.Fatalf("generateDomain second run: %v", err)
	}
	if !bytes.Equal(output, second) {
		t.Fatal("identical input produced different output")
	}
}

func TestRecordGoNamesRejectsCollision(t *testing.T) {
	err := recordGoNames(
		make(map[string]string),
		[]mergedType{
			{cName: "sample_id", goName: "SampleID"},
			{cName: "sample_i_d", goName: "SampleID"},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("recordGoNames error = %v", err)
	}
}

func TestMergeCandidates(t *testing.T) {
	u32 := &btf.Int{Name: "u32", Size: 4}
	first := &btf.Struct{
		Name:    "sample_event",
		Size:    4,
		Members: []btf.Member{{Name: "value", Type: u32}},
	}
	equivalent := &btf.Struct{
		Name:    "sample_event",
		Size:    4,
		Members: []btf.Member{{Name: "value", Type: u32}},
	}
	types, err := mergeCandidates([]candidate{
		{cName: first.Name, typ: first, objectPath: "a.o"},
		{cName: equivalent.Name, typ: equivalent, objectPath: "b.o"},
	})
	if err != nil {
		t.Fatalf("merge equivalent candidates: %v", err)
	}
	if len(types) != 1 {
		t.Fatalf("merged type count = %d, want 1", len(types))
	}

	different := &btf.Struct{
		Name:    "sample_event",
		Size:    8,
		Members: []btf.Member{{Name: "value", Type: u32}},
	}
	_, err = mergeCandidates([]candidate{
		{cName: first.Name, typ: first, objectPath: "a.o"},
		{cName: different.Name, typ: different, objectPath: "b.o"},
	})
	if err == nil || !strings.Contains(err.Error(), "differs between objects") {
		t.Fatalf("merge different candidates error = %v", err)
	}
}

func TestValidateTypeRejectsUnsafeLayouts(t *testing.T) {
	u32 := &btf.Int{Name: "u32", Size: 4}
	tests := map[string]btf.Type{
		"pointer": &btf.Struct{
			Name:    "pointer_event",
			Size:    8,
			Members: []btf.Member{{Name: "value", Type: &btf.Pointer{Target: u32}}},
		},
		"union": &btf.Union{Name: "union_event", Size: 4},
		"bitfield": &btf.Struct{
			Name: "bitfield_event",
			Size: 4,
			Members: []btf.Member{{
				Name:         "value",
				Type:         u32,
				BitfieldSize: 1,
			}},
		},
		"non-byte offset": &btf.Struct{
			Name:    "offset_event",
			Size:    8,
			Members: []btf.Member{{Name: "value", Type: u32, Offset: 4}},
		},
		"bool": &btf.Int{Name: "bool", Size: 1, Encoding: btf.Bool},
	}
	for name, typ := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateType("sample.o", typ.TypeName(), typ); err == nil {
				t.Fatal("validateType accepted an unsafe layout")
			}
		})
	}
}

func TestCleanStaleOnlyRemovesGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "bpf", "abi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	stale := filepath.Join(dir, "stale_types_generated.go")
	manual := filepath.Join(dir, "manual_types_generated.go")
	if err := os.WriteFile(stale, []byte(generatedMarker), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := os.WriteFile(manual, []byte("package abi\n"), 0o600); err != nil {
		t.Fatalf("write manual file: %v", err)
	}

	if err := cleanStale(root, nil); err != nil {
		t.Fatalf("cleanStale: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale generated file still exists: %v", err)
	}
	if _, err := os.Stat(manual); err != nil {
		t.Errorf("manual file was removed: %v", err)
	}
}
