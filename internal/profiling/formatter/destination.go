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

package formatter

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateDestination rejects an absent output path.
// Filesystem type checks belong to the formatter that opens the destination.
func ValidateDestination(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("--output-path must not be empty")
	}
	return nil
}

func isStdoutDestination(path string) bool {
	switch filepath.Clean(path) {
	case "-", "stdout", "/dev/stdout", "/dev/fd/1", "/proc/self/fd/1":
		return true
	}
	return false
}
