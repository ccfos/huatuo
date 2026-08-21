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

	"github.com/ebitengine/purego"
)

// Init loads libmtml.so, registers all symbols, and calls mtmlLibraryInit
// to obtain the MtmlLibrary* interface handle.
func Init() error {
	libmtml.Lock()
	defer libmtml.Unlock()

	if libmtml.lib != 0 {
		return nil
	}

	handle, err := libmtml.loadLocked()
	if err != nil {
		return err
	}

	// mtmlLibraryInit(MtmlLibrary** lib) — the only place we call this.
	// If it fails, dlclose the handle we just opened so we don't leak
	// the .so mapping.
	var lib uintptr
	if mtmlErr := checkReturnCode("mtmlLibraryInit", mtmlLibraryInit(&lib)); mtmlErr != nil {
		_ = purego.Dlclose(handle)
		libmtml.handle = 0
		libmtml.loadedAs = ""
		return mtmlErr
	}

	libmtml.lib = lib
	return nil
}

// Shutdown releases the MtmlLibrary* (mtmlLibraryShutDown) and unloads
// libmtml.so (dlclose).
//
// After Shutdown returns, every purego-registered mtml* function pointer
// is stale — any subsequent mtml.* call will SIGSEGV. The contract is
// "one Init, one Shutdown": Shutdown is called exactly once at daemon
// exit, and no code path may invoke any mtml.* function after it returns.
func Shutdown() error {
	libmtml.Lock()
	defer libmtml.Unlock()

	if libmtml.loadedAs == "" {
		return nil
	}

	if libmtml.lib != 0 {
		_ = checkReturnCode("mtmlLibraryShutDown", mtmlLibraryShutDown(libmtml.lib))
		libmtml.lib = 0
	}

	libmtml.closeLocked()
	if libmtml.loadedAs != "" {
		return fmt.Errorf("mthreads: mtml library not fully released after Shutdown")
	}
	return nil
}
