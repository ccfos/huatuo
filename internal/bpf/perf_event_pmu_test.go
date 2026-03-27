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

//go:build !didi

package bpf

import (
	"errors"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestAttachPerfEventPMU tests perf event attach using table-driven style.
func TestAttachPerfEventPMU(t *testing.T) {
	prog := newTestProgram(t)

	cases := []struct {
		name   string
		opt    *perfEventPMUOption
		wantOK bool
	}{
		{
			name: "ok freq sampling",
			opt: &perfEventPMUOption{
				sampleType:       sampleTypeFreq,
				samplePeriodFreq: 1,
				program:          prog,
			},
			wantOK: true,
		},
		{
			// sampleType 0 is undefined; current implementation falls through
			// to freq because only sampleTypePeriod clears PerfBitFreq.
			name: "undefined sample type defaults to freq behavior",
			opt: &perfEventPMUOption{
				sampleType:       0,
				samplePeriodFreq: 1,
				program:          prog,
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
			opt: &perfEventPMUOption{
				sampleType:       sampleTypeFreq,
				samplePeriodFreq: 1,
				program:          nil,
			},
			wantOK: false,
		},
		{
			name: "closed program",
			opt: &perfEventPMUOption{
				sampleType:       sampleTypeFreq,
				samplePeriodFreq: 1,
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
			opt: &perfEventPMUOption{
				sampleType:       sampleTypeFreq,
				samplePeriodFreq: 0,
				program:          prog,
			},
			wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pmu, err := attachPerfEventPMU(c.opt)
			if c.wantOK {
				skipPerfEventPMUIfNotAvailable(t, err)
				require.NoError(t, err)
				require.NotNil(t, pmu)
				require.NotEmpty(t, pmu.fds)
				t.Cleanup(func() { _ = pmu.detach() })
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestAttachPerfEventPMU_AttachTwice verifies that attaching the same program twice is safe.
func TestAttachPerfEventPMU_AttachTwice(t *testing.T) {
	prog := newTestProgram(t)

	opt := &perfEventPMUOption{
		sampleType:       sampleTypeFreq,
		samplePeriodFreq: 1,
		program:          prog,
	}

	pmu1, err := attachPerfEventPMU(opt)
	skipPerfEventPMUIfNotAvailable(t, err)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pmu1.detach() })

	pmu2, err := attachPerfEventPMU(opt)
	skipPerfEventPMUIfNotAvailable(t, err)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pmu2.detach() })
}

// TestAttach_DirectSyscall tests attach() syscall helper using table-driven style.
func TestAttach_DirectSyscall(t *testing.T) {
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
			fd, err := attach(c.attr, c.progFD, 0)
			if c.wantOK {
				skipPerfEventPMUIfNotAvailable(t, err)
				require.NoError(t, err)
				require.GreaterOrEqual(t, fd, 0)
				t.Cleanup(func() { _ = unix.Close(fd) })
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestPerfEventPMU_Detach tests detach() is safe with empty or invalid fds.
func TestPerfEventPMU_Detach(t *testing.T) {
	cases := []struct {
		name string
		pmu  *perfEventPMU
	}{
		{"nil fds", &perfEventPMU{fds: nil}},
		{"invalid fds", &perfEventPMU{fds: []int{-1, -2}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, c.pmu.detach())
		})
	}
}

// TestPerfEventPMU_DetachTwice verifies that calling detach() twice is safe.
func TestPerfEventPMU_DetachTwice(t *testing.T) {
	pmu := &perfEventPMU{fds: []int{-1, -2}}
	require.NoError(t, pmu.detach())
	require.NoError(t, pmu.detach())
}

// TestPerfEventPMU_DetachValidFDs verifies that detach() correctly closes valid fds.
func TestPerfEventPMU_DetachValidFDs(t *testing.T) {
	prog := newTestProgram(t)

	opt := &perfEventPMUOption{
		sampleType:       sampleTypeFreq,
		samplePeriodFreq: 1,
		program:          prog,
	}

	pmu, err := attachPerfEventPMU(opt)
	skipPerfEventPMUIfNotAvailable(t, err)
	require.NoError(t, err)
	require.NotEmpty(t, pmu.fds)

	require.NoError(t, pmu.detach())
}

// newTestProgram returns a minimal PERF_EVENT program.
func newTestProgram(t *testing.T) *ebpf.Program {
	t.Helper()

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

// skipPerfEventPMUIfNotAvailable skips tests if perf is unavailable due to kernel restrictions or permissions.
func skipPerfEventPMUIfNotAvailable(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EINVAL) {
		t.Skipf("skipping: perf event unavailable in this environment: %v", err)
	}
}
