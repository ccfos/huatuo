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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/flamegraph"
)

func checkFlames(data []byte, comm, forbid string, kernel bool) error {
	var events []struct {
		TracerData struct {
			FlameData []flamegraph.FrameData `json:"flamedata"`
		} `json:"tracer_data"`
	}
	if err := json.Unmarshal(data, &events); err != nil {
		return err
	}
	if len(events) == 0 || comm == "" {
		return errors.New("flame validation requires events and an expected worker comm")
	}
	for i := range events {
		if err := checkFrames(events[i].TracerData.FlameData, comm, forbid, kernel); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
	}
	return nil
}

func checkFrames(frames []flamegraph.FrameData, comm, forbid string, kernel bool) error {
	if len(frames) < 2 || frames[0].Level != 0 || frames[0].Value <= 0 {
		return errors.New("flame graph has no positive root and sampled worker")
	}
	parents := make([]int, 0, len(frames))
	children := make([]int64, len(frames))
	foundComm, foundKernel := false, false
	for i := range frames {
		frame := &frames[i]
		if frame.Level < 0 || frame.Level > int64(len(parents)) ||
			(i > 0 && frame.Level == 0) || frame.Value <= 0 || frame.Self < 0 || frame.Self > frame.Value || frame.Label == "" {
			return fmt.Errorf("invalid flame frame %d: %+v", i, frame)
		}
		parents = parents[:int(frame.Level)]
		if len(parents) > 0 {
			children[parents[len(parents)-1]] += frame.Value
		}
		parents = append(parents, i)
		foundComm = foundComm || frame.Label == comm
		foundKernel = foundKernel || strings.HasSuffix(frame.Label, "_[k]")
		if forbid != "" && frame.Label == forbid {
			return fmt.Errorf("unrelated worker %q appears in the target flame graph", forbid)
		}
	}
	for i := range frames {
		if frames[i].Self+children[i] != frames[i].Value {
			return fmt.Errorf("frame %d value=%d, self+children=%d", i, frames[i].Value, frames[i].Self+children[i])
		}
	}
	if !foundComm || (kernel && !foundKernel) {
		return fmt.Errorf("flame graph worker=%t kernel=%t, expected comm=%q require-kernel=%t", foundComm, foundKernel, comm, kernel)
	}
	return nil
}
