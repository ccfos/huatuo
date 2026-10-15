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
