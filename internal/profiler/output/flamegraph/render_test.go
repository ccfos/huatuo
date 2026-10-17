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

package flamegraph

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderStyleKeepsSymbolsOutOfJavaScript(t *testing.T) {
	symbol := `');alert(1);//<script>alert(2)</script>`
	var output bytes.Buffer
	if err := RenderStyle([]Stack{{
		Names:   []string{"root", symbol},
		Samples: 1,
	}}, &output, DefaultStyle); err != nil {
		t.Fatalf("RenderStyle() error = %v", err)
	}

	content := output.String()
	if strings.Contains(content, `onmouseover="s('`) ||
		strings.Contains(content, "<script>alert(2)</script>") {
		t.Fatalf("SVG embeds an untrusted symbol as executable markup: %s", content)
	}
	if !strings.Contains(content, `data-title="`) ||
		!strings.Contains(content, "&lt;script&gt;alert(2)&lt;/script&gt;") {
		t.Fatalf("SVG does not preserve the escaped symbol as data: %s", content)
	}
}

func TestFitTextTruncatesOnRuneBoundaries(t *testing.T) {
	renderer := newRenderer(DefaultStyle, 1)

	// 110px of frame is 15 glyphs wide at the default style, so the name is cut
	// after 13 glyphs. Each glyph below is three bytes, which puts the byte-based
	// cut in the middle of a rune.
	symbol := strings.Repeat("缓慢调用", 10)

	got := renderer.fitText(symbol, 110)
	if !utf8.ValidString(got) {
		t.Fatalf("fitText() produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "..") {
		t.Fatalf("fitText() did not truncate the over-long symbol: %q", got)
	}
	if runes := utf8.RuneCountInString(got); runes != 15 {
		t.Fatalf("fitText() kept %d glyphs, want 15: %q", runes, got)
	}
}

func TestRenderStyleEmitsValidUTF8ForMultibyteSymbols(t *testing.T) {
	// The SVG is XML, so a symbol truncated in the middle of a multi-byte
	// sequence makes the whole document malformed.
	symbol := strings.Repeat("缓慢调用栈", 40)
	var output bytes.Buffer
	if err := RenderStyle([]Stack{{
		Names:   []string{"root", symbol},
		Samples: 1,
	}}, &output, DefaultStyle); err != nil {
		t.Fatalf("RenderStyle() error = %v", err)
	}

	if content := output.String(); !utf8.ValidString(content) {
		t.Fatalf("SVG is not valid UTF-8 for a multi-byte symbol: %q", content)
	}
}
