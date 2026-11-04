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

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

func testPprofProfile() *profilev1.Profile {
	return &profilev1.Profile{
		StringTable: []string{"", "cpu", "nanoseconds", "root", "worker", "leaf", "other", "samples", "count"},
		SampleType:  []*profilev1.ValueType{{Type: 1, Unit: 2}},
		Function:    []*profilev1.Function{{Id: 1, Name: 3}, {Id: 2, Name: 4}, {Id: 3, Name: 5}, {Id: 4, Name: 6}},
		Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}, {Id: 2, Line: []*profilev1.Line{{FunctionId: 2}}}, {Id: 3, Line: []*profilev1.Line{{FunctionId: 3}}}, {Id: 4, Line: []*profilev1.Line{{FunctionId: 4}}}},
		Sample:      []*profilev1.Sample{{LocationId: []uint64{3, 2, 1}, Value: []int64{70}}, {LocationId: []uint64{2, 1}, Value: []int64{20}}, {LocationId: []uint64{4, 1}, Value: []int64{10}}},
	}
}

func profileSourceJSON(t *testing.T, profile *profilev1.Profile, protobufJSON bool) []byte {
	t.Helper()
	var raw []byte
	var err error
	if protobufJSON {
		raw, err = protojson.Marshal(profile)
	} else {
		raw, err = json.Marshal(profile)
	}
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"tracer_data":{"flamedata":{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","profile":` + string(raw) + `}}}`)
}

func TestNormalizeProfileHandlesFrameAndPprofFormats(t *testing.T) {
	wantFrames := []flamegraph.FrameData{{Level: 0, Label: "root", Value: 100}, {Level: 1, Label: "leaf", Value: 100, Self: 100}}
	array, err := json.Marshal(map[string]any{"tracer_data": map[string]any{"flamedata": wantFrames}})
	if err != nil {
		t.Fatal(err)
	}
	frames, sampleType, sampleUnit, err := normalizeProfile(t.Context(), array)
	if err != nil || sampleType != "" || sampleUnit != "" || !reflect.DeepEqual(frames, wantFrames) {
		t.Fatalf("frame payload=%+v %s/%s %v", frames, sampleType, sampleUnit, err)
	}
	for _, protobufJSON := range []bool{false, true} {
		frames, sampleType, sampleUnit, err := normalizeProfile(t.Context(), profileSourceJSON(t, testPprofProfile(), protobufJSON))
		if err != nil || sampleType != "cpu" || sampleUnit != "nanoseconds" {
			t.Fatalf("pprof payload=%+v %s/%s %v", frames, sampleType, sampleUnit, err)
		}
		byName := make(map[string]flamegraph.FrameData)
		for _, frame := range frames {
			byName[frame.Label] = frame
		}
		if byName[ProfileRootLabel].Value != 100 || byName["worker"].Value != 90 || byName["worker"].Self != 20 || byName["leaf"].Self != 70 || byName["other"].Self != 10 {
			t.Fatalf("incorrect normalized weights: %+v", frames)
		}
		if byName["worker"].Level != 2 || byName["leaf"].Level != 3 {
			t.Fatalf("leaf/root stack orientation reversed: %+v", frames)
		}
	}
}

func TestDecodedDocumentPreservesProfileWeights(t *testing.T) {
	for _, weight := range []int64{9007199254740993, math.MaxInt64} {
		profile := testPprofProfile()
		profile.Sample = profile.Sample[:1]
		profile.Sample[0].Value = []int64{weight}
		for _, protobufJSON := range []bool{false, true} {
			raw := profileSourceJSON(t, profile, protobufJSON)
			raw = append([]byte(`{"uploaded_timestamp":"2026-10-12T10:10:00Z",`), raw[1:]...)
			var document event.Document
			if err := decodeDocument(raw, &document); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			frames, sampleType, sampleUnit, err := normalizeProfile(t.Context(), encoded)
			if err != nil || len(frames) == 0 || frames[0].Value != weight || sampleType != "cpu" || sampleUnit != "nanoseconds" {
				t.Fatalf("weight=%d protobuf=%t: frames=%+v type=%s unit=%s err=%v", weight, protobufJSON, frames, sampleType, sampleUnit, err)
			}
		}
	}
}

func TestPprofSelectionInlineFramesAndInvalidProfiles(t *testing.T) {
	profile := testPprofProfile()
	profile.SampleType = []*profilev1.ValueType{{Type: 7, Unit: 8}, {Type: 1, Unit: 2}}
	for _, sample := range profile.Sample {
		sample.Value = []int64{1, sample.Value[0]}
	}
	profile.Location[2].Line = append(profile.Location[2].Line, &profilev1.Line{FunctionId: 4})
	frames, sampleType, sampleUnit, err := profileFrames(t.Context(), profile, "process_cpu:cpu:nanoseconds:cpu:nanoseconds")
	if err != nil || sampleType != "cpu" || sampleUnit != "nanoseconds" || frames[0].Value != 100 {
		t.Fatalf("wrong sample selection: %+v %s/%s %v", frames, sampleType, sampleUnit, err)
	}
	var inlineLeafDepth int64
	for _, frame := range frames {
		if frame.Label == "leaf" {
			inlineLeafDepth = frame.Level
		}
	}
	if inlineLeafDepth != 4 {
		t.Fatalf("inline frame lost: %+v", frames)
	}
	for _, mutate := range []func(*profilev1.Profile){
		func(p *profilev1.Profile) { p.Sample = nil },
		func(p *profilev1.Profile) { p.Sample[0].LocationId = []uint64{999} },
		func(p *profilev1.Profile) { p.Function[0].Name = 999 },
		func(p *profilev1.Profile) { p.Sample[0].Value[0] = -1 },
		func(p *profilev1.Profile) { p.Sample[0].Value[0] = math.MaxInt64 },
		func(p *profilev1.Profile) { p.Sample[0].LocationId = make([]uint64, MaxProfileSourceDepth+1) },
	} {
		profile := testPprofProfile()
		mutate(profile)
		if _, _, _, err := profileFrames(t.Context(), profile, ""); err == nil {
			t.Fatal("malformed/oversized pprof accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := profileFrames(ctx, testPprofProfile(), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("pprof cancellation=%v", err)
	}
}

func TestProfileReadNormalizesPprofAndReportsMalformedPayload(t *testing.T) {
	valid := profileSourceJSON(t, testPprofProfile(), false)
	var good map[string]any
	if err := json.Unmarshal(valid, &good); err != nil {
		t.Fatal(err)
	}
	good["uploaded_time"] = "2026-10-12T10:10:00Z"
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		query := string(body["query"])
		if !strings.Contains(query, ProfileFramesField) || !strings.Contains(query, ProfileSamplesField) || !strings.Contains(query, "sampleType.type") || !strings.Contains(query, "functionId") || strings.Contains(query, "tracer_type") {
			t.Errorf("profile discovery/read constraints=%s", query)
		}
		bad := map[string]any{"uploaded_time": "2026-10-12T10:09:00Z", "tracer_data": map[string]any{"flamedata": map[string]any{"profile_type": "cpu", "profile": map[string]any{}}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{map[string]any{"_id": "good", "_source": good}, map[string]any{"_id": "bad", "_source": bad}}}})
	}, nil)
	result, err := reader.SearchEvents(t.Context(), &event.Query{Region: "cn", ProfileOnly: true, Limit: 2})
	if err != nil || len(result.Documents) != 2 || len(result.Documents[0].ProfileFrames) == 0 || result.Documents[0].ProfileSampleUnit != "nanoseconds" || len(result.Documents[1].ProfileFrames) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("pprof read=%+v %v", result, err)
	}
}

func TestProfileOnlyUnindexedPayloadUsesSourceWithoutDroppingFilters(t *testing.T) {
	caps := capsFixture(true)
	for _, group := range profileFieldGroups() {
		for _, name := range group {
			delete(caps.Fields, name)
		}
	}
	schema, err := resolveFieldSchema(&caps)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err := json.Unmarshal(profileSourceJSON(t, testPprofProfile(), true), &source); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"region": "cn", "hostname": "node-a", "tracer_name": "cpusys", "uploaded_timestamp": 1768471800123} {
		source[name] = value
	}
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/_search" {
			t.Errorf("profile compatibility read left configured selector: %s", r.URL.Path)
		}
		var body struct {
			Query json.RawMessage `json:"query"`
			Size  int             `json:"size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		query := string(body.Query)
		for _, expected := range []string{`"region"`, `"hostname"`, `"tracer_name"`, `"range"`} {
			if !strings.Contains(query, expected) {
				t.Errorf("profile source read dropped safe predicate %s: %s", expected, query)
			}
		}
		if strings.Contains(query, `"exists"`) || body.Size != MaxSourceScanDocuments+1 {
			t.Errorf("unindexed profile markers or unbounded request: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{
			map[string]any{"_id": "profile", "_source": source},
			map[string]any{"_id": "no-payload", "_source": map[string]any{"region": "cn", "hostname": "node-a", "tracer_name": "cpusys", "uploaded_timestamp": 1768471800123}},
		}}})
	}, nil)
	reader.fields = schema
	query := &event.Query{
		Region: "cn", Hostnames: []string{"node-a"}, TracerNames: []string{"cpusys"}, ProfileOnly: true,
		StartTime: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC), Limit: 1,
	}
	count, err := reader.Count(t.Context(), query)
	if err != nil || count != 1 {
		t.Fatalf("profile count=%d error=%v", count, err)
	}
	tracers, err := reader.TracerNames(t.Context(), query)
	if err != nil || !reflect.DeepEqual(tracers, []string{"cpusys"}) {
		t.Fatalf("profile tracers=%v error=%v", tracers, err)
	}
	result, err := reader.SearchEvents(t.Context(), query)
	if err != nil || len(result.Documents) != 1 || len(result.Warnings) != 0 {
		t.Fatalf("profile search=%+v error=%v", result, err)
	}
	if len(result.Documents[0].ProfileFrames) == 0 || result.Documents[0].ProfileSampleUnit != "nanoseconds" {
		t.Fatalf("unindexed profile was not normalized: %+v", result.Documents[0])
	}
}

func TestProfileFallbackLimitsUnqualifiedCandidatesToUnindexedIndices(t *testing.T) {
	caps := capsFixture(true)
	caps.Indices = []string{"indexed", "source-only"}
	for _, group := range profileFieldGroups() {
		for _, name := range group {
			delete(caps.Fields, name)
		}
	}
	caps.Fields[ProfileFramesField] = map[string]fieldCapability{"keyword": {
		Searchable: true, Indices: []string{"indexed"},
	}}
	schema, err := resolveFieldSchema(&caps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.sourceProfileIndices, []string{"source-only"}) {
		t.Fatalf("unexpected source-only indices: %v", schema.sourceProfileIndices)
	}
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/_search" {
			t.Errorf("configured selector replaced: %s", r.URL.Path)
		}
		var body struct {
			Query json.RawMessage `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		query := string(body.Query)
		for _, expected := range []string{`"region"`, `"range"`, `"_index":["source-only"]`, ProfileFramesField, `"minimum_should_match":1`} {
			if !strings.Contains(query, expected) {
				t.Errorf("profile compatibility candidate scope lost %s: %s", expected, query)
			}
		}
		_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
	}, nil)
	reader.fields = schema
	_, err = reader.Count(t.Context(), &event.Query{
		Region: "cn", ProfileOnly: true,
		StartTime: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
}
