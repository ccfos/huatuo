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

package elasticsearch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	esclient "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esutil"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestBulkSaveRetainsOriginalPayload(t *testing.T) {
	indexer := &benchmarkBulkIndexer{}
	backend := Storage{index: "profiles", bulk: indexer}
	payload := []byte(`{"value":"original"}`)
	if err := backend.Save(t.Context(), driver.Record{ID: "one", Data: payload}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	copy(payload, `{"value":"modified"}`)
	queued, err := io.ReadAll(indexer.item.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(queued) != `{"value":"original"}` {
		t.Fatalf("queued payload changed after Save returned: %s", queued)
	}
}

type blockedBulkBody struct {
	reader  *bytes.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedBulkBody) Seek(offset int64, whence int) (int64, error) {
	return b.reader.Seek(offset, whence)
}

func (b *blockedBulkBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.reader.Read(p)
}

func TestBulkSavePayloadOwnershipOverHTTP(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read bulk request", http.StatusBadRequest)
			return
		}
		received <- string(body)
		_, _ = io.WriteString(w, `{"errors":false,"items":[{"index":{"_id":"blocker","status":201}},{"index":{"_id":"one","status":201}}]}`)
	}))
	defer server.Close()
	client, err := newCompatClient(&esclient.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	indexer, err := esutil.NewBulkIndexer(esutil.BulkIndexerConfig{Client: client, Index: "profiles", NumWorkers: 1, FlushBytes: 1 << 20, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	blocker := &blockedBulkBody{reader: bytes.NewReader([]byte(`{}`)), entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	closed := false
	defer func() {
		if !released {
			close(blocker.release)
		}
		if !closed {
			if err := indexer.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := indexer.Add(t.Context(), esutil.BulkIndexerItem{Action: "index", DocumentID: "blocker", Body: blocker}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("bulk worker did not enter blocked body")
	}
	backend := Storage{index: "profiles", bulk: indexer}
	payload := []byte(`{"value":"original"}`)
	if err := backend.Save(t.Context(), driver.Record{ID: "one", Data: payload}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	copy(payload, `{"value":"modified"}`)
	close(blocker.release)
	released = true
	// Closing drains the queued item and performs the HTTP bulk request.
	closed = true
	if err := backend.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		if !strings.Contains(body, `{"value":"original"}`) || strings.Contains(body, `{"value":"modified"}`) {
			t.Fatalf("bulk HTTP request contains mutated payload: %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bulk HTTP request was not delivered")
	}
}

func BenchmarkBulkSavePayload(b *testing.B) {
	for _, size := range []int{1024, 64 << 10} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			indexer := &benchmarkBulkIndexer{}
			backend := Storage{index: "profiles", bulk: indexer}
			payload := []byte(`{"value":"` + strings.Repeat("x", size) + `"}`)
			record := driver.Record{ID: "one", Data: payload}
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				if err := backend.Save(b.Context(), record, driver.SaveOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
