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
	"runtime"
	"sync"

	"huatuo-bamai/core/metrics/ascend/dl"

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

	// Register all symbols after successful loading.
	l.registerDcmiLibSymbols(l.dl.Handle())

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

// registerDcmiLibSymbols registers all required DCMI symbols
// from the loaded shared library.
func (l *library) registerDcmiLibSymbols(handle uintptr) {
	purego.RegisterLibFunc(&dcmiInit, handle, "dcmi_init")
	purego.RegisterLibFunc(&dcGetDeviceHealth, handle, "dcmi_get_device_health")
	purego.RegisterLibFunc(&dcGetCardList, handle, "dcmi_get_card_list")
	purego.RegisterLibFunc(&dcGetDeviceNumInCard, handle, "dcmi_get_device_num_in_card")
}
