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

// Package kernaddr encodes and decodes 64-bit kernel pointers as hex strings.
//
// Kernel addresses on 64-bit platforms commonly have the high bit set and
// exceed the range of a signed int64, so they must travel as strings in JSON
// destined for Elasticsearch (which maps numeric fields as `long`).
package kernaddr

import (
	"fmt"
	"strconv"
	"strings"
)

// Format renders a kernel pointer as a "0x"-prefixed 16-digit hex string.
// Zero returns the empty string so JSON `omitempty` tags drop the field.
func Format(addr uint64) string {
	if addr == 0 {
		return ""
	}
	return fmt.Sprintf("0x%016x", addr)
}

// Parse parses a hex kernel pointer string produced by Format. Returns
// ok=false for empty, malformed, or zero values.
func Parse(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return v, true
}
