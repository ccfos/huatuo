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

// Package speedscope writes Speedscope JSON profile format (https://www.speedscope.app/).
package speedscope

import (
	"encoding/json"
	"io"

	"huatuo-bamai/internal/profiler/output"
)

type speedscopeFile struct {
	Schema           string           `json:"$schema"`
	Shared           sharedData       `json:"shared"`
	Profiles         []sampledProfile `json:"profiles"`
	ActiveProfileIdx int              `json:"activeProfileIndex"`
	Exporter         string           `json:"exporter"`
	Name             string           `json:"name"`
}

type sharedData struct {
	Frames []ssFrame `json:"frames"`
}

type ssFrame struct {
	Name string `json:"name"`
	File string `json:"file,omitempty"`
	Line int32  `json:"line,omitempty"`
}

type sampledProfile struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Unit       string    `json:"unit"`
	StartValue float64   `json:"startValue"`
	EndValue   float64   `json:"endValue"`
	Samples    [][]int   `json:"samples"`
	Weights    []float64 `json:"weights"`
}

type threadState struct {
	name    string
	samples [][]int
	total   int64 // total sample count
}

// Formatter accumulates samples and writes Speedscope JSON.
type Formatter struct {
	sampleDuration float64 // seconds per sample (default 0.01 = 100 Hz)
	frames         map[ssFrame]int
	frameList      []ssFrame
	threads        map[string]*threadState
	threadOrder    []string // insertion order
}

var _ output.Formatter = (*Formatter)(nil)

// New creates a Formatter. Pass 0 for sampleRateHz to default to 100 Hz.
func New(sampleRateHz float64) *Formatter {
	if sampleRateHz <= 0 {
		sampleRateHz = 100
	}
	return &Formatter{
		sampleDuration: 1.0 / sampleRateHz,
		frames:         make(map[ssFrame]int),
		threads:        make(map[string]*threadState),
	}
}

func (f *Formatter) Name() string { return "speedscope" }

// Add incorporates one sample. Batch samples (Count > 1) are expanded inline.
func (f *Formatter) Add(s *output.Sample) error {
	if len(s.Frames) == 0 {
		return nil
	}

	key := threadKey(s)
	ts := f.getOrAddThread(key, threadName(s))

	indices := make([]int, 0, len(s.Frames))

	for i, name := range s.Frames {
		var detail *output.Frame
		if i < len(s.FrameDetails) {
			detail = &s.FrameDetails[i]
		}

		indices = append(indices, f.getOrAddFrame(name, detail))
	}

	count := s.Count
	if count <= 0 {
		count = 1
	}
	for i := int64(0); i < count; i++ {
		ts.samples = append(ts.samples, indices)
		ts.total++
	}
	return nil
}

func (f *Formatter) Write(w io.Writer) error {
	profiles := make([]sampledProfile, 0, len(f.threadOrder))
	for _, key := range f.threadOrder {
		ts := f.threads[key]
		weights := make([]float64, len(ts.samples))
		for i := range weights {
			weights[i] = f.sampleDuration
		}
		profiles = append(profiles, sampledProfile{
			Type:       "sampled",
			Name:       ts.name,
			Unit:       "seconds",
			StartValue: 0,
			EndValue:   float64(ts.total) * f.sampleDuration,
			Samples:    ts.samples,
			Weights:    weights,
		})
	}

	activeIdx := 0
	var maxSamples int
	for i := range profiles {
		if len(profiles[i].Samples) > maxSamples {
			maxSamples = len(profiles[i].Samples)
			activeIdx = i
		}
	}

	file := speedscopeFile{
		Schema:           "https://www.speedscope.app/file-format-schema.json",
		Shared:           sharedData{Frames: f.frameList},
		Profiles:         profiles,
		ActiveProfileIdx: activeIdx,
		Exporter:         "huatuo-profiler",
		Name:             "profile",
	}
	return json.NewEncoder(w).Encode(file)
}

func (f *Formatter) Reset() {
	f.frames = make(map[ssFrame]int)
	f.frameList = nil
	f.threads = make(map[string]*threadState)
	f.threadOrder = nil
}

func (f *Formatter) IsEmpty() bool {
	return len(f.threads) == 0
}

// getOrAddFrame returns the global index for a frame, adding it if unseen.
// detail is optional; when provided, file and line are included in dedup and output.
func (f *Formatter) getOrAddFrame(name string, detail *output.Frame) int {
	sf := ssFrame{Name: name}
	if detail != nil {
		sf.File = detail.File
		sf.Line = detail.Line
	}

	if idx, ok := f.frames[sf]; ok {
		return idx
	}

	idx := len(f.frameList)
	f.frameList = append(f.frameList, sf)
	f.frames[sf] = idx

	return idx
}

func (f *Formatter) getOrAddThread(key, name string) *threadState {
	if ts, ok := f.threads[key]; ok {
		return ts
	}
	ts := &threadState{name: name}
	f.threads[key] = ts
	f.threadOrder = append(f.threadOrder, key)
	return ts
}

func threadKey(s *output.Sample) string {
	if s.ThreadID != "" {
		return s.ThreadID
	}
	if s.PID != 0 {
		return "main"
	}
	return "default"
}

func threadName(s *output.Sample) string {
	if s.ThreadName != "" {
		return s.ThreadName
	}
	if s.ThreadID != "" {
		return s.ThreadID
	}
	return "main thread"
}
