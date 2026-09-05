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

package tracing

import (
	"errors"
	"testing"
	"time"

	"huatuo-bamai/internal/pod"
)

func testWriteRequest(containerID string) *WriteRequest {
	return &WriteRequest{
		TracerName:  "test-tracer",
		TracerTime:  time.Now(),
		TracerData:  map[string]any{"payload": "keep"},
		ContainerID: containerID,
	}
}

func TestNewBaseDocumentAttributesContainer(t *testing.T) {
	orig := containerByID
	defer func() { containerByID = orig }()
	containerByID = func(id string) (*pod.Container, error) {
		return &pod.Container{ID: id, Hostname: "pod-1", Labels: map[string]any{"HostNamespace": "ns-1"}}, nil
	}

	document, err := newBaseDocument(DocumentOptions{Hostname: "host-1"}, testWriteRequest("container-1"))
	if err != nil {
		t.Fatalf("newBaseDocument() error = %v", err)
	}
	if document.ContainerID != "container-1" {
		t.Errorf("ContainerID = %q, want container-1", document.ContainerID)
	}
	if document.ContainerHostname != "pod-1" {
		t.Errorf("ContainerHostname = %q, want pod-1", document.ContainerHostname)
	}
	if document.Hostname != "host-1" {
		t.Errorf("Hostname = %q, want host-1", document.Hostname)
	}
}

// A container can vanish between the event and the save (an OOM victim dies
// from the very OOM being reported). The document must be kept with
// host-level attribution, matching how collectors treat unresolvable
// containers, instead of being dropped.
func TestNewBaseDocumentKeepsDocumentWhenContainerVanished(t *testing.T) {
	orig := containerByID
	defer func() { containerByID = orig }()
	containerByID = func(string) (*pod.Container, error) {
		return nil, nil
	}

	document, err := newBaseDocument(DocumentOptions{Hostname: "host-1"}, testWriteRequest("gone-container"))
	if err != nil {
		t.Fatalf("newBaseDocument() dropped the document: %v", err)
	}
	if document.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty host-level attribution", document.ContainerID)
	}
	if document.TracerName != "test-tracer" {
		t.Errorf("TracerName = %q, want test-tracer", document.TracerName)
	}
}

func TestNewBaseDocumentKeepsDocumentWhenContainerLookupFails(t *testing.T) {
	orig := containerByID
	defer func() { containerByID = orig }()
	containerByID = func(string) (*pod.Container, error) {
		return nil, errors.New("kubelet sync failed")
	}

	document, err := newBaseDocument(DocumentOptions{Hostname: "host-1"}, testWriteRequest("some-container"))
	if err != nil {
		t.Fatalf("newBaseDocument() dropped the document: %v", err)
	}
	if document.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty host-level attribution", document.ContainerID)
	}
}

func TestNewBaseDocumentHostLevelWithoutContainerID(t *testing.T) {
	orig := containerByID
	defer func() { containerByID = orig }()
	containerByID = func(string) (*pod.Container, error) {
		t.Fatal("containerByID must not be called without a container ID")
		return nil, nil
	}

	document, err := newBaseDocument(DocumentOptions{Hostname: "host-1"}, testWriteRequest(""))
	if err != nil {
		t.Fatalf("newBaseDocument() error = %v", err)
	}
	if document.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty", document.ContainerID)
	}
}
