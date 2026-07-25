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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/pyroscope"
	"huatuo-bamai/pkg/tracing"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const pyroscopeE2EAddressEnvironment = "HUATUO_PYROSCOPE_E2E_URL"

func TestAutoTracingPyroscopeE2E(t *testing.T) {
	address := strings.TrimSpace(os.Getenv(pyroscopeE2EAddressEnvironment))
	if address == "" {
		t.Skipf("%s is not configured", pyroscopeE2EAddressEnvironment)
	}

	backend, err := pyroscope.NewBackend(&pyroscope.Config{Address: address})
	if err != nil {
		t.Fatalf("create Pyroscope backend: %v", err)
	}
	store, err := storage.NewStore[*tracing.Document](
		t.Context(),
		"pyroscope",
		backend,
		profiler.MetadataCollection,
		tracing.PprofDocumentStoreMapper{},
	)
	if err != nil {
		t.Fatalf("create Pyroscope profile store: %v", err)
	}
	tracing.SetProfileStore(
		[]*storage.Store[*tracing.Document]{store},
		tracing.DocumentOptions{Hostname: "issue327-e2e"},
	)
	t.Cleanup(func() {
		tracing.SetProfileStore(nil, tracing.DocumentOptions{})
		if err := store.Close(context.Background()); err != nil {
			t.Errorf("close Pyroscope store: %v", err)
		}
	})

	previousConfig := cfg
	Set(&Config{Display: DisplayConfig{
		Backend: string(DisplayBackendPyroscope),
	}})
	t.Cleanup(func() {
		Set(previousConfig)
	})

	start := time.Now().UTC().Add(-2 * time.Second)
	tracerID := fmt.Sprintf("issue327-e2e-%d", start.UnixNano())
	err = saveAutotracingCPUEvent(
		&tracing.WriteRequest{
			TracerName:    "cpusys",
			TracerID:      tracerID,
			TracerTime:    start,
			TracerData:    map[string]any{"source": "issue327-e2e"},
			TracerRunType: tracing.TracerRunTypeAutotracing,
		},
		time.Second,
		[]flamegraph.FrameData{
			{Level: 0, Value: 9, Self: 1, Label: "root"},
			{Level: 1, Value: 8, Self: 8, Label: "work"},
		},
	)
	if err != nil {
		t.Fatalf("save AutoTracing snapshot: %v", err)
	}

	matcher := fmt.Sprintf(
		`{profiling_scope="host",tracer_id=%q}`,
		tracerID,
	)
	deadline := time.Now().Add(2 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = assertPyroscopeSeriesAndFlamegraph(
			t.Context(),
			address,
			matcher,
		)
		if lastErr == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("query ingested AutoTracing profile: %v", lastErr)
}

func assertPyroscopeSeriesAndFlamegraph(
	ctx context.Context,
	address string,
	matcher string,
) error {
	series := &querierv1.SeriesResponse{}
	if err := postPyroscopeProto(
		ctx,
		address,
		"/querier.v1.QuerierService/Series",
		&querierv1.SeriesRequest{
			Matchers: []string{matcher},
			LabelNames: []string{
				"service_name",
				"tracer_id",
				profiler.LabelProfilingScope,
			},
		},
		series,
	); err != nil {
		return err
	}
	if len(series.LabelsSet) == 0 {
		return fmt.Errorf("Series returned no matching labels")
	}

	maxNodes := int64(1024)
	flamegraph := &querierv1.SelectMergeStacktracesResponse{}
	if err := postPyroscopeProto(
		ctx,
		address,
		"/querier.v1.QuerierService/SelectMergeStacktraces",
		&querierv1.SelectMergeStacktracesRequest{
			ProfileTypeID: profiler.ProfileTypeCpuSample,
			LabelSelector: matcher,
			MaxNodes:      &maxNodes,
		},
		flamegraph,
	); err != nil {
		return err
	}
	if flamegraph.Flamegraph == nil ||
		flamegraph.Flamegraph.Total <= 0 ||
		len(flamegraph.Flamegraph.Names) == 0 {
		return fmt.Errorf("SelectMergeStacktraces returned an empty flame graph")
	}
	return nil
}

func postPyroscopeProto(
	ctx context.Context,
	address string,
	endpoint string,
	request proto.Message,
	response proto.Message,
) error {
	body, err := protojson.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", endpoint, err)
	}
	requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		strings.TrimRight(address, "/")+endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create %s request: %w", endpoint, err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")

	httpResponse, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("send %s request: %w", endpoint, err)
	}
	defer httpResponse.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", endpoint, err)
	}
	if httpResponse.StatusCode < http.StatusOK ||
		httpResponse.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf(
			"%s returned status %d: %s",
			endpoint,
			httpResponse.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)
	}
	if err := (protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}).Unmarshal(responseBody, response); err != nil {
		return fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	return nil
}
