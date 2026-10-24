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

//go:build integration

package bpfmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type Document[Event any] struct {
	ContainerID string `json:"container_id"`
	TracerName  string `json:"tracer_name"`
	TracerType  string `json:"tracer_type"`
	TracerID    string `json:"tracer_id"`
	Hostname    string `json:"hostname"`
	Region      string `json:"region"`
	Observed    string `json:"observed_timestamp"`
	Uploaded    string `json:"uploaded_timestamp"`
	Data        Event  `json:"tracer_data"`
}

func (d *Document[Event]) ValidateMetadata(tracer string) error {
	if d.TracerName != tracer || d.TracerType != "event" || d.TracerID == "" ||
		d.Hostname == "" || d.Region == "" {
		return errors.New("incomplete or incorrect ES event metadata")
	}
	for _, timestamp := range []string{d.Observed, d.Uploaded} {
		if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err != nil || parsed.IsZero() {
			return fmt.Errorf("invalid ES event timestamp %q", timestamp)
		}
	}
	return nil
}

// ReadDocuments decodes the _source records retrieved by the ES shell helper.
func ReadDocuments[Event any](path string) ([]Document[Event], error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var documents []Document[Event]
	decoder := json.NewDecoder(file)
	for {
		var doc Document[Event]
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return documents, nil
			}
			return nil, fmt.Errorf("decode ES document: %w", err)
		}
		documents = append(documents, doc)
	}
}

func ReadJSON[Value any](path string) (Value, error) {
	var value Value
	body, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(body, &value)
	}
	if err != nil {
		return value, fmt.Errorf("read %s: %w", path, err)
	}
	return value, nil
}

// FindDocumentByTriggerComm returns at most one document for a trigger task name.
// A nil result means no event was published for that task.
func FindDocumentByTriggerComm[Event interface{ TriggerComm() string }](
	documents []Document[Event], comm string,
) (*Document[Event], error) {
	var found *Document[Event]
	for i := range documents {
		if documents[i].Data.TriggerComm() != comm {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple ES documents for trigger comm %q", comm)
		}
		found = &documents[i]
	}
	return found, nil
}
