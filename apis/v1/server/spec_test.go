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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
)

func TestOpenAPIJSON(t *testing.T) {
	t.Parallel()

	first := OpenAPIJSON()
	if len(first) == 0 {
		t.Fatal("OpenAPIJSON() returned an empty document")
	}
	first[0] = 0
	if OpenAPIJSON()[0] == 0 {
		t.Fatal("OpenAPIJSON() returned mutable package state")
	}

	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(OpenAPIJSON())
	if err != nil {
		t.Fatalf("LoadFromData(OpenAPIJSON()) error = %v", err)
	}
	if err := document.Validate(t.Context()); err != nil {
		t.Errorf("Validate(OpenAPIJSON()) error = %v", err)
	}
}

// TestProfilingAndTracingJobSchemasAcceptServerOutput guards against the
// schemas becoming unsatisfiable again. The server always emits the Job fields
// plus the profiling/tracing extras (see cmd/huatuo-apiserver/handlers/api.go),
// so a realistic instance of the generated structs must validate against the
// served document's ProfilingJob / TracingJob schemas.
func TestProfilingAndTracingJobSchemasAcceptServerOutput(t *testing.T) {
	t.Parallel()

	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(OpenAPIJSON())
	if err != nil {
		t.Fatalf("LoadFromData(OpenAPIJSON()) error = %v", err)
	}

	createdAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	instances := map[string]any{
		"ProfilingJob": mustJSON(t, ProfilingJob{
			RequestID:       "abc",
			Hostname:        "h1",
			DurationSeconds: 60,
			Scope:           apiv1.ObservationScope("host"),
			Status:          JobStatus("pending"),
			CreatedAt:       createdAt,
			UpdatedAt:       createdAt,
			Type:            ProfilingType("cpu"),
			Language:        ProfilingLanguage("go"),
			Mode:            ProfilingMode("oncpu"),
		}),
		"TracingJob": mustJSON(t, TracingJob{
			RequestID:       "abc",
			Hostname:        "h1",
			DurationSeconds: 60,
			Scope:           apiv1.ObservationScope("host"),
			Status:          JobStatus("pending"),
			CreatedAt:       createdAt,
			UpdatedAt:       createdAt,
			Type:            TracingType("networking_drop"),
		}),
	}

	for name, instance := range instances {
		schema := document.Components.Schemas[name]
		if schema == nil {
			t.Fatalf("schema %q not found in served document", name)
		}
		if err := schema.Value.VisitJSON(instance); err != nil {
			t.Errorf("%s schema rejects the server-emitted body: %v", name, err)
		}
	}
}

func mustJSON(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	return out
}

func TestGeneratedContractsCompile(t *testing.T) {
	t.Parallel()

	if _, err := NewClient("http://127.0.0.1:8080"); err != nil {
		t.Errorf("NewClient() error = %v, want nil", err)
	}
	var _ StrictServerInterface = (*unimplementedStrictServer)(nil)
}

func TestCapabilitiesRouteIsNotCapturedAsRequestID(t *testing.T) {
	t.Parallel()

	handler := &routingStrictServer{}
	router := gin.New()
	RegisterHandlers(router, NewStrictHandler(handler, nil))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/profiling/capabilities", http.NoBody)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Errorf("GET /v1/profiling/capabilities status = %d, want 200", recorder.Code)
	}
	if !handler.capabilitiesCalled {
		t.Error("GET /v1/profiling/capabilities did not call GetProfilingCapabilities")
	}
	if handler.jobCalled {
		t.Error("GET /v1/profiling/capabilities was captured as request_id")
	}
}

func TestServerHTTPStatusForErrorCode(t *testing.T) {
	t.Parallel()

	status, ok := HTTPStatusForErrorCode(ErrorCodeJobNotFound)
	if status != http.StatusNotFound || !ok {
		t.Errorf("HTTPStatusForErrorCode(job_not_found) = (%d, %t), want (404, true)", status, ok)
	}
	status, ok = HTTPStatusForErrorCode(apiv1.ErrorCodeUnauthenticated)
	if status != http.StatusUnauthorized || !ok {
		t.Errorf("HTTPStatusForErrorCode(unauthenticated) = (%d, %t), want (401, true)", status, ok)
	}
}

func TestRemovedServerRoutesAreNotRegistered(t *testing.T) {
	t.Parallel()

	router := gin.New()
	RegisterHandlers(router, NewStrictHandler(&unimplementedStrictServer{}, nil))
	for _, path := range []string{
		"/healthz",
		"/v1/profiles",
		"/v1/profiles/job-1",
		"/v1/traces",
		"/v1/traces/job-1",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, recorder.Code)
		}
	}
}

type unimplementedStrictServer struct{}

var errNotImplemented = errors.New("test handler is not implemented")

func (*unimplementedStrictServer) GetReadiness(
	context.Context,
	GetReadinessRequestObject,
) (GetReadinessResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetOpenAPI(
	context.Context,
	GetOpenAPIRequestObject,
) (GetOpenAPIResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) ListProfilingJobs(
	context.Context,
	ListProfilingJobsRequestObject,
) (ListProfilingJobsResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) CreateProfilingJob(
	context.Context,
	CreateProfilingJobRequestObject,
) (CreateProfilingJobResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetProfilingCapabilities(
	context.Context,
	GetProfilingCapabilitiesRequestObject,
) (GetProfilingCapabilitiesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) SelectMergeStacktraces(
	context.Context,
	SelectMergeStacktracesRequestObject,
) (SelectMergeStacktracesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetProfileTypes(
	context.Context,
	GetProfileTypesRequestObject,
) (GetProfileTypesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetProfileLabelNames(
	context.Context,
	GetProfileLabelNamesRequestObject,
) (GetProfileLabelNamesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetProfileLabelValues(
	context.Context,
	GetProfileLabelValuesRequestObject,
) (GetProfileLabelValuesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetProfilingJob(
	context.Context,
	GetProfilingJobRequestObject,
) (GetProfilingJobResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetRawProfiles(
	context.Context,
	GetRawProfilesRequestObject,
) (GetRawProfilesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) StopProfilingJob(
	context.Context,
	StopProfilingJobRequestObject,
) (StopProfilingJobResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) ListTracingJobs(
	context.Context,
	ListTracingJobsRequestObject,
) (ListTracingJobsResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) CreateTracingJob(
	context.Context,
	CreateTracingJobRequestObject,
) (CreateTracingJobResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetTracingCapabilities(
	context.Context,
	GetTracingCapabilitiesRequestObject,
) (GetTracingCapabilitiesResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetTracingJob(
	context.Context,
	GetTracingJobRequestObject,
) (GetTracingJobResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) StopTracingJob(
	context.Context,
	StopTracingJobRequestObject,
) (StopTracingJobResponseObject, error) {
	return nil, errNotImplemented
}

type routingStrictServer struct {
	StrictServerInterface
	capabilitiesCalled bool
	jobCalled          bool
}

func (s *routingStrictServer) GetProfilingCapabilities(
	context.Context,
	GetProfilingCapabilitiesRequestObject,
) (GetProfilingCapabilitiesResponseObject, error) {
	s.capabilitiesCalled = true
	return GetProfilingCapabilities200JSONResponse{
		Data: ProfilingCapabilities{Items: []ProfilingCapability{}},
	}, nil
}

func (s *routingStrictServer) GetProfilingJob(
	context.Context,
	GetProfilingJobRequestObject,
) (GetProfilingJobResponseObject, error) {
	s.jobCalled = true
	return GetProfilingJob200JSONResponse{}, nil
}
