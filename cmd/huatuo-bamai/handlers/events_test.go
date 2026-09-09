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

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	nodecloudevents "huatuo-bamai/internal/nodeagent/cloudevents"
	"huatuo-bamai/internal/server/response"
	tracingstore "huatuo-bamai/pkg/tracing/store"
	"huatuo-bamai/pkg/types"
)

func TestWatchEventsRejectsInvalidFilter(t *testing.T) {
	handler := newTestNodeAPIHandler(t, time.Second)
	invalidPattern := "[invalid"
	_, err := handler.WatchEvents(t.Context(), nodeapi.WatchEventsRequestObject{
		Body: &nodeapi.WatchEventsJSONRequestBody{
			Filters: &nodeapi.WatchEventFilters{TracerName: &invalidPattern},
		},
	})
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != apiv1.ErrorCodeInvalidRequest {
		t.Fatalf("WatchEvents() error = %v, want invalid_request", err)
	}
}

func TestWatchEventsWritesHeartbeat(t *testing.T) {
	handler := newTestNodeAPIHandler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := handler.WatchEvents(ctx, nodeapi.WatchEventsRequestObject{
		Body: &nodeapi.WatchEventsJSONRequestBody{},
	})
	if err != nil {
		t.Fatalf("WatchEvents() error = %v", err)
	}
	writer := &cancelingResponseWriter{header: make(http.Header), cancel: cancel}
	if err := stream.VisitWatchEventsResponse(writer); err != nil {
		t.Fatalf("VisitWatchEventsResponse() error = %v", err)
	}

	if writer.status != http.StatusOK {
		t.Errorf("status = %d, want 200", writer.status)
	}
	if got := writer.header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := writer.header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := writer.header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
	if got := writer.body.String(); !strings.Contains(got, ": ping\n") {
		t.Errorf("body = %q, want heartbeat", got)
	}
}

func TestWatchEventsWritesCloudEvent(t *testing.T) {
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{})
	if err != nil {
		t.Fatalf("tracingstore.NewFromConfig() error = %v", err)
	}
	cloudEvents, err := nodecloudevents.New(store, 1)
	if err != nil {
		t.Fatalf("cloudevents.New() error = %v", err)
	}
	handler := &NodeAPIHandler{
		cloudEvents:       cloudEvents,
		keepAliveInterval: time.Hour,
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := handler.WatchEvents(ctx, nodeapi.WatchEventsRequestObject{
		Body: &nodeapi.WatchEventsJSONRequestBody{},
	})
	if err != nil {
		t.Fatalf("WatchEvents() error = %v", err)
	}

	observedTimestamp := time.Unix(1_700_000_000, 0).UTC()
	if err := store.Save(&tracingstore.Document{Document: types.Document{
		Hostname:          "node-1",
		Region:            "cn",
		ObservedTimestamp: &observedTimestamp,
		TracerName:        "cpu",
		TracerRunType:     types.TracerRunTypeEvent,
	}}); err != nil {
		t.Fatalf("Store.Save() error = %v", err)
	}

	writer := &cancelingResponseWriter{header: make(http.Header), cancel: cancel}
	if err := stream.VisitWatchEventsResponse(writer); err != nil {
		t.Fatalf("VisitWatchEventsResponse() error = %v", err)
	}
	payload, ok := strings.CutPrefix(
		strings.TrimSuffix(writer.body.String(), "\n\n"),
		"data: ",
	)
	if !ok {
		t.Fatalf("body = %q, want an SSE data field", writer.body.String())
	}
	var event nodeapi.WatchEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("unmarshal CloudEvent: %v", err)
	}
	if event.SpecVersion != nodeapi.WatchEventSpecVersion10 ||
		event.Type != "tech.huatuo.kernel.event" ||
		event.DataContentType != "application/json" {
		t.Errorf("CloudEvent envelope = %+v", event)
	}
	if event.Data.Hostname != "node-1" || event.Data.TracerName == nil ||
		*event.Data.TracerName != "cpu" {
		t.Errorf("CloudEvent data = %+v", event.Data)
	}
}

type cancelingResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
	cancel context.CancelFunc
}

func (w *cancelingResponseWriter) Header() http.Header {
	return w.header
}

func (w *cancelingResponseWriter) Write(data []byte) (int, error) {
	return w.body.Write(data)
}

func (w *cancelingResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *cancelingResponseWriter) Flush() {
	w.cancel()
}

func newTestCloudEventsService(t *testing.T, maxSubscriptions int) *nodecloudevents.Service {
	t.Helper()
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{})
	if err != nil {
		t.Fatalf("tracingstore.NewFromConfig() error = %v", err)
	}
	service, err := nodecloudevents.New(store, maxSubscriptions)
	if err != nil {
		t.Fatalf("cloudevents.New() error = %v", err)
	}
	return service
}
