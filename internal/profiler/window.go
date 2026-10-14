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

package profiler

import (
	"fmt"
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/pkg/types"
)

// NewWindow converts the internal profile into the common wire and output model.
func NewWindow(data *ProfileData, containerID string, duration time.Duration) (*types.ProfilingWindow, error) {
	encoded, err := data.Profile.MarshalVT()
	if err != nil {
		return nil, fmt.Errorf("encode profile: %w", err)
	}
	profile := new(profilev1.Profile)
	if err := profile.UnmarshalVT(encoded); err != nil {
		return nil, fmt.Errorf("decode profile: %w", err)
	}
	profile.DurationNanos = int64(duration)
	return &types.ProfilingWindow{ContainerID: containerID, ProfileType: data.ProfileType, Profile: profile}, nil
}
