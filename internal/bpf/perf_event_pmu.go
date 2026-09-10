// Copyright 2025, 2026 The HuaTuo Authors
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

package bpf

import (
	"errors"
	"fmt"
	"runtime"

	"huatuo-bamai/internal/utils/cpuutil"

	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
)

var errInvalidPerfEventOption = errors.New("invalid perf event option")

// onlineCPUIDs reads the sysfs online CPU list; overridable in tests.
var onlineCPUIDs = func() ([]int, error) {
	return cpuutil.OnlineCPUIDs(cpuutil.SystemCPUOnlinePath)
}

type perfEventSampleMode uint8

const (
	perfEventSampleFrequency perfEventSampleMode = iota
	perfEventSamplePeriod
)

type perfEventAttach struct {
	fds []int
}

type perfEventOption struct {
	sample      uint64
	sampleMode  perfEventSampleMode
	program     *ebpf.Program
	cpuIDs      []int
	eventType   uint32
	eventConfig uint64
}

func (opt *perfEventOption) Validate() error {
	if opt == nil {
		return fmt.Errorf("%w: option required", errInvalidPerfEventOption)
	}

	var errs []error

	if opt.program == nil {
		errs = append(errs, fmt.Errorf(
			"%w: program required", errInvalidPerfEventOption,
		))
	}

	if opt.sample == 0 {
		errs = append(errs, fmt.Errorf(
			"%w: sample value required", errInvalidPerfEventOption,
		))
	}

	if len(errs) == 0 {
		return nil
	}

	return errors.Join(errs...)
}

func openPerfEvent(attr *unix.PerfEventAttr, progFD, cpuID int) (int, error) {
	fd, err := unix.PerfEventOpen(attr, -1, cpuID, -1, unix.PERF_FLAG_FD_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("open perf event on cpu %d: %w", cpuID, err)
	}

	if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_SET_BPF, progFD); err != nil {
		return -1, errors.Join(
			fmt.Errorf("set bpf program on perf event for cpu %d: %w", cpuID, err),
			closePerfEventFDs([]int{fd}),
		)
	}

	if err := unix.IoctlSetInt(fd, unix.PERF_EVENT_IOC_ENABLE, 0); err != nil {
		return -1, errors.Join(
			fmt.Errorf("enable perf event on cpu %d: %w", cpuID, err),
			closePerfEventFDs([]int{fd}),
		)
	}

	return fd, nil
}

func attachPerfEvent(opt *perfEventOption) (*perfEventAttach, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}

	attr := newPerfEventAttr(opt)
	cpuIDs := opt.cpuIDs
	if len(cpuIDs) == 0 {
		cpuIDs = defaultPerfCPUIDs()
	}

	fds := make([]int, 0, len(cpuIDs))
	for _, cpuID := range cpuIDs {
		fd, err := openPerfEvent(&attr, opt.program.FD(), cpuID)
		if err != nil {
			return nil, errors.Join(err, closePerfEventFDs(fds))
		}
		fds = append(fds, fd)
	}

	return &perfEventAttach{fds: fds}, nil
}

// defaultPerfCPUIDs returns the CPU list used when the caller does not pin
// the perf event to specific CPUs: one perf event per online CPU, falling
// back to the usable CPU count when the sysfs online list cannot be read.
// An online CPU count is not an upper CPU ID when hotplug leaves holes.
func defaultPerfCPUIDs() []int {
	ids, err := onlineCPUIDs()
	if err != nil || len(ids) == 0 {
		ids = make([]int, runtime.NumCPU())
		for cpuID := range ids {
			ids[cpuID] = cpuID
		}
	}
	return ids
}

func newPerfEventAttr(opt *perfEventOption) unix.PerfEventAttr {
	attr := unix.PerfEventAttr{
		Type:   opt.eventType,
		Size:   unix.PERF_ATTR_SIZE_VER0,
		Config: opt.eventConfig,
		Bits:   unix.PerfBitFreq | unix.PerfBitDisabled,
		Sample: opt.sample,
	}

	if opt.sampleMode == perfEventSamplePeriod {
		// Clear only frequency mode and preserve any other perf event flags.
		attr.Bits &^= unix.PerfBitFreq
	}
	return attr
}

func (p *perfEventAttach) detach() error {
	fds := p.fds
	p.fds = nil
	return closePerfEventFDs(fds)
}

func closePerfEventFDs(fds []int) error {
	var errs []error
	for _, fd := range fds {
		if err := unix.Close(fd); err != nil {
			errs = append(errs, fmt.Errorf("close perf fd %d: %w", fd, err))
		}
	}
	return errors.Join(errs...)
}
