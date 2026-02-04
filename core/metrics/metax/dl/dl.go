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

package dl

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// DynamicLibrary is the default implementation of dynamicLibrary
// based on purego.
type DynamicLibrary struct {
	path   string
	flags  int
	handle uintptr
}

// New creates a new dynamic library loader for the given path and flags.
func New(path string, flags int) *DynamicLibrary {
	return &DynamicLibrary{
		path:  path,
		flags: flags,
	}
}

// Open loads the shared library if it is not already loaded.
func (dl *DynamicLibrary) Open() error {
	if dl.handle != 0 {
		return fmt.Errorf("library %s already opened", dl.path)
	}

	handle, err := purego.Dlopen(dl.path, dl.flags)
	if err != nil {
		return fmt.Errorf("dlopen %s failed: %w", dl.path, err)
	}

	dl.handle = handle
	return nil
}

// Close unloads the shared library if it is currently loaded.
func (dl *DynamicLibrary) Close() error {
	if dl.handle == 0 {
		return nil
	}

	if err := purego.Dlclose(dl.handle); err != nil {
		return fmt.Errorf("dlclose %s failed: %w", dl.path, err)
	}

	dl.handle = 0
	return nil
}

// Handle returns the underlying dlopen handle.
// It is valid only after a successful Open call.
func (dl *DynamicLibrary) Handle() uintptr {
	return dl.handle
}
