// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package dcmi

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ccfos/huatuo/core/metrics/ascend/dl"

	"github.com/ebitengine/purego"
)

// dynamicLibrary abstracts a dynamically loaded shared library.
// It is responsible only for managing the dlopen/dlclose lifecycle.
type dynamicLibrary interface {
	Open() error
	Close() error
	Handle() uintptr
}

// library represents the DCMI shared library.
// It coordinates reference counting and symbol registration,
// while delegating loading/unloading to dynamicLibrary.
type library struct {
	sync.Mutex
	refcount refcount
	dl       dynamicLibrary
}

// global singleton instance
var libdcmi = newLibrary()

func newLibrary() *library {
	path := defaultDcmiLibraryPath()
	return &library{
		dl: dl.New(path, purego.RTLD_NOW|purego.RTLD_GLOBAL),
	}
}

func defaultDcmiLibraryPath() string {
	switch runtime.GOOS {
	case "linux":
		return "/usr/local/dcmi/libdcmi.so"
	default:
		return ""
	}
}

// load initializes the shared library and registers all required symbols.
// Multiple calls are reference-counted and idempotent.
func (l *library) load() (rerr error) {
	l.Lock()
	defer l.Unlock()
	defer func() { l.refcount.incOnNoError(rerr) }()

	if l.refcount > 0 {
		return nil
	}

	if err := l.dl.Open(); err != nil {
		return err
	}

	// Register all symbols after successful loading. A missing symbol means
	// the loaded library's exported symbol set changed (e.g. after a
	// CANN/driver upgrade); return an error so the collector is disabled
	// instead of panicking, and close the handle so a failed registration
	// does not leak the dlopen reference.
	if err := l.registerDcmiLibSymbols(l.dl.Handle()); err != nil {
		_ = l.dl.Close()
		return err
	}

	return nil
}

// close decrements the reference count and unloads the library
// when the last reference is released.
func (l *library) close() (rerr error) {
	l.Lock()
	defer l.Unlock()
	defer func() { l.refcount.decOnNoError(rerr) }()

	if l.refcount != 1 {
		return nil
	}

	return l.dl.Close()
}

// registerDcmiLibSymbols resolves and registers all required DCMI symbols
// from the loaded shared library. It returns an error if any required symbol
// is missing, so a library whose exported symbol set changed (e.g. after a
// CANN/driver upgrade) disables the collector instead of panicking.
func (l *library) registerDcmiLibSymbols(handle uintptr) error {
	symbols := []struct {
		name string
		dst  any
	}{
		{"dcmi_init", &dcmiInit},
		{"dcmi_get_device_health", &dcGetDeviceHealth},
		{"dcmi_get_card_list", &dcGetCardList},
		{"dcmi_get_device_num_in_card", &dcGetDeviceNumInCard},
		{"dcmi_get_device_power_info", &dcGetDevicePowerInfo},
		{"dcmi_get_device_temperature", &dcGetDeviceTemperature},
		{"dcmi_get_device_voltage", &dcGetDeviceVoltage},
		{"dcmi_get_device_utilization_rate", &dcGetDeviceUtilizationRate},
		{"dcmi_get_device_frequency", &dcGetDeviceFrequency},
		{"dcmi_get_device_network_health", &dcGetDeviceNetWorkHealth},
		{"dcmi_get_device_hbm_info", &dcGetDeviceHbmInfo},
		{"dcmi_get_device_ecc_info", &dcGetDeviceEccInfo},
		{"dcmi_get_device_pcie_info_v2", &dcGetDevicePcieInfoV2},
		{"dcmi_get_device_logic_id", &dcGetDeviceLogicID},
		{"dcmi_get_device_phyid_from_logicid", &dcGetPhysicIDFromLogicID},
	}
	for _, s := range symbols {
		sym, err := purego.Dlsym(handle, s.name)
		if err != nil {
			return fmt.Errorf("dcmi: required symbol %s not found: %w", s.name, err)
		}
		purego.RegisterFunc(s.dst, sym)
	}
	return nil
}
