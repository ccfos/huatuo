// Copyright 2025 The HuaTuo Authors
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

// A simple link type implemented by referring to the Cilium community.
// link/perf_event.go

//go:build !didi

package bpf

import (
	"fmt"
	"runtime"

	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
)

var perfEventPmuSysbmol = "perf_event_pmu_sysbmol"

const (
	sampleTypePeriod = 1
	sampleTypeFreq   = 2
)

type perfEventPMU struct {
	fds []int
}

type pmuOption struct {
	samplePeriodFreq uint64
	sampleType       uint32
	program          *ebpf.Program
}

func (opt *pmuOption) Validate() error {
	if opt == nil {
		return fmt.Errorf("options required")
	}

	var errs []error

	if opt.program == nil {
		errs = append(errs, fmt.Errorf("program required"))
	}

	if opt.samplePeriodFreq == 0 {
		errs = append(errs, fmt.Errorf("samplePeriodFreq required"))
	}

	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf("invalid options: %v", errs)
}

func perfEventOpenWithBPF(attr *unix.PerfEventAttr, progFD, cpuId int) (int, error) {
	fd, err := unix.PerfEventOpen(attr, -1, cpuId, -1, unix.PERF_FLAG_FD_CLOEXEC)
	if err != nil {
		return -1, err
	}

	if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_SET_BPF, progFD); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}

	if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_ENABLE, 0); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}

	return fd, nil
}

func attachPerfEventPMU(opt *pmuOption) (*perfEventPMU, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}

	attr := unix.PerfEventAttr{
		Type:   unix.PERF_TYPE_SOFTWARE,
		Size:   unix.PERF_ATTR_SIZE_VER0,
		Config: unix.PERF_COUNT_SW_CPU_CLOCK,
		Bits:   unix.PerfBitFreq,
		Sample: opt.samplePeriodFreq,
	}

	if opt.sampleType == sampleTypePeriod {
		attr.Bits = 0
	}

	var fds []int
	for i := 0; i < runtime.NumCPU(); i++ {
		fd, err := perfEventOpenWithBPF(&attr, opt.program.FD(), i)
		if err != nil {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
			return nil, err
		}

		fds = append(fds, fd)
	}

	return &perfEventPMU{fds: fds}, nil
}

func (p *perfEventPMU) detach() error {
	for _, fd := range p.fds {
		_ = unix.Close(fd)
	}
	return nil
}
