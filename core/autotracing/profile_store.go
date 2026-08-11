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
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/tracing"

	"github.com/rs/xid"
)

const autotracingProfileSampleRate = 99

func saveAutotracingCPUEvent(
	request *tracing.WriteRequest,
	duration time.Duration,
	frames []flamegraph.FrameData,
) error {
	if request.TracerID == "" {
		request.TracerID = xid.New().String()
	}

	tracingErr := tracing.Save(request)
	foldedErr := exportAutotracingFoldedSnapshot(request, frames)
	profileData, profileErr := profiler.ParseFlamegraphFrames(
		request.TracerTime,
		duration,
		profiler.ProfileTypeCpuSample,
		frames,
		&profiler.ParseOption{SampleRate: autotracingProfileSampleRate},
	)
	if profileErr == nil {
		profileErr = tracing.SaveProfile(&tracing.WriteRequest{
			TracerName:    request.TracerName,
			TracerID:      request.TracerID,
			ContainerID:   request.ContainerID,
			TracerTime:    request.TracerTime,
			TracerData:    &profctx.TracerData{FlameData: profileData},
			TracerRunType: tracing.TracerRunTypeAutotracing,
		})
	}

	return errors.Join(
		wrapAutotracingSaveError("JSON event", tracingErr),
		wrapAutotracingSaveError("folded stacks", foldedErr),
		wrapAutotracingSaveError("pprof profile", profileErr),
	)
}

func wrapAutotracingSaveError(target string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("save AutoTracing %s: %w", target, err)
}
