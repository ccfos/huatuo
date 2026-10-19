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
	"encoding/json"
	"encoding/xml"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSVGSearchCountsAdjacentFrames(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded SVG search script")
	}
	style := DefaultStyle
	style.ImageWidth = 120
	for _, rooted := range []bool{false, true} {
		stacks := []Stack{
			{Names: []string{"a"}, Samples: 23},
			{Names: []string{"hit_b", "hit_nested"}, Samples: 28},
			{Names: []string{"hit_c"}, Samples: 949},
		}
		if rooted {
			for i := range stacks {
				stacks[i].Names = append([]string{"root"}, stacks[i].Names...)
			}
		}
		for _, tc := range []struct{ pattern, want string }{
			{"^hit", "Matched: 97.7%"},
			{"^hit_b$", "Matched: 2.8%"},
			{".*", "Matched: 100%"},
			{"^absent$", ""},
		} {
			checkSVGSearch(t, node, stacks, style, tc.pattern, tc.want)
		}
	}
}

func checkSVGSearch(t *testing.T, node string, stacks []Stack, style Style, pattern, want string) {
	t.Helper()
	var output bytes.Buffer
	require.NoError(t, RenderStyle(stacks, &output, style))
	var svg struct {
		Groups []struct {
			Title string `xml:"title" json:"Title"`
			Rect  struct {
				X     string `xml:"x,attr" json:"X"`
				Width string `xml:"width,attr" json:"Width"`
			} `xml:"rect" json:"Rect"`
		} `xml:"g"`
	}
	require.NoError(t, xml.Unmarshal(output.Bytes(), &svg))
	groups, err := json.Marshal(svg.Groups)
	require.NoError(t, err)
	query, err := json.Marshal(pattern)
	require.NoError(t, err)
	// Use real rendered coordinates and the production script. Only the DOM
	// operations needed by search are stubbed; no browser layout is involved.
	script := `const groups = ` + string(groups) + `;
const elements = groups.map(g => ({
  attributes: {class: {value: 'func_g'}},
  childNodes: [
    {tagName: 'title', firstChild: {nodeValue: g.Title}},
    {tagName: 'rect', attributes: {x: {value: g.Rect.X}, width: {value: g.Rect.Width}, fill: {value: 'red'}},
      setAttribute(k, v) { this.attributes[k] = {value: v}; }}
  ]
}));
const window = {addEventListener() {}};
const document = {getElementsByTagName() {return elements;}};
` + javascript + `
searchbtn = {style: {}, firstChild: {nodeValue: ''}};
matchedtxt = {style: {}, firstChild: {nodeValue: ''}};
searching = 0;
search(` + string(query) + `);
console.log(matchedtxt.firstChild.nodeValue);
`
	cmd := exec.CommandContext(t.Context(), node)
	cmd.Stdin = strings.NewReader(script)
	result, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", result)
	require.Equal(t, want, strings.TrimSpace(string(result)), "pattern %q, stacks %v", pattern, stacks)
}
