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

// Package store defines tracing documents and their persistence behavior.
package store

import (
	"errors"

	"huatuo-bamai/pkg/types"
)

// Document is one heterogeneous tracing event persisted by the Node agent.
type Document struct {
	types.Document
	TracerData any `json:"tracer_data,omitempty"`
}

func (d *Document) validate() error {
	if d == nil {
		return errors.New("tracing document is required")
	}
	return d.Document.Validate()
}
