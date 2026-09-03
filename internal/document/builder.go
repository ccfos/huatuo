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

// Package document constructs metadata shared by persisted observations.
package document

import (
	"errors"
	"fmt"
	"os"
	"time"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/types"
)

const defaultHostname = "huatuo-dev"

// Input contains fields supplied by an observation producer.
type Input struct {
	TracerName        string
	TracerID          string
	ContainerID       string
	StartedTimestamp  time.Time
	ObservedTimestamp time.Time
	TracerRunType     string
}

// Builder enriches observation metadata with Node and container fields.
type Builder struct {
	region   string
	hostname string
}

// New binds Node-local metadata used by every generated document.
func New(region string) *Builder {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = defaultHostname
	}
	return &Builder{region: region, hostname: hostname}
}

// Build creates shared document metadata and resolves optional container fields.
func (b *Builder) Build(input *Input) (types.Document, error) {
	if b == nil {
		return types.Document{}, errors.New("document builder is required")
	}
	if input == nil {
		return types.Document{}, errors.New("document input is required")
	}
	metadata := types.Document{
		Hostname:      b.hostname,
		Region:        b.region,
		TracerName:    input.TracerName,
		TracerID:      input.TracerID,
		TracerRunType: input.TracerRunType,
	}
	if !input.StartedTimestamp.IsZero() {
		startedTimestamp := input.StartedTimestamp.UTC()
		metadata.StartedTimestamp = &startedTimestamp
	}
	if !input.ObservedTimestamp.IsZero() {
		observedTimestamp := input.ObservedTimestamp.UTC()
		metadata.ObservedTimestamp = &observedTimestamp
	}
	if input.ContainerID == "" {
		return metadata, nil
	}
	container, err := pod.ContainerByID(input.ContainerID)
	if err != nil {
		return types.Document{}, fmt.Errorf("get container %q: %w", input.ContainerID, err)
	}
	if container == nil {
		return types.Document{}, fmt.Errorf("container %q not found", input.ContainerID)
	}
	metadata.ContainerID = container.ID
	metadata.ContainerHostname = container.Hostname
	metadata.ContainerHostNamespace = container.LabelHostNamespace()
	metadata.ContainerType = container.Type.String()
	metadata.ContainerQoS = container.Qos.String()
	return metadata, nil
}
