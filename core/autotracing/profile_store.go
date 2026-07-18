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

package autotracing

import (
	"time"

	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/tracing"
)

const autotracingProfileSampleRate = 99

func saveAutotracingCPUProfile(
	tracerID string,
	tracerName string,
	containerID string,
	tracerTime time.Time,
	frames []flamegraph.FrameData,
) error {
	profileData, err := profiler.ParseFlamegraphFrames(
		tracerTime,
		profiler.ProfileTypeCpuSample,
		frames,
		&profiler.ParseOption{SampleRate: autotracingProfileSampleRate},
	)
	if err != nil {
		return err
	}

	return tracing.SaveProfile(&tracing.WriteRequest{
		TracerName:    tracerName,
		TracerID:      tracerID,
		ContainerID:   containerID,
		TracerTime:    tracerTime,
		TracerData:    &profctx.TracerData{FlameData: profileData},
		TracerRunType: tracing.TracerRunTypeAutotracing,
	})
}
