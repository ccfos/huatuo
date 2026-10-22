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

package store

import (
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/storage/sqlite"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

// Only timestamp indexes are needed to exercise time selection across the
// mapper and a real database, independent of unrelated nested-field indexes.
type timeQueryMapper struct{ mapper }

func (timeQueryMapper) Indexes() []driver.Index {
	return []driver.Index{{Field: types.DocumentFieldStartedTimestamp}}
}

func TestTimeQueriesSelectCollectionInsteadOfUpload(t *testing.T) {
	backend, err := sqlite.NewBackend(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := storage.NewStore[*Document](t.Context(), "sqlite", backend, Collection, timeQueryMapper{})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{store: persistence}
	t.Cleanup(func() {
		if err := store.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})

	collected := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	uploaded := collected.Add(2 * time.Minute)
	document := validDocument()
	document.StartedTimestamp = &timeutil.Timestamp{Time: collected}
	document.UploadedTimestamp = timeutil.Timestamp{Time: uploaded}
	// Persist explicit historical timestamps; SaveSync intentionally stamps ingestion now.
	if err := persistence.Save(t.Context(), document, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		start, end time.Time
		want       int
	}{
		{"collection start inclusive", collected, collected.Add(time.Second), 1},
		{"collection end inclusive", collected.Add(-time.Second), collected, 1},
		{"collection point", collected, collected, 1},
		{"upload period", uploaded.Add(-time.Second), uploaded.Add(time.Second), 0},
		{"after collection start", collected.Add(time.Second), collected.Add(time.Minute), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := &Filter{StartTime: tc.start, EndTime: tc.end, Limit: 10}
			count := 0
			if err := store.Search(t.Context(), filter, func(batch []*Document) error {
				count += len(batch)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if count != tc.want {
				t.Fatalf("Search records = %d, want %d", count, tc.want)
			}
			values, err := store.Values(t.Context(), filter, "id")
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != tc.want {
				t.Fatalf("Values = %v, want %d values", values, tc.want)
			}
			if tc.want > 0 && values[0] != document.TracerID {
				t.Fatalf("Values = %v, want task ID %s", values, document.TracerID)
			}
		})
	}
}
