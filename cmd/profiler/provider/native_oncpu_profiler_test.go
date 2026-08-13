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

package provider

import (
	"errors"
	"testing"

	"huatuo-bamai/internal/bpf"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/profiling"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestNativeCPUStartRejectsUnsupportedMode(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		PIDs:    []int{123},
		CPUMode: profiling.CPUMode("invalid"),
	}

	err := (&cpuNativeProfiler{}).Start(pctx)
	require.EqualError(t, err, `start native CPU profiler: unsupported mode "invalid"`)
}

func TestNativeOnCPUAttachOptions(t *testing.T) {
	t.Parallel()

	options := nativeOnCPUAttachOptions(
		&pcontext.ProfilerContext{Freq: 199, CPUIDs: []int{1, 3}},
		unix.PERF_TYPE_HARDWARE,
		unix.PERF_COUNT_HW_CPU_CYCLES,
	)
	require.Len(t, options, 1)
	require.Equal(t, "perf_event_sw_cpu_clock", options[0].ProgramName)
	require.Equal(t, uint64(199), options[0].PerfEvent.SampleFreq)
	require.Zero(t, options[0].PerfEvent.SamplePeriod)
	require.Equal(t, []int{1, 3}, options[0].PerfEvent.CPUIDs)
	require.Equal(t, uint32(unix.PERF_TYPE_HARDWARE), options[0].PerfEvent.Type)
	require.Equal(t, uint64(unix.PERF_COUNT_HW_CPU_CYCLES), options[0].PerfEvent.Config)
}

func TestAttachNativeOnCPU(t *testing.T) {
	t.Parallel()

	hardwareErr := errors.New("hardware unavailable")
	softwareErr := errors.New("software unavailable")
	tests := []struct {
		name               string
		requireHardwarePMU bool
		errs               []error
		wantCalls          int
		wantErr            error
		wantErrorMessage   string
	}{
		{
			name:      "hardware PMU available",
			errs:      []error{nil},
			wantCalls: 1,
		},
		{
			name:      "software fallback",
			errs:      []error{hardwareErr, nil},
			wantCalls: 2,
		},
		{
			name:               "hardware PMU required",
			requireHardwarePMU: true,
			errs:               []error{hardwareErr},
			wantCalls:          1,
			wantErr:            hardwareErr,
			wantErrorMessage:   "attach required hardware PMU",
		},
		{
			name:             "both sources unavailable",
			errs:             []error{hardwareErr, softwareErr},
			wantCalls:        2,
			wantErr:          softwareErr,
			wantErrorMessage: "attach software CPU clock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls [][]bpf.AttachOption
			attach := func(opts []bpf.AttachOption) error {
				calls = append(calls, opts)
				return tt.errs[len(calls)-1]
			}
			err := attachNativeOnCPU(attach, &pcontext.ProfilerContext{
				Freq:               99,
				RequireHardwarePMU: tt.requireHardwarePMU,
			})
			require.Len(t, calls, tt.wantCalls)
			require.Equal(
				t,
				uint32(unix.PERF_TYPE_HARDWARE),
				calls[0][0].PerfEvent.Type,
			)
			require.Equal(
				t,
				uint64(unix.PERF_COUNT_HW_CPU_CYCLES),
				calls[0][0].PerfEvent.Config,
			)
			if tt.wantCalls == 2 {
				require.Equal(
					t,
					uint32(unix.PERF_TYPE_SOFTWARE),
					calls[1][0].PerfEvent.Type,
				)
				require.Equal(
					t,
					uint64(unix.PERF_COUNT_SW_CPU_CLOCK),
					calls[1][0].PerfEvent.Config,
				)
			}
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}

			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorContains(t, err, tt.wantErrorMessage)
		})
	}
}
