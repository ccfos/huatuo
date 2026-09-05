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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/rs/xid"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
)

const defaultHostname = "huatuo-dev"

type documentWriter struct {
	stores  []*storage.Store[*Document]
	options DocumentOptions
}

func newDocumentWriter(
	stores []*storage.Store[*Document],
	options DocumentOptions,
) *documentWriter {
	return &documentWriter{
		stores:  stores,
		options: options,
	}
}

func (s *documentWriter) saveText(req *WriteRequest) error {
	req.TracerData = map[string]any{"output": req.TracerData}
	document, err := newBaseDocument(s.options, req)
	if err != nil {
		return err
	}
	return s.saveDocument(document)
}

func (s *documentWriter) saveJSON(req *WriteRequest) error {
	raw, ok := req.TracerData.(string)
	if !ok {
		return fmt.Errorf("task output store: tracerData must be a string for JSON output")
	}

	var tracerDataMap map[string]any
	if err := json.Unmarshal([]byte(raw), &tracerDataMap); err != nil {
		return fmt.Errorf("task output store: unmarshal tracer data: %w", err)
	}

	req.TracerData = tracerDataMap
	document, err := newBaseDocument(s.options, req)
	if err != nil {
		return err
	}
	return s.saveDocument(document)
}

func (s *documentWriter) saveRaw(req *WriteRequest) error {
	document, err := newBaseDocument(s.options, req)
	if err != nil {
		return err
	}

	return s.saveDocument(document)
}

func (s *documentWriter) saveDocument(document *Document) error {
	NotifySubscribers(document)

	var errs []error
	for _, store := range s.stores {
		if store == nil {
			continue
		}
		if err := store.Save(context.Background(), document); err != nil {
			errs = append(errs, fmt.Errorf("[storage backend: %s, err: %w]", store.Name, err))
		}
	}

	return errors.Join(errs...)
}

// containerByID resolves a container for document attribution; overridable
// in tests.
var containerByID = pod.ContainerByID

func newBaseDocument(options DocumentOptions, req *WriteRequest) (*Document, error) {
	formattedTime := req.TracerTime.Format(tracingDocumentTimeLayout)
	document := Document{
		Hostname:      setDocumentHostnameWithDefault(options.Hostname),
		Region:        options.Region,
		UploadedTime:  time.Now(),
		Time:          formattedTime,
		TracerName:    req.TracerName,
		TracerTime:    formattedTime,
		TracerRunType: req.TracerRunType,
		TracerData:    req.TracerData,
		TracerID:      req.TracerID,
	}
	if document.TracerID == "" {
		document.TracerID = xid.New().String()
	}

	if req.ContainerID == "" {
		return &document, nil
	}

	// The container can vanish between the event and this save - an OOM
	// victim dies from the very OOM being reported, or a profiled container
	// exits mid-run. Keep the document with host-level attribution instead
	// of dropping it, matching how collectors treat unresolvable containers
	// (e.g. oom.go counts them as host events).
	container, err := containerByID(req.ContainerID)
	if err != nil || container == nil {
		return &document, nil
	}

	document.ContainerID = container.ID
	document.ContainerHostname = container.Hostname
	document.ContainerHostNamespace = container.LabelHostNamespace()
	document.ContainerType = container.Type.String()
	document.ContainerQoS = container.Qos.String()
	return &document, nil
}

func setDocumentHostnameWithDefault(hostname string) string {
	if hostname != "" {
		return hostname
	}

	detectedHostname, err := os.Hostname()
	if err != nil {
		return defaultHostname
	}

	return detectedHostname
}
