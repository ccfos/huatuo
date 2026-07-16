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

package service

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
)

func TestStandalonePprofAndSVGExportsUseTheSameSelection(t *testing.T) {
	start := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	installFakeProfileStorage(t,
		testProfileDocument(start.Add(time.Second), "node-a", "42", 10, "root", "hot"),
		testProfileDocument(start.Add(2*time.Second), "node-b", "42", 999, "root", "wrong-host"),
	)
	req := &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a",tgid="42"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(5 * time.Second).UnixMilli(),
	}

	payload, err := MarshalPprof(req)
	if err != nil {
		t.Fatalf("MarshalPprof() error = %v", err)
	}
	decoded, err := pprof.RawFromBytes(payload)
	if err != nil {
		t.Fatalf("RawFromBytes() error = %v", err)
	}
	if len(decoded.Sample) != 1 || decoded.Sample[0].Value[0] != 10 {
		t.Fatalf("pprof samples = %#v, want selected value 10", decoded.Sample)
	}

	var svg bytes.Buffer
	if err := RenderProfileSVG(req, &svg); err != nil {
		t.Fatalf("RenderProfileSVG() error = %v", err)
	}
	content := svg.String()
	for _, expected := range []string{"<svg", "Flame Graph", "root", "hot", "Search"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("SVG does not contain %q", expected)
		}
	}
	if strings.Contains(content, "wrong-host") {
		t.Fatal("SVG contains stack excluded by label selector")
	}
}

func TestStandaloneSVGDoesNotEmbedSymbolNamesInJavaScript(t *testing.T) {
	start := time.Date(2026, time.July, 16, 13, 0, 0, 0, time.UTC)
	malicious := `');alert(1);//<script>alert(2)</script>`
	installFakeProfileStorage(t,
		testProfileDocument(start.Add(time.Second), "node-a", "42", 1, "root", malicious),
	)
	req := &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profiler.ProfileTypeCpuSample,
		LabelSelector: `{hostname="node-a"}`,
		Start:         start.UnixMilli(),
		End:           start.Add(2 * time.Second).UnixMilli(),
	}
	var svg bytes.Buffer
	if err := RenderProfileSVG(req, &svg); err != nil {
		t.Fatalf("RenderProfileSVG() error = %v", err)
	}
	content := svg.String()
	if strings.Contains(content, `onmouseover="s('`) || strings.Contains(content, "<script>alert(2)</script>") {
		t.Fatalf("SVG embeds untrusted symbol as executable markup: %s", content)
	}
	if !strings.Contains(content, `data-title="`) || !strings.Contains(content, "&lt;script&gt;") {
		t.Fatalf("SVG does not preserve the escaped symbol as data")
	}
}
