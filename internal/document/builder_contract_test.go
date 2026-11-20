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

package document_test

import (
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/pkg/types"
)

// These tests exercise the exported constructor and the host-scoped input
// path through the public API only; the in-package tests already cover the
// local hostname/region wiring with literal Builder values.

func TestNewFillsRegionAndHostname(t *testing.T) {
	builder := document.New("region-1")

	metadata, err := builder.Build(&document.Input{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if metadata.Region != "region-1" {
		t.Fatalf("Region = %q, want %q", metadata.Region, "region-1")
	}

	// The hostname falls back to a stable development value when the OS
	// lookup fails, so it must never be empty.
	if metadata.Hostname == "" {
		t.Fatal("Hostname is empty")
	}
}

func TestBuildRejectsNilInput(t *testing.T) {
	builder := document.New("region-1")

	if _, err := builder.Build(nil); err == nil || !strings.Contains(err.Error(), "document input is required") {
		t.Fatalf("Build() with nil input error = %v, want input-required", err)
	}
}

// TestBuildOmitsZeroTimestamps pins the invariant that a producer must not be
// able to plant epoch-zero timestamps: zero-valued inputs leave the pointer
// fields unset instead of serializing 0001-01-01.
func TestBuildOmitsZeroTimestamps(t *testing.T) {
	metadata, err := document.New("region-1").Build(&document.Input{
		TracerName:    "iotracing",
		TracerID:      "task-1",
		TracerRunType: types.TracerRunTypeAutotracing,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if metadata.StartedTimestamp != nil {
		t.Fatalf("StartedTimestamp = %v, want nil for a zero input", metadata.StartedTimestamp)
	}
	if metadata.ObservedTimestamp != nil {
		t.Fatalf("ObservedTimestamp = %v, want nil for a zero input", metadata.ObservedTimestamp)
	}
}

// TestBuildWithoutContainerLeavesContainerFieldsEmpty covers the early return
// for host-scoped observations: no container lookup may happen and every
// container field stays empty.
func TestBuildWithoutContainerLeavesContainerFieldsEmpty(t *testing.T) {
	metadata, err := document.New("region-1").Build(&document.Input{
		TracerName:    "netdev",
		TracerRunType: types.TracerRunTypeEvent,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if metadata.ContainerID != "" || metadata.ContainerHostname != "" ||
		metadata.ContainerHostNamespace != "" || metadata.ContainerType != "" ||
		metadata.ContainerQoS != "" {
		t.Fatalf("container fields not empty for a host-scoped input: %+v", metadata)
	}
}
