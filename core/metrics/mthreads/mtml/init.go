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

// Init obtains a MtmlLibrary* interface handle from libmtml.so.
//
// On first call: dlopens libmtml.so (if not already loaded) and calls
// mtmlLibraryInit. On subsequent calls (e.g. the collector's
// recoverable-error recovery path after a Shutdown): the .so is
// already mapped, so we only re-issue mtmlLibraryInit to obtain a
// fresh MtmlLibrary* handle. The dlopen mapping is never released
// for the process lifetime.
func Init() error {
	libmtml.Lock()
	defer libmtml.Unlock()

	if libmtml.ready {
		return nil
	}

	if _, err := libmtml.loadLocked(); err != nil {
		libmtml.ready = false
		return err
	}

	// mtmlLibraryInit(MtmlLibrary** lib) — called on first Init and
	// on every re-init after a Shutdown.
	var lib uintptr
	if mtmlErr := checkReturnCode("mtmlLibraryInit", mtmlLibraryInit(&lib)); mtmlErr != nil {
		libmtml.ready = false
		libmtml.lib = 0
		return mtmlErr
	}

	libmtml.lib = lib
	libmtml.ready = true
	return nil
}

// Shutdown releases the MtmlLibrary* interface handle (mtmlLibraryShutDown).
//
// The .so mapping is intentionally NOT released. This matches the
// vendor binding's lifecycle (pymtml.py: "Leave the library loaded,
// but shutdown the interface"): all purego-registered mtml* function
// pointers (types.go) keep pointing into the .so code section, so
// subsequent mtml.* calls stay valid. A subsequent Init() re-issues
// mtmlLibraryInit to obtain a fresh MtmlLibrary* handle without
// reloading the .so or re-registering any symbol.
func Shutdown() error {
	libmtml.Lock()
	defer libmtml.Unlock()

	if libmtml.loadedAs == "" {
		return nil
	}

	if libmtml.lib == 0 {
		return nil
	}

	if err := checkReturnCode("mtmlLibraryShutDown", mtmlLibraryShutDown(libmtml.lib)); err != nil {
		return err
	}
	libmtml.lib = 0
	libmtml.ready = false
	return nil
}

// IsReady reports whether the mtml library is in a state where
// native calls are safe.
func IsReady() bool {
	libmtml.Lock()
	defer libmtml.Unlock()
	return libmtml.ready
}
