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

func newBaseDocument(options DocumentOptions, req *WriteRequest) (*Document, error) {
	formattedTime := req.TracerTime.Format(tracingDocumentTimeLayout)
	document := Document{
		Hostname:      setDocumentHostnameWithDefault(options.Hostname),
		Region:        options.Region,
		NodeIP:        options.NodeIP,
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

	container, err := pod.ContainerByID(req.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("get container %s: %w", req.ContainerID, err)
	}
	if container == nil {
		return nil, fmt.Errorf("container %s not found", req.ContainerID)
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
