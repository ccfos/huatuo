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

package tracing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/timeutil"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestEnableDocumentWriterRequiresDependencies(t *testing.T) {
	tests := []struct {
		name      string
		store     *tracingstore.Store
		documents *document.Builder
		want      string
	}{
		{
			name:      "missing store",
			documents: document.New(""),
			want:      "store is required",
		},
		{
			name:  "missing document builder",
			store: newTracingStore(t),
			want:  "document builder is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := EnableDocumentWriter(test.store, test.documents)
			if err == nil {
				t.Fatal("EnableDocumentWriter() error = nil")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EnableDocumentWriter() error = %q, want containing %q", err, test.want)
			}
		})
	}
}

func TestDisableDocumentWriter(t *testing.T) {
	DisableDocumentWriter()
	t.Cleanup(DisableDocumentWriter)

	if err := EnableDocumentWriter(newTracingStore(t), document.New("")); err != nil {
		t.Fatalf("EnableDocumentWriter() error = %v", err)
	}
	if configuredWriter.Load() == nil {
		t.Fatal("configured writer = nil after EnableDocumentWriter()")
	}

	DisableDocumentWriter()
	if configuredWriter.Load() != nil {
		t.Fatal("configured writer is still set after DisableDocumentWriter()")
	}
}

func TestSaveContextPublishesObservedEvent(t *testing.T) {
	DisableDocumentWriter()
	t.Cleanup(DisableDocumentWriter)
	store := newTracingStore(t)
	if err := EnableDocumentWriter(store, document.New("health-region")); err != nil {
		t.Fatal(err)
	}
	documents, unsubscribe := store.Subscribe()
	defer unsubscribe()
	observed := timeutil.Timestamp{Time: time.Unix(123, 456)}
	payload := map[string]any{"type": "block_error"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := SaveContext(ctx, &WriteRequest{
		TracerName:        "io_health",
		ObservedTimestamp: observed,
		TracerData:        payload,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-documents:
		if event.TracerName != "io_health" || event.Region != "health-region" ||
			event.TracerRunType != types.TracerRunTypeEvent ||
			event.ObservedTimestamp == nil || !event.ObservedTimestamp.Equal(observed.Time) ||
			event.StartedTimestamp != nil || event.TracerID == "" {
			t.Fatalf("observed event metadata = %+v", event.Document)
		}
		if event.TracerData.(map[string]any)["type"] != "block_error" {
			t.Fatalf("event payload = %+v", event.TracerData)
		}
	case <-time.After(time.Second):
		t.Fatal("observed event was not published")
	}
}

func newTracingStore(t *testing.T) *tracingstore.Store {
	t.Helper()
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{})
	if err != nil {
		t.Fatalf("tracingstore.NewFromConfig() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(t.Context()); err != nil {
			t.Errorf("Store.Close() error = %v", err)
		}
	})
	return store
}
