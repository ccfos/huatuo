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

package sqlite

import (
	"context"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func benchmarkSQLiteSave(b *testing.B) {
	storage, err := NewBackend("file::memory:?cache=shared")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = storage.Close(context.Background()) }()

	if err := storage.Init(context.Background(), "bench_events", nil); err != nil {
		b.Fatal(err)
	}

	rec := driver.Record{
		ID:   "bench-id",
		Data: []byte(`{"k":1}`),
		Fields: map[string]any{
			"hostname": "node-1",
			"tracer":   "bench",
		},
	}
	ctx := context.Background()
	opts := driver.SaveOptions{Mode: driver.SaveModeUpsert}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := storage.Save(ctx, rec, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLiteSave(b *testing.B) { benchmarkSQLiteSave(b) }
