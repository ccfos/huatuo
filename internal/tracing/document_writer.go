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
	"sync/atomic"
	"time"

	"github.com/rs/xid"

	"huatuo-bamai/internal/document"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	"huatuo-bamai/pkg/types"
)

// WriteRequest carries one heterogeneous tracing event.
type WriteRequest struct {
	TracerName        string
	TracerID          string
	ContainerID       string
	StartedTimestamp  time.Time
	ObservedTimestamp time.Time
	TracerData        any
	TracerRunType     string
}

type documentWriter struct {
	store     *tracingstore.Store
	documents *document.Builder
}

var configuredWriter atomic.Pointer[documentWriter]

// ConfigureWriter installs the process-wide writer used by registered tracers.
func ConfigureWriter(store *tracingstore.Store, documents *document.Builder) error {
	if store == nil {
		configuredWriter.Store(nil)
		return nil
	}
	if documents == nil {
		return errors.New("configure tracing writer: document builder is required")
	}
	configuredWriter.Store(&documentWriter{store: store, documents: documents})
	return nil
}

// Save enriches and publishes tracing data when the process-wide writer is enabled.
func Save(request *WriteRequest) error {
	current := configuredWriter.Load()
	if current == nil {
		return nil
	}
	if request == nil {
		return errors.New("save tracing event: write request is required")
	}
	tracerID := request.TracerID
	if tracerID == "" {
		tracerID = xid.New().String()
	}
	runType := request.TracerRunType
	if runType == "" {
		runType = types.TracerRunTypeEvent
	}
	metadata, err := current.documents.Build(&document.Input{
		TracerName:        request.TracerName,
		TracerID:          tracerID,
		ContainerID:       request.ContainerID,
		StartedTimestamp:  request.StartedTimestamp,
		ObservedTimestamp: request.ObservedTimestamp,
		TracerRunType:     runType,
	})
	if err != nil {
		return err
	}
	return current.store.Save(&tracingstore.Document{
		Document:   metadata,
		TracerData: request.TracerData,
	})
}
