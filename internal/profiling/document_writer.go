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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	"github.com/ccfos/huatuo/pkg/types"
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

	return &DocumentWriter{
		store:     store,
		documents: documents,
	}, nil
}

// Handle persists each supplied window even after earlier failures. Only an
// error-free task may publish its completion marker.
func (w *DocumentWriter) Handle(ctx context.Context, request *toolstream.Request[types.ProfilingWindow]) error {
	if request == nil {
		return errors.New("profiling result request is required")
	}

	if request.ToolName == "" {
		return errors.New("profiling result tool name is required")
	}

	if request.TaskID == "" {
		return errors.New("profiling result request task id is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var writeErr error
	if request.Data != nil {
		writeErr = w.writeWindow(ctx, request)
	}

	if writeErr != nil {
		return writeErr
	}

	if request.End && !request.Commit {
		return errors.New("profiling result stream ended without commit")
	}

	if !request.Commit || request.PriorErr != nil || request.Err != nil {
		return nil
	}

	if !request.End {
		return errors.New("profiling commit requires end")
	}

	return w.store.Commit(ctx, request.TaskID)
}

func (w *DocumentWriter) writeWindow(ctx context.Context, request *toolstream.Request[types.ProfilingWindow]) error {
	window := request.Data

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
		TracerName:       request.ToolName,
		TracerID:         request.TaskID,
		ContainerID:      window.ContainerID,
		StartedTimestamp: timeutil.Timestamp{Time: time.Unix(0, window.Profile.TimeNanos)},
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
				DroppedSamples: window.DroppedSamples,
			},
		},
	}

	return w.store.SaveSync(ctx, document)
}

// HandleStream dispatches business schemas independently of Toolstream framing.
// Version 2 aggregate payloads are rejected, rather than partially decoded.
func (w *DocumentWriter) HandleStream(ctx context.Context, request *toolstream.Request[json.RawMessage]) error {
	if request == nil {
		return errors.New("profiling result request is required")
	}

	if request.Version != "1" && request.Version != "2" && request.Version != "3" {
		return fmt.Errorf("unsupported profiling business version %q", request.Version)
	}

	legacy := &toolstream.Request[types.ProfilingWindow]{
		ServiceName: request.ServiceName,
		ToolName:    request.ToolName,
		Version:     request.Version,
		TaskID:      request.TaskID,
		End:         request.End,
		Commit:      request.Commit,
		PriorErr:    request.PriorErr,
		Err:         request.Err,
	}
	if request.Data != nil {
		decoder := json.NewDecoder(bytes.NewReader(*request.Data))
		decoder.DisallowUnknownFields()
		if request.Version == "3" {
			var window types.AggregationWindow
			if err := decoder.Decode(&window); err != nil {
				return fmt.Errorf("decode profiling aggregation window: %w", err)
			}

			if err := profilewindow.Validate(&window); err != nil {
				return err
			}

			profile, err := profilewindow.BuildProfile(ctx, &window)
			if err != nil {
				return err
			}

			legacy.Data = &types.ProfilingWindow{
				ContainerID:    window.ContainerID,
				ProfileType:    window.ProfileType,
				Profile:        profile,
				DroppedSamples: window.DroppedSamples,
			}
		} else {
			legacy.Data = &types.ProfilingWindow{}
			if err := decoder.Decode(legacy.Data); err != nil {
				return fmt.Errorf("decode legacy complete pprof: %w", err)
			}
		}
	}

	return w.Handle(ctx, legacy)
}
