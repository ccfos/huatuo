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
	"runtime"
	"sync"

	"huatuo-bamai/core/metrics/metax/dl"

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
	l.registerSmlLibSymbols(l.dl.Handle())

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

// registerSmlLibSymbols registers all required SML symbols
// from the loaded shared library.
func (l *library) registerSmlLibSymbols(handle uintptr) {
	purego.RegisterLibFunc(&mxSmlInit, handle, "mxSmlInit")
	purego.RegisterLibFunc(&mxSmlGetErrorString, handle, "mxSmlGetErrorString")
	purego.RegisterLibFunc(&mxSmlGetMacaVersion, handle, "mxSmlGetMacaVersion")
	purego.RegisterLibFunc(&mxSmlGetDeviceCount, handle, "mxSmlGetDeviceCount")
	purego.RegisterLibFunc(&mxSmlGetPfDeviceCount, handle, "mxSmlGetPfDeviceCount")
	purego.RegisterLibFunc(&mxSmlGetDeviceInfo, handle, "mxSmlGetDeviceInfo")
	purego.RegisterLibFunc(&mxSmlGetDeviceDieCount, handle, "mxSmlGetDeviceDieCount")
	purego.RegisterLibFunc(&mxSmlGetDeviceVersion, handle, "mxSmlGetDeviceVersion")
	purego.RegisterLibFunc(&mxSmlGetBoardPowerInfo, handle, "mxSmlGetBoardPowerInfo")
	purego.RegisterLibFunc(&mxSmlGetPcieInfo, handle, "mxSmlGetPcieInfo")
	purego.RegisterLibFunc(&mxSmlGetPcieThroughput, handle, "mxSmlGetPcieThroughput")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkInfo_v2, handle, "mxSmlGetMetaXLinkInfo_v2")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkBandwidth, handle, "mxSmlGetMetaXLinkBandwidth")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkTrafficStat, handle, "mxSmlGetMetaXLinkTrafficStat")
	purego.RegisterLibFunc(&mxSmlGetMetaXLinkAer, handle, "mxSmlGetMetaXLinkAer")
	purego.RegisterLibFunc(&mxSmlGetDieUnavailableReason, handle, "mxSmlGetDieUnavailableReason")
	purego.RegisterLibFunc(&mxSmlGetDieTemperatureInfo, handle, "mxSmlGetDieTemperatureInfo")
	purego.RegisterLibFunc(&mxSmlGetDieIpUsage, handle, "mxSmlGetDieIpUsage")
	purego.RegisterLibFunc(&mxSmlGetDieMemoryInfo, handle, "mxSmlGetDieMemoryInfo")
	purego.RegisterLibFunc(&mxSmlGetDieClocks, handle, "mxSmlGetDieClocks")
	purego.RegisterLibFunc(&mxSmlGetDieCurrentClocksThrottleReason, handle, "mxSmlGetDieCurrentClocksThrottleReason")
	purego.RegisterLibFunc(&mxSmlGetCurrentDieDpmIpPerfLevel, handle, "mxSmlGetCurrentDieDpmIpPerfLevel")
	purego.RegisterLibFunc(&mxSmlGetDieTotalEccErrors, handle, "mxSmlGetDieTotalEccErrors")
}
