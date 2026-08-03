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

package bpf

import (
	"testing"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
)

func TestPerfEventOptionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opt         *perfEventOption
		wantDetails []string
	}{
		{
			name:        "nil option",
			wantDetails: []string{"option required"},
		},
		{
			name:        "missing program",
			opt:         &perfEventOption{sample: 1},
			wantDetails: []string{"program required"},
		},
		{
			name:        "missing sample value",
			opt:         &perfEventOption{program: new(ebpf.Program)},
			wantDetails: []string{"sample value required"},
		},
		{
			name: "missing program and sample value",
			opt:  new(perfEventOption),
			wantDetails: []string{
				"program required",
				"sample value required",
			},
		},
		{
			name: "valid",
			opt: &perfEventOption{
				program: new(ebpf.Program),
				sample:  1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.opt.Validate()
			if len(tt.wantDetails) == 0 {
				require.NoError(t, err)
				return
			}

			require.ErrorIs(t, err, errInvalidPerfEventOption)
			for _, detail := range tt.wantDetails {
				require.ErrorContains(t, err, detail)
			}
		})
	}
}

func TestParsePerfEventAttachOptions(t *testing.T) {
	t.Parallel()

	program := &loadedProgram{handle: new(ebpf.Program)}
	tests := []struct {
		name         string
		samplePeriod uint64
		sampleFreq   uint64
		wantSample   uint64
		wantMode     perfEventSampleMode
		wantErr      bool
	}{
		{
			name:       "frequency",
			sampleFreq: 99,
			wantSample: 99,
			wantMode:   perfEventSampleFrequency,
		},
		{
			name:         "period",
			samplePeriod: 1000,
			wantSample:   1000,
			wantMode:     perfEventSamplePeriod,
		},
		{
			name:         "conflicting modes",
			samplePeriod: 1000,
			sampleFreq:   99,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opt, err := parsePerfEventAttachOptions(
				program,
				tt.samplePeriod,
				tt.sampleFreq,
				nil,
			)
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidPerfEventOption)
				require.ErrorContains(t, err, "mutually exclusive")
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantSample, opt.sample)
			require.Equal(t, tt.wantMode, opt.sampleMode)
		})
	}
}
