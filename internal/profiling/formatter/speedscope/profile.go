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

package speedscope

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// Browser viewers use IEEE-754 numbers even when JSON carries integer weights.
const maxExactInteger int64 = 1<<53 - 1

type profileDecoder struct {
	reader                       *output.ProfileReader
	sampleType, sampleUnit, unit string
}

func newProfileDecoder(p *profilev1.Profile) (*profileDecoder, error) {
	if len(p.StringTable) == 0 || p.StringTable[0] != "" {
		return nil, fmt.Errorf("speedscope: profile string table must start with an empty string")
	}

	reader, err := output.NewProfileReader(p)
	if err != nil {
		return nil, err
	}

	d := &profileDecoder{reader: reader}
	if len(p.SampleType) == 0 || p.SampleType[0] == nil {
		return nil, fmt.Errorf("speedscope: first sample type is required")
	}

	d.sampleType, err = d.reader.Text(p.SampleType[0].Type)
	if err != nil {
		return nil, err
	}

	if d.sampleType == "" {
		return nil, fmt.Errorf("speedscope: sample type name is required")
	}

	d.sampleUnit, err = d.reader.Text(p.SampleType[0].Unit)
	if err != nil {
		return nil, err
	}

	switch d.sampleUnit {
	case "nanoseconds", "microseconds", "milliseconds", "seconds", "bytes", "none":
		d.unit = d.sampleUnit
	case "count", "counts":
		d.unit = "none"
	default:
		return nil, fmt.Errorf("speedscope: unsupported sample unit %q", d.sampleUnit)
	}

	return d, nil
}

func (d *profileDecoder) frames(sample *profilev1.Sample) ([]frame, error) {
	var frames []frame
	err := d.reader.WalkStack(sample, func(name, file string, line int64) error {
		if line < 0 || line > maxExactInteger {
			return fmt.Errorf("speedscope: invalid source line %d", line)
		}

		frames = append(frames, frame{Name: name, File: file, Line: line})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return frames, nil
}

// All labels participate in grouping. This preserves explicit process/thread
// identity without interpreting frame names or inventing absent identities.
func (d *profileDecoder) group(window *types.ProfilingWindow, sample *profilev1.Sample) (string, string, error) {
	var labels []string
	for _, label := range sample.Label {
		if label == nil {
			return "", "", fmt.Errorf("speedscope: nil sample label")
		}

		key, err := d.reader.Text(label.Key)
		if err != nil {
			return "", "", err
		}

		var value string
		if label.Str != 0 {
			text, err := d.reader.Text(label.Str)
			if err != nil {
				return "", "", err
			}

			value = strconv.Quote(text)
		} else {
			unit, err := d.reader.Text(label.NumUnit)
			if err != nil {
				return "", "", err
			}

			value = strconv.FormatInt(label.Num, 10) + " " + strconv.Quote(unit)
		}
		labels = append(labels, strconv.Quote(key)+"="+value)
	}
	slices.Sort(labels)
	key, err := json.Marshal(struct {
		Container string   `json:"container"`
		Profile   string   `json:"profile"`
		Labels    []string `json:"labels"`
	}{Container: window.ContainerID, Profile: window.ProfileType, Labels: labels})
	if err != nil {
		return "", "", err
	}

	name := d.sampleType + " (" + d.sampleUnit + ")"
	if window.ProfileType != "" {
		name += " " + window.ProfileType
	}
	if window.ContainerID != "" {
		name += " container=" + window.ContainerID
	}
	if len(labels) > 0 {
		name += " [" + strings.Join(labels, ", ") + "]"
	}
	return string(key), name, nil
}
