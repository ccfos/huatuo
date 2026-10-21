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

import "testing"

func TestEscapeControls(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ordinary", `worker /path/中文🙂 "quoted" \literal`, `worker /path/中文🙂 "quoted" \literal`},
		{"line breaks", "a\nb\rc\td", `a\nb\rc\td`},
		{"terminal", "\x1b[2J\x00\x7f", `\x1b[2J\x00\x7f`},
		{"unicode separators", "前\u0085后\u2028末\u2029", `前\u0085后\u2028末\u2029`},
		{"invalid UTF-8", string([]byte{0xff, '\n', 0xfe}), string([]byte{0xff, '\\', 'n', 0xfe})},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := EscapeControls(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func BenchmarkEscapeControls(b *testing.B) {
	for _, value := range []string{"normal-worker", "工人🙂worker", "line\nbreak\x1b"} {
		b.Run(value, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = EscapeControls(value)
			}
		})
	}
}
