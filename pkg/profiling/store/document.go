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

// Package store defines the persisted profiling result and its storage access.
package store

import (
	"errors"

	"github.com/ccfos/huatuo/pkg/types"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

// Document is one persisted profiling aggregation window.
type Document struct {
	types.Document

	ProfileData *ProfileData `json:"profile_data,omitempty"`
}

// Metrics contains diagnostics captured for one aggregation window.
type Metrics struct {
	AggrOverflowCount int `json:"aggr_overflow_count"`
}

// ProfileData contains one Pyroscope-compatible profile and its diagnostics.
type ProfileData struct {
	ProfileType string             `json:"profile_type"`
	Profile     *profilev1.Profile `json:"profile"`
	Metrics     *Metrics           `json:"metrics,omitempty"`
}

func (d *Document) validate() error {
	if d == nil {
		return errors.New("profiling document is required")
	}
	if err := d.Document.Validate(); err != nil {
		return err
	}
	if d.TracerID == "" {
		return errors.New("profiling document tracer id is required")
	}
	if d.ProfileData == nil {
		return errors.New("profiling document profile data is required")
	}
	if d.ProfileData.ProfileType == "" {
		return errors.New("profiling document profile type is required")
	}
	if d.ProfileData.Profile == nil {
		return errors.New("profiling document profile is required")
	}
	return nil
}
