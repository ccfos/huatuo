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

// Package profiling constructs and persists profiling results.
package profiling

import (
	"context"
	"errors"
	"time"

	"huatuo-bamai/internal/document"
	"huatuo-bamai/internal/toolstream"
	profilingstore "huatuo-bamai/pkg/profiling/store"
	"huatuo-bamai/pkg/types"
)

// DocumentWriter persists profiling documents received over Toolstream.
type DocumentWriter struct {
	store     *profilingstore.Store
	documents *document.Builder
}

// NewDocumentWriter creates a profiling document writer.
func NewDocumentWriter(
	store *profilingstore.Store,
	documents *document.Builder,
) (*DocumentWriter, error) {
	if store == nil {
		return nil, errors.New("create profiling document writer: store is required")
	}
	if documents == nil {
		return nil, errors.New("create profiling document writer: document builder is required")
	}
	return &DocumentWriter{store: store, documents: documents}, nil
}

// Write persists Operation results synchronously and standalone results asynchronously.
func (w *DocumentWriter) Write(
	session *toolstream.Session,
	window *types.ProfilingWindow,
) error {
	if window == nil {
		return errors.New("profiling window is required")
	}
	if session == nil || session.Session == nil {
		return errors.New("profiling result session is required")
	}
	if session.TaskID == "" {
		return errors.New("profiling result session task id is required")
	}
	if window.ProfileType == "" {
		return errors.New("profiling window profile type is required")
	}
	if window.Profile == nil {
		return errors.New("profiling window profile is required")
	}
	if window.Profile.TimeNanos == 0 {
		return errors.New("profiling window profile start timestamp is required")
	}
	metadata, err := w.documents.Build(&document.Input{
		TracerName:       types.ProfilingToolName,
		TracerID:         session.TaskID,
		ContainerID:      window.ContainerID,
		StartedTimestamp: time.Unix(0, window.Profile.TimeNanos).UTC(),
		TracerRunType:    types.TracerRunTypeProfiling,
	})
	if err != nil {
		return err
	}
	document := &profilingstore.Document{
		Document: metadata,
		ProfileData: &profilingstore.ProfileData{
			ProfileType: window.ProfileType,
			Profile:     window.Profile,
			Metrics: &profilingstore.Metrics{
				AggrOverflowCount: window.AggregationOverflowCount,
			},
		},
	}
	if session.IsExpected {
		return w.store.SaveSync(context.Background(), document)
	}
	return w.store.Save(context.Background(), document)
}
