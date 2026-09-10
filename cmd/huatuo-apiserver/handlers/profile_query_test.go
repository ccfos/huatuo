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
	"errors"
	"testing"

	serverapi "github.com/ccfos/huatuo/apis/v1/server"
	"github.com/ccfos/huatuo/internal/auth"
	profilequery "github.com/ccfos/huatuo/internal/profiling/query"
	"github.com/ccfos/huatuo/internal/server/response"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"google.golang.org/protobuf/proto"
)

type stubProfileQueryService struct {
	selectRequest *querierv1.SelectMergeStacktracesRequest
	err           error
}

func (s *stubProfileQueryService) SelectMergeStacktraces(
	_ context.Context,
	request *querierv1.SelectMergeStacktracesRequest,
) (*querierv1.SelectMergeStacktracesResponse, error) {
	s.selectRequest = request
	return &querierv1.SelectMergeStacktracesResponse{}, s.err
}

func (*stubProfileQueryService) ProfileTypes(
	context.Context,
	*querierv1.ProfileTypesRequest,
) (*querierv1.ProfileTypesResponse, error) {
	return &querierv1.ProfileTypesResponse{}, nil
}

func (*stubProfileQueryService) LabelNames(
	context.Context,
	*typesv1.LabelNamesRequest,
) (*typesv1.LabelNamesResponse, error) {
	return &typesv1.LabelNamesResponse{}, nil
}

func (*stubProfileQueryService) LabelValues(
	context.Context,
	*typesv1.LabelValuesRequest,
) (*typesv1.LabelValuesResponse, error) {
	return &typesv1.LabelValuesResponse{}, nil
}

func profileQueryContext(t *testing.T, isAdmin bool) context.Context {
	t.Helper()
	return auth.WithPrincipal(t.Context(), auth.Principal{ID: "user-1", IsAdmin: isAdmin})
}

func TestSelectMergeStacktracesUsesStrictProtobufContract(t *testing.T) {
	service := &stubProfileQueryService{}
	handler := &APIHandler{profileQuery: service}
	body, err := proto.Marshal(&querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		LabelSelector: `{hostname="node-1"}`,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := handler.SelectMergeStacktraces(
		profileQueryContext(t, true),
		serverapi.SelectMergeStacktracesRequestObject{Body: bytes.NewReader(body)},
	)
	if err != nil {
		t.Fatalf("SelectMergeStacktraces() error = %v", err)
	}
	if _, ok := result.(serverapi.SelectMergeStacktraces200ApplicationProtoResponse); !ok {
		t.Fatalf("SelectMergeStacktraces() response = %T", result)
	}
	if service.selectRequest == nil || service.selectRequest.LabelSelector != `{hostname="node-1"}` {
		t.Fatalf("profile query request = %+v", service.selectRequest)
	}
}

func TestSelectMergeStacktracesRequiresAdmin(t *testing.T) {
	handler := &APIHandler{profileQuery: &stubProfileQueryService{}}
	_, err := handler.SelectMergeStacktraces(
		profileQueryContext(t, false),
		serverapi.SelectMergeStacktracesRequestObject{Body: bytes.NewReader(nil)},
	)
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "permission_denied" {
		t.Fatalf("SelectMergeStacktraces() error = %v", err)
	}
}

func TestSelectMergeStacktracesMapsProfileAbsence(t *testing.T) {
	handler := &APIHandler{profileQuery: &stubProfileQueryService{
		err: profilequery.ErrProfilesAbsent,
	}}
	_, err := handler.SelectMergeStacktraces(
		profileQueryContext(t, true),
		serverapi.SelectMergeStacktracesRequestObject{Body: bytes.NewReader(nil)},
	)
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("SelectMergeStacktraces() error = %v", err)
	}
}
