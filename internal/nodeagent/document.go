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

// Package nodeagent contains Node-local services shared across observation kinds.
package nodeagent

import (
	"errors"
	"fmt"
	"os"
	"time"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/types"
)

const (
	defaultHostname = "huatuo-dev"
)

// DocumentInput contains fields supplied by an observation producer.
type DocumentInput struct {
	TracerName        string
	TracerID          string
	ContainerID       string
	StartedTimestamp  time.Time
	ObservedTimestamp time.Time
	TracerRunType     string
}

// DocumentBuilder enriches observation metadata with Node and container fields.
type DocumentBuilder struct {
	region   string
	hostname string
}

// NewDocumentBuilder binds Node-local metadata used by every generated document.
func NewDocumentBuilder(region, hostname string) *DocumentBuilder {
	if hostname == "" {
		detected, err := os.Hostname()
		if err == nil {
			hostname = detected
		} else {
			hostname = defaultHostname
		}
	}
	return &DocumentBuilder{region: region, hostname: hostname}
}

// Build creates shared document metadata and resolves optional container fields.
func (b *DocumentBuilder) Build(input *DocumentInput) (types.Document, error) {
	if b == nil {
		return types.Document{}, errors.New("document builder is required")
	}
	if input == nil {
		return types.Document{}, errors.New("document input is required")
	}
	document := types.Document{
		Hostname:      b.hostname,
		Region:        b.region,
		TracerName:    input.TracerName,
		TracerID:      input.TracerID,
		TracerRunType: input.TracerRunType,
	}
	if !input.StartedTimestamp.IsZero() {
		startedTimestamp := input.StartedTimestamp.UTC()
		document.StartedTimestamp = &startedTimestamp
	}
	if !input.ObservedTimestamp.IsZero() {
		observedTimestamp := input.ObservedTimestamp.UTC()
		document.ObservedTimestamp = &observedTimestamp
	}
	if input.ContainerID == "" {
		return document, nil
	}
	container, err := pod.ContainerByID(input.ContainerID)
	if err != nil {
		return types.Document{}, fmt.Errorf("get container %q: %w", input.ContainerID, err)
	}
	if container == nil {
		return types.Document{}, fmt.Errorf("container %q not found", input.ContainerID)
	}
	document.ContainerID = container.ID
	document.ContainerHostname = container.Hostname
	document.ContainerHostNamespace = container.LabelHostNamespace()
	document.ContainerType = container.Type.String()
	document.ContainerQoS = container.Qos.String()
	return document, nil
}
