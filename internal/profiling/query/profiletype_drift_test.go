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

package query

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"

	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	"github.com/ccfos/huatuo/pkg/types"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
)

// queryRecordingBackend records the filters of every executed query so a
// test can prove which profile_type actually filtered the search.
type queryRecordingBackend struct {
	mu      sync.Mutex
	records []driver.Record
	queries []driver.Query
}

func (m *queryRecordingBackend) Init(context.Context, string, []driver.Index) error {
	return nil
}

func (m *queryRecordingBackend) Save(_ context.Context, rec driver.Record, _ driver.SaveOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, rec)
	return nil
}

func (m *queryRecordingBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (m *queryRecordingBackend) Delete(context.Context, string) error { return nil }

func (m *queryRecordingBackend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, nil
}

func (m *queryRecordingBackend) Query(_ context.Context, q driver.Query, consume func([]driver.Record) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries = append(m.queries, q)
	var out []driver.Record
	for _, r := range m.records {
		match := true
		for _, f := range q.Filters {
			field := strings.TrimSuffix(f.Field, ".keyword")
			v, ok := r.Fields[field]
			if !ok {
				match = false
				break
			}
			if f.Op == driver.OpNotExists {
				// ES indexes the mapper's JSON body, where empty strings are
				// omitted (omitempty); absent there is what not_exists means.
				value, present := r.Fields[field]
				match = !present || value == nil || value == ""
				if !match {
					break
				}
				continue
			}
			if field == types.DocumentFieldUploadedTimestamp {
				bound, err := time.Parse(time.RFC3339Nano, fmt.Sprint(f.Value))
				if err != nil {
					match = false
					break
				}
				stored, ok := v.(timeutil.Timestamp)
				if !ok {
					match = false
					break
				}
				switch f.Op {
				case driver.OpGte:
					match = !stored.Time.Before(bound)
				case driver.OpLte:
					match = !stored.Time.After(bound)
				default:
					match = false
				}
				if !match {
					break
				}
				continue
			}
			if fmt.Sprint(v) != fmt.Sprint(f.Value) {
				match = false
				break
			}
		}
		if match {
			out = append(out, r)
		}
	}
	return consume(out)
}

func (*queryRecordingBackend) Count(context.Context, driver.Query) (int64, error) {
	return 0, nil
}

func (*queryRecordingBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*queryRecordingBackend) Close(context.Context) error { return nil }

func fmtSprint(v any) string { return fmt.Sprint(v) }

func memProfileDoc(tracerID, hostname, profileType, sampleTypeName string) *profilingstore.Document {
	started := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return &profilingstore.Document{
		Document: types.Document{
			Hostname:         hostname,
			Region:           "r1",
			StartedTimestamp: &timeutil.Timestamp{Time: started},
			TracerName:       "profiling",
			TracerID:         tracerID,
			TracerRunType:    types.TracerRunTypeProfiling,
		},
		ProfileData: &profilingstore.ProfileData{
			ProfileType: profileType,
			Profile: &profilev1.Profile{
				SampleType: []*profilev1.ValueType{{Type: 1, Unit: 2}},
				PeriodType: &profilev1.ValueType{Type: 1, Unit: 2},
				Sample: []*profilev1.Sample{{
					LocationId: []uint64{1},
					Value:      []int64{42},
				}},
				Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
				Function:    []*profilev1.Function{{Id: 1, Name: 3}},
				StringTable: []string{"", sampleTypeName, "bytes", "main"},
			},
		},
	}
}

func newDriftTestService(t *testing.T) (*ProfileQueryService, *queryRecordingBackend) {
	t.Helper()
	backend := &queryRecordingBackend{}
	persistence, err := storage.NewStore[*profilingstore.Document](
		context.Background(), "memory", backend,
		profilingstore.Collection, profilingstore.NewMapperForTest(),
	)
	if err != nil {
		t.Fatalf("profile store: %v", err)
	}
	store := profilingstore.NewStoreFromPersistence(persistence)
	if err != nil {
		t.Fatalf("profile store: %v", err)
	}
	if err := store.SaveSync(context.Background(), memProfileDoc(
		"task-mem-1", "node-a",
		"process_mem:memory:bytes:space:bytes", "memory",
	)); err != nil {
		t.Fatalf("seed memory doc: %v", err)
	}
	svc, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatalf("NewProfileQueryService: %v", err)
	}
	return svc, backend
}

// TestConsistentProfileTypeRequestWorks is the control: the request's
// ProfileTypeID and the __profile_type__ label agree.
func TestConsistentProfileTypeRequestWorks(t *testing.T) {
	svc, _ := newDriftTestService(t)
	resp, err := svc.SelectMergeStacktraces(context.Background(), &querierv1.SelectMergeStacktracesRequest{
		Start:         0,
		End:           time.Now().Add(24 * time.Hour).UnixMilli(),
		ProfileTypeID: "process_mem:memory:bytes:space:bytes",
		LabelSelector: `{hostname="node-a", __profile_type__="process_mem:memory:bytes:space:bytes"}`,
	})
	if err != nil {
		t.Fatalf("SelectMergeStacktraces(consistent) error = %v", err)
	}
	if resp == nil || resp.Flamegraph == nil {
		t.Fatalf("SelectMergeStacktraces(consistent) = %v, want flamegraph", resp)
	}
}

// TestProfileTypeLabelAndRequestIDDisagree: the search filter uses the
// __profile_type__ label value, so the sample-type axis must be derived from
// the same (post-matcher) value — not from the pre-override ProfileTypeID.
func TestProfileTypeLabelAndRequestIDDisagree(t *testing.T) {
	svc, backend := newDriftTestService(t)
	_, err := svc.SelectMergeStacktraces(context.Background(), &querierv1.SelectMergeStacktracesRequest{
		Start:         0,
		End:           time.Now().Add(24 * time.Hour).UnixMilli(),
		ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		LabelSelector: `{hostname="node-a", __profile_type__="process_mem:memory:bytes:space:bytes"}`,
	})

	backend.mu.Lock()
	var lastProfileTypeFilter any
	for _, q := range backend.queries {
		for _, f := range q.Filters {
			if strings.Contains(f.Field, "profile_type") {
				lastProfileTypeFilter = f.Value
			}
		}
	}
	backend.mu.Unlock()
	if lastProfileTypeFilter != "process_mem:memory:bytes:space:bytes" {
		t.Fatalf("search filter profile_type = %v, want the __profile_type__ label value", lastProfileTypeFilter)
	}

	// The search targeted memory profiles (which exist, sample type
	// "memory"); the service must not fail with a missing sample type
	// "cpu" — that failure is the drift: axis from ProfileTypeID while
	// the filter came from the label.
	if err != nil && strings.Contains(err.Error(), `sample type "cpu" not found`) {
		t.Fatalf("profile-type drift: %v (filter used the label value, sample axis used ProfileTypeID)", err)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
