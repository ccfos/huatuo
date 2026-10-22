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
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestRenderKeepsSymbolsOutOfJavaScript(t *testing.T) {
	symbol := `');alert(1);//<script>alert(2)</script>`
	var proc processor
	proc.Process(Stack{
		Names:   []string{"root", symbol},
		Samples: 1,
	})

	var output bytes.Buffer
	if err := proc.render(&output, DefaultStyle); err != nil {
		t.Fatalf("render flame graph: %v", err)
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

func TestRenderReplacesXMLForbiddenFrameCharacters(t *testing.T) {
	for _, symbol := range []string{"work\x00name", "work\x01name", "work\x1bname", "work\ufffename", "work\uffffname"} {
		t.Run(symbol, func(t *testing.T) {
			var proc processor
			proc.Process(Stack{Names: []string{symbol}, Samples: 1})
			var output bytes.Buffer
			if err := proc.render(&output, DefaultStyle); err != nil {
				t.Fatal(err)
			}
			decoder := xml.NewDecoder(bytes.NewReader(output.Bytes()))
			for {
				_, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("symbol %q produced invalid SVG: %v", symbol, err)
				}
			}
			if !strings.Contains(output.String(), "work") || !strings.Contains(output.String(), "name") {
				t.Fatal("symbol context lost")
			}
		})
	}
}

func TestXMLFrameNamePreservesValidUnicode(t *testing.T) {
	input := "work\t\n\r café 中文 🦀 &<>'\""
	if got := xmlFrameName(input); got != input {
		t.Fatalf("valid XML text changed: %q", got)
	}
	if got := xmlFrameName("before\x01after"); got != "before\ufffdafter" {
		t.Fatalf("replacement = %q", got)
	}
}

func BenchmarkXMLFrameName(b *testing.B) {
	for _, name := range []string{"runtime.mallocgc", "java.lang.Thread.run 中文 🦀", "work\x01name"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = xmlFrameName(name)
			}
		})
	}
}
