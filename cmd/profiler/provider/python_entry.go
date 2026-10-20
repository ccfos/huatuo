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

package provider

import (
	"path/filepath"
	"strings"
)

// pythonEntryName accepts a nonempty command line. A script is optional:
// CPython can execute standard input or an interactive session with no entry.
func pythonEntryName(cmdline []string) string {
	for _, arg := range cmdline[1:] {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return filepath.Base(cmdline[0])
}
