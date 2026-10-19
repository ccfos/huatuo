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
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderStylePreservesFontFamilyAttributes(t *testing.T) {
	for _, family := range []string{"Verdana", `"Fira Code", monospace`, `"A&B <Mono>"`} {
		t.Run(family, func(t *testing.T) {
			style := DefaultStyle
			style.FontFamily = family
			var output bytes.Buffer
			require.NoError(t, RenderStyle([]Stack{{Names: []string{"root", "leaf"}, Samples: 1}}, &output, style))
			decoder := xml.NewDecoder(&output)
			textCount := 0
			for {
				token, err := decoder.Token()
				if errors.Is(err, io.EOF) {
					break
				}
				require.NoError(t, err, "rendered SVG must remain well-formed XML")
				start, ok := token.(xml.StartElement)
				if !ok || start.Name.Local != "text" {
					continue
				}
				textCount++
				var families []string
				for _, attr := range start.Attr {
					if attr.Name.Local == "font-family" {
						families = append(families, attr.Value)
					}
				}
				require.Equal(t, []string{family}, families)
			}
			require.Equal(t, 7, textCount, "five controls and two frame labels")
		})
	}
}
