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
	"context"
	"errors"
	"time"

	"huatuo-bamai/internal/storage"
)

const (
	tracingDocumentTimeLayout = "2006-01-02 15:04:05.000 -0700"

	TracerRunTypeTask        = "task"
	TracerRunTypeAutotracing = "autotracing"
	TracerRunTypeEvent       = "event"
)

// DocumentOptions contains common fields applied to tracing documents.
type DocumentOptions struct {
	Region   string
	Hostname string
}

// WriteRequest carries the parameters for a single document write operation.
type WriteRequest struct {
	TracerName    string
	TracerID      string
	ContainerID   string
	TracerTime    time.Time
	TracerData    any
	TracerRunType string
}

var (
	tracingDataWriter *documentWriter
	taskDataWriter    *documentWriter
	profileDataWriter *documentWriter
)

// SetTracingStore configures stores for tracing documents.
func SetTracingStore(stores []*storage.Store[*Document], options DocumentOptions) {
	if len(stores) == 0 {
		tracingDataWriter = nil
		return
	}

	tracingDataWriter = newDocumentWriter(stores, options)
}

// Save writes tracing data when a tracing document store is configured.
func Save(req *WriteRequest) error {
	if tracingDataWriter == nil {
		return nil
	}

	if req.TracerRunType == "" {
		req.TracerRunType = TracerRunTypeEvent
	}

	return tracingDataWriter.saveRaw(req)
}

// SetTaskStore configures stores for task output.
func SetTaskStore(stores []*storage.Store[*Document], options DocumentOptions) {
	if len(stores) == 0 {
		taskDataWriter = nil
		return
	}

	taskDataWriter = newDocumentWriter(stores, options)
}

// SetProfileStore configures stores for profiling documents.
func SetProfileStore(stores []*storage.Store[*Document], options DocumentOptions) {
	if len(stores) == 0 {
		profileDataWriter = nil
		return
	}

	profileDataWriter = newDocumentWriter(stores, options)
}

// SaveProfile writes profiling data when a profile document store is configured.
func SaveProfile(req *WriteRequest) error {
	if profileDataWriter == nil {
		return nil
	}

	if req.TracerRunType == "" {
		req.TracerRunType = TracerRunTypeAutotracing
	}

	return profileDataWriter.saveRaw(req)
}

// SaveTaskOutputText stores task output as plain text.
func SaveTaskOutputText(req *WriteRequest) error {
	if taskDataWriter == nil {
		return nil
	}

	req.TracerRunType = TracerRunTypeTask
	return taskDataWriter.saveText(req)
}

// SaveTaskOutputJSON stores task output as JSON.
func SaveTaskOutputJSON(req *WriteRequest) error {
	if taskDataWriter == nil {
		return nil
	}

	req.TracerRunType = TracerRunTypeTask
	return taskDataWriter.saveJSON(req)
}

// CloseStores flushes and releases every configured tracing/task store. The
// same Store may be registered under both writers; close it only once. All
// close errors are joined and returned so the caller can observe every
// failure.
func CloseStores(ctx context.Context) error {
	seen := make(map[*storage.Store[*Document]]struct{})
	var errs []error

	closeAll := func(stores []*storage.Store[*Document]) {
		for _, st := range stores {
			if st == nil {
				continue
			}
			if _, ok := seen[st]; ok {
				continue
			}
			seen[st] = struct{}{}
			if err := st.Close(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if tracingDataWriter != nil {
		closeAll(tracingDataWriter.stores)
	}
	if taskDataWriter != nil {
		closeAll(taskDataWriter.stores)
	}
	if profileDataWriter != nil {
		closeAll(profileDataWriter.stores)
	}
	return errors.Join(errs...)
}
