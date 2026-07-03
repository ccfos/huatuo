// Copyright 2026 The HuaTuo Authors
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

package bpf

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var errInvalidName = errors.New("invalid bpf name")

// validateName guards bpf object names supplied via CLI/config. LoadBpf
// joins name into DefaultObjDir; a "../" prefix would escape that
// directory and let a caller load arbitrary files. Slashes and absolute
// paths are otherwise fine — names like "./_output/bpf/iotracing.o" are
// expected.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("empty: %w", errInvalidName)
	}

	cleaned := filepath.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q: path traversal: %w", name, errInvalidName)
	}

	return nil
}
