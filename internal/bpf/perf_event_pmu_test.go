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
	"errors"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestNewPerfEventAttrFrequency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantType   uint32
		wantConfig uint64
	}{
		{
			name:       "hardware CPU cycles",
			wantType:   unix.PERF_TYPE_HARDWARE,
			wantConfig: unix.PERF_COUNT_HW_CPU_CYCLES,
		},
		{
			name:       "software CPU clock",
			wantType:   unix.PERF_TYPE_SOFTWARE,
			wantConfig: unix.PERF_COUNT_SW_CPU_CLOCK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			attr := newPerfEventAttr(&perfEventOption{
				sample:      99,
				eventType:   tt.wantType,
				eventConfig: tt.wantConfig,
			})
			require.Equal(t, tt.wantType, attr.Type)
			require.Equal(t, tt.wantConfig, attr.Config)
			require.Equal(t, uint64(99), attr.Sample)
			require.NotZero(t, attr.Bits&unix.PerfBitFreq)
		})
	}
}

// TestAttachPerfEvent tests perf event attach using table-driven style.
func TestAttachPerfEvent(t *testing.T) {
	prog := newTestProgram(t)

	cases := []struct {
		name   string
		opt    *perfEventOption
		wantOK bool
	}{
		{
			name: "ok freq sampling",
			opt: &perfEventOption{
				sample:  1,
				program: prog,
			},
			wantOK: true,
		},
		{
			name: "ok period sampling",
			opt: &perfEventOption{
				sample:     1_000_000,
				sampleMode: perfEventSamplePeriod,
				program:    prog,
			},
			wantOK: true,
		},
		{
			name:   "nil option",
			opt:    nil,
			wantOK: false,
		},
		{
			name: "nil program",
			opt: &perfEventOption{
				sample: 1,
			},
			wantOK: false,
		},
		{
			name: "closed program",
			opt: &perfEventOption{
				sample: 1,
				program: func() *ebpf.Program {
					p := newTestProgram(t)
					p.Close()
					return p
				}(),
			},
			wantOK: false,
		},
		{
			name: "zero sample freq",
			opt: &perfEventOption{
				program: prog,
			},
			wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pmu, err := attachPerfEvent(c.opt)
			if c.wantOK {
				skipPerfEventIfNotAvailable(t, err)
				require.NoError(t, err)
				require.NotNil(t, pmu)
				require.NotEmpty(t, pmu.fds)
				t.Cleanup(func() { require.NoError(t, pmu.detach()) })
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestAttachPerfEvent_AttachTwice verifies that attaching the same program twice is safe.
func TestAttachPerfEvent_AttachTwice(t *testing.T) {
	prog := newTestProgram(t)

	opt := &perfEventOption{
		sample:  1,
		program: prog,
	}

	pmu1, err := attachPerfEvent(opt)
	skipPerfEventIfNotAvailable(t, err)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pmu1.detach()) })

	pmu2, err := attachPerfEvent(opt)
	skipPerfEventIfNotAvailable(t, err)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pmu2.detach()) })
}

// TestOpenPerfEvent tests openPerfEvent syscall helper using table-driven style.
func TestOpenPerfEvent(t *testing.T) {
	prog := newTestProgram(t)

	cases := []struct {
		name   string
		attr   *unix.PerfEventAttr
		progFD int
		wantOK bool
	}{
		{
			name: "PerfEventOpen fails",
			attr: &unix.PerfEventAttr{
				Type:   unix.PERF_TYPE_SOFTWARE,
				Config: 999999,
			},
			progFD: prog.FD(),
			wantOK: false,
		},
		{
			name: "SET_BPF fails with bad fd",
			attr: &unix.PerfEventAttr{
				Type:   unix.PERF_TYPE_SOFTWARE,
				Size:   unix.PERF_ATTR_SIZE_VER0,
				Config: unix.PERF_COUNT_SW_CPU_CLOCK,
				Bits:   unix.PerfBitFreq,
				Sample: 1,
			},
			progFD: -1,
			wantOK: false,
		},
		{
			name: "ok attach",
			attr: &unix.PerfEventAttr{
				Type:   unix.PERF_TYPE_SOFTWARE,
				Size:   unix.PERF_ATTR_SIZE_VER0,
				Config: unix.PERF_COUNT_SW_CPU_CLOCK,
				Bits:   unix.PerfBitFreq,
				Sample: 1,
			},
			progFD: prog.FD(),
			wantOK: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd, err := openPerfEvent(c.attr, c.progFD, 0)
			if c.wantOK {
				skipPerfEventIfNotAvailable(t, err)
				require.NoError(t, err)
				require.GreaterOrEqual(t, fd, 0)
				t.Cleanup(func() { require.NoError(t, unix.Close(fd)) })
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestPerfEventAttach_Detach tests detach() is safe with empty or invalid fds.
func TestPerfEventAttach_Detach(t *testing.T) {
	cases := []struct {
		name string
		pmu  *perfEventAttach
	}{
		{"nil fds", &perfEventAttach{fds: nil}},
		{"invalid fds", &perfEventAttach{fds: []int{-1, -2}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.pmu.fds != nil {
				require.Error(t, c.pmu.detach())
			} else {
				require.NoError(t, c.pmu.detach())
			}
		})
	}
}

// TestPerfEventAttach_DetachTwice verifies that calling detach() twice is safe.
func TestPerfEventAttach_DetachTwice(t *testing.T) {
	pmu := &perfEventAttach{fds: []int{-1, -2}}
	require.Error(t, pmu.detach())
	require.NoError(t, pmu.detach())
}

// TestPerfEventAttach_DetachValidFDs verifies that detach() correctly closes valid fds.
func TestPerfEventAttach_DetachValidFDs(t *testing.T) {
	prog := newTestProgram(t)

	opt := &perfEventOption{
		sample:  1,
		program: prog,
	}

	pmu, err := attachPerfEvent(opt)
	skipPerfEventIfNotAvailable(t, err)
	require.NoError(t, err)
	require.NotEmpty(t, pmu.fds)

	require.NoError(t, pmu.detach())
}

// newTestProgram returns a minimal PERF_EVENT program.
func newTestProgram(t *testing.T) *ebpf.Program {
	t.Helper()
	requireBPFPermission(t)

	prog, err := ebpf.NewProgram(&ebpf.ProgramSpec{
		Type: ebpf.PerfEvent,
		Instructions: asm.Instructions{
			asm.Mov.Imm(asm.R0, 0),
			asm.Return(),
		},
		License: "GPL",
	})

	if errors.Is(err, ebpf.ErrNotSupported) {
		t.Skipf("skipping: ebpf not supported: %v", err)
	}

	require.NoError(t, err)
	t.Cleanup(func() { prog.Close() })
	return prog
}

// skipPerfEventIfNotAvailable skips tests if perf is unavailable due to kernel restrictions or permissions.
func skipPerfEventIfNotAvailable(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOENT) {
		t.Skipf("skipping: perf event unavailable in this environment: %v", err)
	}
}
