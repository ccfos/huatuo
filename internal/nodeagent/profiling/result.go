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

package profiling

import (
	"context"
	"errors"
	"fmt"

	"huatuo-bamai/internal/document"
	profilingresult "huatuo-bamai/internal/profiling/result"
	"huatuo-bamai/internal/toolstream"
	profilingstore "huatuo-bamai/pkg/profiling/store"
	"huatuo-bamai/pkg/types"
)

// ResultWriter persists typed profiling results received over Toolstream.
type ResultWriter struct {
	store     *profilingstore.Store
	documents *document.Builder
}

// NewResultWriter creates a profiling result writer.
func NewResultWriter(
	store *profilingstore.Store,
	documents *document.Builder,
) (*ResultWriter, error) {
	if store == nil {
		return nil, errors.New("create profiling result writer: store is required")
	}
	if documents == nil {
		return nil, errors.New("create profiling result writer: document builder is required")
	}
	return &ResultWriter{store: store, documents: documents}, nil
}

// Write persists Operation results synchronously and standalone results asynchronously.
func (w *ResultWriter) Write(
	session *toolstream.Session,
	event *profilingresult.Event,
) error {
	if event == nil {
		return errors.New("profiling result event is required")
	}
	if session == nil || session.Session == nil {
		return errors.New("profiling result session is required")
	}
	if event.TracerID == "" {
		return errors.New("profiling result tracer id is required")
	}
	if event.TracerName == "" {
		return errors.New("profiling result tracer name is required")
	}
	if session.TaskID != event.TracerID {
		return fmt.Errorf(
			"profiling result tracer id %q does not match session task id %q",
			event.TracerID,
			session.TaskID,
		)
	}
	if event.TracerRunType != types.TracerRunTypeProfiling {
		return fmt.Errorf(
			"profiling result tracer type %q is not supported",
			event.TracerRunType,
		)
	}
	metadata, err := w.documents.Build(&document.Input{
		TracerName:       event.TracerName,
		TracerID:         event.TracerID,
		ContainerID:      event.ContainerID,
		StartedTimestamp: event.StartedTimestamp,
		TracerRunType:    event.TracerRunType,
	})
	if err != nil {
		return err
	}
	document := &profilingstore.Document{
		Document:    metadata,
		ProfileData: event.ProfileData,
	}
	if session.IsExpected {
		return w.store.SaveSync(context.Background(), document)
	}
	return w.store.Save(context.Background(), document)
}
