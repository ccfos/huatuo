// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The MetaX Authors
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

package sml

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ccfos/huatuo/core/metrics/metax/dl"

	"github.com/ebitengine/purego"
)

// dynamicLibrary abstracts a dynamically loaded shared library.
// It is responsible only for managing the dlopen/dlclose lifecycle.
type dynamicLibrary interface {
	Open() error
	Close() error
	Handle() uintptr
}

// library represents the SML shared library.
// It coordinates reference counting and symbol registration,
// while delegating loading/unloading to dynamicLibrary.
type library struct {
	sync.Mutex
	refcount refcount
	dl       dynamicLibrary
}

// global singleton instance
var libsml = newLibrary()

func newLibrary() *library {
	path := defaultSmlLibraryPath()
	return &library{
		dl: dl.New(path, purego.RTLD_NOW|purego.RTLD_GLOBAL),
	}
}

func defaultSmlLibraryPath() string {
	switch runtime.GOOS {
	case "linux":
		return "/opt/mxdriver/lib/libmxsml.so"
	default:
		return ""
	}
}

// load initializes the shared library and registers all required symbols.
// Multiple calls are reference-counted and idempotent.
func (l *library) load() (rerr error) {
	l.Lock()
	defer l.Unlock()
	defer func() { l.refcount.IncOnNoError(rerr) }()

	if l.refcount > 0 {
		return nil
	}

	if err := l.dl.Open(); err != nil {
		return err
	}

	// Register all symbols after successful loading.
	if err := registerSmlLibSymbols(l.dl.Handle()); err != nil {
		if closeErr := l.dl.Close(); closeErr != nil {
			return fmt.Errorf("register SML symbols: %w; close library: %w", err, closeErr)
		}
		return fmt.Errorf("register SML symbols: %w", err)
	}

	return nil
}

// close decrements the reference count and unloads the library
// when the last reference is released.
func (l *library) close() (rerr error) {
	l.Lock()
	defer l.Unlock()
	defer func() { l.refcount.DecOnNoError(rerr) }()

	if l.refcount != 1 {
		return nil
	}

	return l.dl.Close()
}

// registerSmlLibSymbols registers all required SML symbols from the loaded
// shared library. Missing symbols are returned to the caller so an unsupported
// SML version disables only the MetaX collector instead of panicking the agent.
func registerSmlLibSymbols(handle uintptr) error {
	registrations := []struct {
		function any
		name     string
	}{
		{&mxSmlInit, "mxSmlInit"},
		{&mxSmlGetErrorString, "mxSmlGetErrorString"},
		{&mxSmlGetMacaVersion, "mxSmlGetMacaVersion"},
		{&mxSmlGetDeviceCount, "mxSmlGetDeviceCount"},
		{&mxSmlGetPfDeviceCount, "mxSmlGetPfDeviceCount"},
		{&mxSmlGetDeviceInfo, "mxSmlGetDeviceInfo"},
		{&mxSmlGetDeviceDieCount, "mxSmlGetDeviceDieCount"},
		{&mxSmlGetDeviceVersion, "mxSmlGetDeviceVersion"},
		{&mxSmlGetBoardPowerInfo, "mxSmlGetBoardPowerInfo"},
		{&mxSmlGetPcieInfo, "mxSmlGetPcieInfo"},
		{&mxSmlGetPcieThroughput, "mxSmlGetPcieThroughput"},
		{&mxSmlGetMetaXLinkInfo_v2, "mxSmlGetMetaXLinkInfo_v2"},
		{&mxSmlGetMetaXLinkBandwidth, "mxSmlGetMetaXLinkBandwidth"},
		{&mxSmlGetMetaXLinkTrafficStat, "mxSmlGetMetaXLinkTrafficStat"},
		{&mxSmlGetMetaXLinkAer, "mxSmlGetMetaXLinkAer"},
		{&mxSmlGetDieUnavailableReason, "mxSmlGetDieUnavailableReason"},
		{&mxSmlGetDieTemperatureInfo, "mxSmlGetDieTemperatureInfo"},
		{&mxSmlGetDieIpUsage, "mxSmlGetDieIpUsage"},
		{&mxSmlGetDieMemoryInfo, "mxSmlGetDieMemoryInfo"},
		{&mxSmlGetDieClocks, "mxSmlGetDieClocks"},
		{&mxSmlGetDieCurrentClocksThrottleReason, "mxSmlGetDieCurrentClocksThrottleReason"},
		{&mxSmlGetCurrentDieDpmIpPerfLevel, "mxSmlGetCurrentDieDpmIpPerfLevel"},
		{&mxSmlGetDieTotalEccErrors, "mxSmlGetDieTotalEccErrors"},
	}
	for _, registration := range registrations {
		symbol, err := purego.Dlsym(handle, registration.name)
		if err != nil {
			return fmt.Errorf("required symbol %s not found: %w", registration.name, err)
		}
		purego.RegisterFunc(registration.function, symbol)
	}
	return nil
}
