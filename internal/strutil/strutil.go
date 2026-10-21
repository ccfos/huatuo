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

package strutil

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func SplitCommaList(raw string) []string {
	if raw == "" {
		return nil
	}

	var parts []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}

	return parts
}

// EscapeControls makes control characters visible without changing ordinary text.
// Use it on display fields, leaving the original data available for matching and
// structured output.
func EscapeControls(value string) string {
	isControl := func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }
	first := strings.IndexFunc(value, isControl)
	if first < 0 {
		return value
	}
	var out strings.Builder
	out.Grow(len(value) + 8)
	last := 0
	for offset, r := range value[first:] {
		if !isControl(r) {
			continue
		}
		i := first + offset
		out.WriteString(value[last:i])
		quoted := strconv.QuoteRune(r)
		out.WriteString(quoted[1 : len(quoted)-1])
		last = i + utf8.RuneLen(r)
	}
	out.WriteString(value[last:])
	return out.String()
}
