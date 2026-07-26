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

package autotracing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/tracing"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestBuildAndSaveCPUSystemWritesJSONAndPprof(t *testing.T) {
	tracingBackend := &captureProfileBackend{}
	profileBackend := &captureProfileBackend{}
	configureAutotracingStores(t, tracingBackend, profileBackend)

	start := time.Unix(1700000000, 123).UTC()
	duration := 10 * time.Second
	rawFrames := []byte(`[
		{"level":0,"value":5,"self":0,"label":"root"},
		{"level":1,"value":5,"self":5,"label":"leaf"}
	]`)
	cpu := &cpuSysTracing{sysPercent: 70, sysPercentDelta: 25}
	err := cpu.buildAndSaveCPUSystem(
		start,
		duration,
		&cpuSysThreshold{usage: 45, delta: 20},
		rawFrames,
	)
	if err != nil {
		t.Fatalf("buildAndSaveCPUSystem returned error: %v", err)
	}

	if len(tracingBackend.records) != 1 {
		t.Fatalf(
			"tracing records = %d, want 1",
			len(tracingBackend.records),
		)
	}
	if len(profileBackend.records) != 1 {
		t.Fatalf(
			"profile records = %d, want 1",
			len(profileBackend.records),
		)
	}

	var event struct {
		TracerID      string            `json:"tracer_id"`
		TracerName    string            `json:"tracer_name"`
		TracerRunType string            `json:"tracer_type"`
		TracerData    CpuSysTracingData `json:"tracer_data"`
	}
	if err := json.Unmarshal(tracingBackend.records[0].Data, &event); err != nil {
		t.Fatalf("decode tracing JSON: %v", err)
	}
	if event.TracerID == "" {
		t.Fatal("tracing event has an empty tracer_id")
	}
	if event.TracerName != "cpusys" {
		t.Errorf("tracer_name = %q, want cpusys", event.TracerName)
	}
	if event.TracerRunType != tracing.TracerRunTypeAutotracing {
		t.Errorf(
			"tracer_type = %q, want %q",
			event.TracerRunType,
			tracing.TracerRunTypeAutotracing,
		)
	}
	if len(event.TracerData.FlameData) != 2 {
		t.Fatalf(
			"JSON flame frames = %d, want 2",
			len(event.TracerData.FlameData),
		)
	}

	profileRecord := profileBackend.records[0]
	assertProfileField(t, profileRecord, "tracer_id", event.TracerID)
	assertProfileField(t, profileRecord, "tracer_name", "cpusys")
	assertProfileField(
		t,
		profileRecord,
		"tracer_type",
		tracing.TracerRunTypeAutotracing,
	)
	assertProfileField(
		t,
		profileRecord,
		"profile_type",
		profiler.ProfileTypeCpuSample,
	)
	assertProfileField(t, profileRecord, "hostname", "test-host")

	if got := profileRecord.Fields["profile_start_time"]; got != start {
		t.Errorf("profile_start_time = %v, want %v", got, start)
	}
	if got := profileRecord.Fields["profile_end_time"]; got != start.Add(duration) {
		t.Errorf(
			"profile_end_time = %v, want %v",
			got,
			start.Add(duration),
		)
	}

	var profile ptree.Profile
	if err := profile.UnmarshalVT(profileRecord.Data); err != nil {
		t.Fatalf("decode pprof protobuf: %v", err)
	}
	if profile.TimeNanos != start.UnixNano() {
		t.Errorf(
			"profile TimeNanos = %d, want %d",
			profile.TimeNanos,
			start.UnixNano(),
		)
	}
	if profile.DurationNanos != int64(duration) {
		t.Errorf(
			"profile DurationNanos = %d, want %d",
			profile.DurationNanos,
			duration,
		)
	}
}

func TestSaveAutotracingCPUEventPreservesOneSideOnFailure(t *testing.T) {
	t.Run("JSON backend fails", func(t *testing.T) {
		saveErr := errors.New("JSON backend unavailable")
		tracingBackend := &captureProfileBackend{saveErr: saveErr}
		profileBackend := &captureProfileBackend{}
		configureAutotracingStores(t, tracingBackend, profileBackend)

		err := saveAutotracingCPUEvent(
			&tracing.WriteRequest{
				TracerName:    "cpusys",
				TracerTime:    time.Unix(1700000000, 0).UTC(),
				TracerData:    map[string]any{"flamedata": "preserved"},
				TracerRunType: tracing.TracerRunTypeAutotracing,
			},
			time.Second,
			validAutotracingFrames(),
		)
		if !errors.Is(err, saveErr) {
			t.Fatalf("error = %v, want JSON backend error", err)
		}
		if len(profileBackend.records) != 1 {
			t.Fatalf(
				"profile records = %d, want 1 after JSON failure",
				len(profileBackend.records),
			)
		}
	})

	t.Run("pprof conversion fails", func(t *testing.T) {
		tracingBackend := &captureProfileBackend{}
		profileBackend := &captureProfileBackend{}
		configureAutotracingStores(t, tracingBackend, profileBackend)

		err := saveAutotracingCPUEvent(
			&tracing.WriteRequest{
				TracerName:    "cpusys",
				TracerTime:    time.Unix(1700000000, 0).UTC(),
				TracerData:    map[string]any{"flamedata": "preserved"},
				TracerRunType: tracing.TracerRunTypeAutotracing,
			},
			time.Second,
			[]flamegraph.FrameData{
				{Level: 0, Value: 1, Self: -1, Label: "invalid"},
			},
		)
		if err == nil || !strings.Contains(err.Error(), "self value") {
			t.Fatalf("error = %v, want negative self error", err)
		}
		if len(tracingBackend.records) != 1 {
			t.Fatalf(
				"tracing records = %d, want 1 after conversion failure",
				len(tracingBackend.records),
			)
		}
		if len(profileBackend.records) != 0 {
			t.Fatalf(
				"profile records = %d, want 0 for invalid profile",
				len(profileBackend.records),
			)
		}
	})

	t.Run("pprof backend fails", func(t *testing.T) {
		saveErr := errors.New("pprof backend unavailable")
		tracingBackend := &captureProfileBackend{}
		profileBackend := &captureProfileBackend{saveErr: saveErr}
		configureAutotracingStores(t, tracingBackend, profileBackend)

		err := saveAutotracingCPUEvent(
			&tracing.WriteRequest{
				TracerName:    "cpusys",
				TracerTime:    time.Unix(1700000000, 0).UTC(),
				TracerData:    map[string]any{"flamedata": "preserved"},
				TracerRunType: tracing.TracerRunTypeAutotracing,
			},
			time.Second,
			validAutotracingFrames(),
		)
		if !errors.Is(err, saveErr) {
			t.Fatalf("error = %v, want pprof backend error", err)
		}
		if len(tracingBackend.records) != 1 {
			t.Fatalf(
				"tracing records = %d, want 1 after pprof failure",
				len(tracingBackend.records),
			)
		}
	})
}

func configureAutotracingStores(
	t *testing.T,
	tracingBackend *captureProfileBackend,
	profileBackend *captureProfileBackend,
) {
	t.Helper()
	tracingStore, err := storage.NewStore[*tracing.Document](
		context.Background(),
		"capture-json",
		tracingBackend,
		tracing.DocumentCollection,
		tracing.DocumentStoreMapper{},
	)
	if err != nil {
		t.Fatalf("new tracing store: %v", err)
	}
	profileStore, err := storage.NewStore[*tracing.Document](
		context.Background(),
		"capture-pprof",
		profileBackend,
		profiler.MetadataCollection,
		captureProfileMapper{},
	)
	if err != nil {
		t.Fatalf("new profile store: %v", err)
	}
	options := tracing.DocumentOptions{Hostname: "test-host", Region: "test"}
	tracing.SetTracingStore(
		[]*storage.Store[*tracing.Document]{tracingStore},
		options,
	)
	tracing.SetProfileStore(
		[]*storage.Store[*tracing.Document]{profileStore},
		options,
	)
	t.Cleanup(func() {
		tracing.SetTracingStore(nil, tracing.DocumentOptions{})
		tracing.SetProfileStore(nil, tracing.DocumentOptions{})
	})
}

func validAutotracingFrames() []flamegraph.FrameData {
	return []flamegraph.FrameData{
		{Level: 0, Value: 1, Self: 1, Label: "root"},
	}
}

func assertProfileField(
	t *testing.T,
	record driver.Record,
	field string,
	want string,
) {
	t.Helper()
	if got := record.Fields[field]; got != want {
		t.Errorf("%s = %v, want %q", field, got, want)
	}
}

type captureProfileMapper struct {
	tracing.ProfileDocumentStoreMapper
}

func (captureProfileMapper) Encode(document *tracing.Document) ([]byte, error) {
	data, err := captureProfileData(document)
	if err != nil {
		return nil, err
	}
	return data.Profile.MarshalVT()
}

func (captureProfileMapper) Fields(
	document *tracing.Document,
) (map[string]any, error) {
	fields, err := (tracing.ProfileDocumentStoreMapper{}).Fields(document)
	if err != nil {
		return nil, err
	}
	data, err := captureProfileData(document)
	if err != nil {
		return nil, err
	}

	start := time.Unix(0, data.Profile.TimeNanos).UTC()
	fields["profile_type"] = data.ProfileType
	fields["profile_start_time"] = start
	fields["profile_end_time"] = start.Add(
		time.Duration(data.Profile.DurationNanos),
	)
	return fields, nil
}

func captureProfileData(
	document *tracing.Document,
) (*profiler.ProfileData, error) {
	data, ok := document.TracerData.(*profctx.TracerData)
	if !ok || data == nil || data.FlameData == nil {
		return nil, errors.New("capture profile mapper: profile data is missing")
	}
	return data.FlameData, nil
}

type captureProfileBackend struct {
	records []driver.Record
	saveErr error
}

func (*captureProfileBackend) Init(
	context.Context,
	string,
	[]driver.Index,
) error {
	return nil
}

func (b *captureProfileBackend) Save(
	_ context.Context,
	record driver.Record,
) error {
	b.records = append(b.records, record)
	return b.saveErr
}

func (*captureProfileBackend) Get(
	context.Context,
	string,
) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupportedOp
}

func (*captureProfileBackend) Delete(context.Context, string) error {
	return driver.ErrUnsupportedOp
}

func (*captureProfileBackend) Query(
	context.Context,
	driver.Query,
) ([]driver.Record, error) {
	return nil, driver.ErrUnsupportedOp
}

func (*captureProfileBackend) Count(
	context.Context,
	driver.Query,
) (int64, error) {
	return 0, driver.ErrUnsupportedOp
}

func (*captureProfileBackend) Values(
	context.Context,
	string,
	driver.Query,
	int,
) ([]string, error) {
	return nil, driver.ErrUnsupportedOp
}

func (*captureProfileBackend) Close(context.Context) error {
	return nil
}
