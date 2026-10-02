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

package node

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestGeneratedContractsCompile(t *testing.T) {
	t.Parallel()

	if _, err := NewClient("http://127.0.0.1:8080"); err != nil {
		t.Errorf("NewClient() error = %v, want nil", err)
	}
	var _ StrictServerInterface = (*unimplementedStrictServer)(nil)
}

func TestNodeHTTPStatusForErrorCode(t *testing.T) {
	t.Parallel()

	status, ok := HTTPStatusForErrorCode(ErrorCodeOperationNotFound)
	if status != http.StatusNotFound || !ok {
		t.Errorf("HTTPStatusForErrorCode(operation_not_found) = (%d, %t), want (404, true)", status, ok)
	}
	status, ok = HTTPStatusForErrorCode(apiv1.ErrorCodeInvalidRequest)
	if status != http.StatusBadRequest || !ok {
		t.Errorf("HTTPStatusForErrorCode(invalid_request) = (%d, %t), want (400, true)", status, ok)
	}
	status, ok = HTTPStatusForErrorCode(ErrorCodeContainerNotFound)
	if status != http.StatusNotFound || !ok {
		t.Errorf("HTTPStatusForErrorCode(container_not_found) = (%d, %t), want (404, true)", status, ok)
	}
	status, ok = HTTPStatusForErrorCode(ErrorCodeEventStreamLimitExceeded)
	if status != http.StatusTooManyRequests || !ok {
		t.Errorf("HTTPStatusForErrorCode(event_stream_limit_exceeded) = (%d, %t), want (429, true)", status, ok)
	}
}

func TestRemovedNodeRoutesAreNotRegistered(t *testing.T) {
	t.Parallel()

	router := gin.New()
	RegisterHandlers(router, NewStrictHandler(&unimplementedStrictServer{}, nil))

	for _, path := range []string{
		"/config",
		"/healthz",
		"/tasks",
		"/tasks/job-1",
		"/tracers",
		"/tracers/dropwatch",
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

func (*unimplementedStrictServer) UpdateConfig(
	context.Context,
	UpdateConfigRequestObject,
) (UpdateConfigResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) WatchEvents(
	context.Context,
	WatchEventsRequestObject,
) (WatchEventsResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) GetContainer(
	context.Context,
	GetContainerRequestObject,
) (GetContainerResponseObject, error) {
	return nil, errNotImplemented
}

func (*unimplementedStrictServer) StartOperation(
	context.Context,
	StartOperationRequestObject,
) (StartOperationResponseObject, error) {
	return nil, nil
}

func (*unimplementedStrictServer) GetOperation(
	context.Context,
	GetOperationRequestObject,
) (GetOperationResponseObject, error) {
	return nil, nil
}

func (*unimplementedStrictServer) StopOperation(
	context.Context,
	StopOperationRequestObject,
) (StopOperationResponseObject, error) {
	return nil, nil
}

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
