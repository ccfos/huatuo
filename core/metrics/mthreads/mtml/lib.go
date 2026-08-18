// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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

package mtml

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"huatuo-bamai/core/metrics/mthreads/dl"

	"github.com/ebitengine/purego"
)

// dynamicLibrary abstracts a dynamically loaded shared library. Kept
// purely as a test seam — the package-level library no longer holds one
// after load returns; Shutdown calls purego.Dlclose(handle) directly.
type dynamicLibrary interface {
	Open() error
	Close() error
	Handle() uintptr
}

// library tracks the .so handle and the MtmlLibrary* returned by
// mtmlLibraryInit. Bool-style: only one Init is expected per process
// (at daemon start) and one Shutdown (at daemon exit); the struct is
// mutex-guarded because a future second caller must not race.
type library struct {
	sync.Mutex
	handle   uintptr // dlopen handle; 0 when not loaded
	loadedAs string  // name that successfully loaded; "" when not loaded
	lib      uintptr // MtmlLibrary* from mtmlLibraryInit; 0 when not initialised
}

// global singleton instance.
var libmtml = newLibrary()

func newLibrary() *library {
	return &library{}
}

// defaultMtmlLibraryNames returns the ordered candidate names for loading the
// MTML shared library, matching the discovery order used by the official
// Moore Threads Python binding (pymtml). Bare names (no directory prefix)
// let the system dynamic linker search LD_LIBRARY_PATH, /etc/ld.so.cache,
// and the standard library directories, so installations that expose only
// the versioned SONAME or place the library in a non-/usr/lib path are
// handled automatically.
func defaultMtmlLibraryNames() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{"libmtml.so.2", "libmtml.so"}
	default:
		return nil
	}
}

// loadLocked dlopens libmtml.so and registers all symbols.
//
// Caller must hold l.Mutex. On success, returns the dlopen handle and
// stores loadedAs/l.handle/l.handle. On error, any partially-opened
// candidate has been closed and the library state is unchanged from
// the caller's perspective (still unloaded).
func (l *library) loadLocked() (uintptr, error) {
	if l.loadedAs != "" {
		return l.handle, nil
	}

	candidates := defaultMtmlLibraryNames()
	if len(candidates) == 0 {
		return 0, fmt.Errorf("no MTML library names configured for %s", runtime.GOOS)
	}

	var errs []string
	for _, name := range candidates {
		d := dl.New(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err := d.Open(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}

		if err := registerSymbols(d.Handle()); err != nil {
			_ = d.Close()
			errs = append(errs, fmt.Sprintf("%s: registerSymbols: %v", name, err))
			continue
		}

		l.loadedAs = name
		l.handle = d.Handle()
		return l.handle, nil
	}

	return 0, fmt.Errorf("failed to load MTML library (tried %s):\n  %s",
		strings.Join(candidates, ", "),
		strings.Join(errs, "\n  "))
}

// closeLocked releases dlopen/.so state held by this library. Caller
// must hold l.Mutex.
func (l *library) closeLocked() {
	if l.loadedAs == "" {
		return
	}

	if err := purego.Dlclose(l.handle); err != nil {
		fmt.Printf("mthreads: dlclose failed: %v\n", err)
	}
	l.handle = 0
	l.loadedAs = ""
}

// registerSymbols registers MTML library symbols using purego.Dlsym + RegisterFunc.
// Returns error if any required symbol is missing.
// Optional symbols that are missing are left as nil (callers return NotSupported).
func registerSymbols(handle uintptr) error {
	// Required symbols - library cannot function without these.
	sym, err := purego.Dlsym(handle, "mtmlLibraryInit")
	if err != nil {
		return fmt.Errorf("required symbol mtmlLibraryInit not found: %w", err)
	}
	purego.RegisterFunc(&mtmlLibraryInit, sym)

	sym, err = purego.Dlsym(handle, "mtmlLibraryShutDown")
	if err != nil {
		return fmt.Errorf("required symbol mtmlLibraryShutDown not found: %w", err)
	}
	purego.RegisterFunc(&mtmlLibraryShutDown, sym)

	sym, err = purego.Dlsym(handle, "mtmlLibraryCountDevice")
	if err != nil {
		return fmt.Errorf("required symbol mtmlLibraryCountDevice not found: %w", err)
	}
	purego.RegisterFunc(&mtmlLibraryCountDevice, sym)

	sym, err = purego.Dlsym(handle, "mtmlLibraryInitDeviceByIndex")
	if err != nil {
		return fmt.Errorf("required symbol mtmlLibraryInitDeviceByIndex not found: %w", err)
	}
	purego.RegisterFunc(&mtmlLibraryInitDeviceByIndex, sym)

	sym, err = purego.Dlsym(handle, "mtmlLibraryFreeDevice")
	if err != nil {
		return fmt.Errorf("required symbol mtmlLibraryFreeDevice not found: %w", err)
	}
	purego.RegisterFunc(&mtmlLibraryFreeDevice, sym)

	// Optional symbols - missing symbols are left as nil, callers return NotSupported.
	if sym, err := purego.Dlsym(handle, "mtmlLibraryGetVersion"); err == nil {
		purego.RegisterFunc(&mtmlLibraryGetVersion, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlLibraryInitSystem"); err == nil {
		purego.RegisterFunc(&mtmlLibraryInitSystem, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlLibraryFreeSystem"); err == nil {
		purego.RegisterFunc(&mtmlLibraryFreeSystem, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlSystemGetDriverVersion"); err == nil {
		purego.RegisterFunc(&mtmlSystemGetDriverVersion, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetIndex"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetIndex, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetName"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetName, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetUUID"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetUUID, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetBrand"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetBrand, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetSerialNumber"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetSerialNumber, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetMtBiosVersion"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetMtBiosVersion, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceCountGpuCores"); err == nil {
		purego.RegisterFunc(&mtmlDeviceCountGpuCores, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetPciInfo"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetPciInfo, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetPowerUsage"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetPowerUsage, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetPcieReplayCounter"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetPcieReplayCounter, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetPerformanceState"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetPerformanceState, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetMusaComputeCapability"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetMusaComputeCapability, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceInitGpu"); err == nil {
		purego.RegisterFunc(&mtmlDeviceInitGpu, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceFreeGpu"); err == nil {
		purego.RegisterFunc(&mtmlDeviceFreeGpu, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetTemperature"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetTemperature, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetUtilization"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetUtilization, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetClock"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetClock, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetMaxClock"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetMaxClock, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetVoltage"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetVoltage, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetEnforcedPowerLimit"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetEnforcedPowerLimit, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetPowerManagementDefaultLimit"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetPowerManagementDefaultLimit, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlGpuGetTemperatureThreshold"); err == nil {
		purego.RegisterFunc(&mtmlGpuGetTemperatureThreshold, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceInitMemory"); err == nil {
		purego.RegisterFunc(&mtmlDeviceInitMemory, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceFreeMemory"); err == nil {
		purego.RegisterFunc(&mtmlDeviceFreeMemory, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetTotal"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetTotal, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetUsed"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetUsed, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetUtilization"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetUtilization, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetTemperature"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetTemperature, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetClock"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetClock, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetMaxClock"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetMaxClock, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetBusWidth"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetBusWidth, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetBandwidth"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetBandwidth, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetSpeed"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetSpeed, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetType"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetType, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlMemoryGetVendor"); err == nil {
		purego.RegisterFunc(&mtmlMemoryGetVendor, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceInitVpu"); err == nil {
		purego.RegisterFunc(&mtmlDeviceInitVpu, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceFreeVpu"); err == nil {
		purego.RegisterFunc(&mtmlDeviceFreeVpu, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlVpuGetUtilization"); err == nil {
		purego.RegisterFunc(&mtmlVpuGetUtilization, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlVpuGetClock"); err == nil {
		purego.RegisterFunc(&mtmlVpuGetClock, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlVpuGetMaxClock"); err == nil {
		purego.RegisterFunc(&mtmlVpuGetMaxClock, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceCountFan"); err == nil {
		purego.RegisterFunc(&mtmlDeviceCountFan, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetFanSpeed"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetFanSpeed, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetFanRpm"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetFanRpm, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetMtLinkSpec"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetMtLinkSpec, sym)
	}
	if sym, err := purego.Dlsym(handle, "mtmlDeviceGetMtLinkState"); err == nil {
		purego.RegisterFunc(&mtmlDeviceGetMtLinkState, sym)
	}

	// for each constructor/destructor pair, if the constructor resolved
	// but the destructor did not, force the constructor back to nil.
	enforceInitFreePair(&mtmlDeviceInitGpu, mtmlDeviceFreeGpu)
	enforceInitFreePair(&mtmlDeviceInitMemory, mtmlDeviceFreeMemory)
	enforceInitFreePair(&mtmlDeviceInitVpu, mtmlDeviceFreeVpu)
	enforceInitFreePair(&mtmlLibraryInitSystem, mtmlLibraryFreeSystem)

	return nil
}

func enforceInitFreePair(initPtr *func(uintptr, *uintptr) Return, freePtr func(uintptr) Return) {
	if *initPtr != nil && freePtr == nil {
		*initPtr = nil
	}
}

// Lib returns the MtmlLibrary* handle for the initialized library.
func (l *library) Lib() uintptr { return l.lib }
