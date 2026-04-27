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

package testutils

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/cpu"
)

// NativeFile substitutes %s with an abbreviation of the host endianness.
func NativeFile(tb testing.TB, path string) string {
	tb.Helper()

	if !strings.Contains(path, "%s") {
		tb.Fatalf("File %q doesn't contain %%s", path)
	}

	if cpu.IsBigEndian {
		return fmt.Sprintf(path, "eb")
	}

	return fmt.Sprintf(path, "el")
}
