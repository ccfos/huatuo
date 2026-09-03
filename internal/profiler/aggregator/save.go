// Copyright 2025, 2026 The HuaTuo Authors
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

package aggregator

import (
	"context"
	"fmt"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	profilingresult "huatuo-bamai/internal/profiling/result"
	profilingstore "huatuo-bamai/pkg/profiling/store"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func (p *Pipeline) saveProfilingDocument(_ context.Context, data any) error {
	if p.pctx.ToolstreamClient == nil {
		return fmt.Errorf("toolstream client not initialized")
	}

	result, ok := data.(*profiler.ProfileData)
	if !ok {
		return fmt.Errorf("invalid pprof data for uploading: %T", data)
	}

	profile, err := convertProfile(&result.Profile)
	if err != nil {
		return fmt.Errorf("convert profile for upload: %w", err)
	}
	if profile.TimeNanos == 0 {
		return fmt.Errorf("profile start timestamp is required")
	}
	profileData := &profilingstore.ProfileData{
		ProfileType: result.ProfileType,
		Profile:     profile,
		Metrics:     newMetrics(int(p.overflowCount.Load())),
	}

	ev := &profilingresult.Event{
		ContainerID:      p.pctx.ContainerID,
		StartedTimestamp: time.Unix(0, profile.TimeNanos).UTC(),
		ProfileData:      profileData,
	}

	if err := p.pctx.ToolstreamClient.Send(ev); err != nil {
		log.WithField("tracer_id", p.tracerID).Errorf("failed to send profiling event: %v", err)
		return err
	}

	log.WithField("tracer_id", p.tracerID).Infof("profiling event sent via toolstream")

	return nil
}

func convertProfile(profile *ptree.Profile) (*profilev1.Profile, error) {
	data, err := profile.MarshalVT()
	if err != nil {
		return nil, err
	}
	converted := new(profilev1.Profile)
	if err := converted.UnmarshalVT(data); err != nil {
		return nil, err
	}
	return converted, nil
}
