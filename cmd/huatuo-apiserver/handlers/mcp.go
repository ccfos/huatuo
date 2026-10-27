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
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	serverapi "github.com/ccfos/huatuo/apis/v1/server"
)

// mcpResponse lets the protocol transport own headers and streaming writes
// rather than buffering them in the generated response serializer.
type mcpResponse struct {
	handler http.Handler
	request *http.Request
}

func (r mcpResponse) serve(w http.ResponseWriter) error {
	if r.handler == nil {
		http.NotFound(w, r.request)
		return nil
	}
	r.handler.ServeHTTP(w, r.request)
	return nil
}

func (r mcpResponse) VisitHandleMCPResponse(w http.ResponseWriter) error {
	return r.serve(w)
}

func (r mcpResponse) VisitStreamMCPResponse(w http.ResponseWriter) error {
	return r.serve(w)
}

func (r mcpResponse) VisitDeleteMCPResponse(w http.ResponseWriter) error {
	return r.serve(w)
}

func (h *APIHandler) mcpResponse(ctx context.Context) (mcpResponse, error) {
	ginContext, ok := ctx.(*gin.Context)
	if !ok {
		return mcpResponse{}, errors.New("dispatch MCP: generated Gin request context is required")
	}
	return mcpResponse{handler: h.mcp, request: ginContext.Request.Clone(ginContext.Request.Context())}, nil
}

func (h *APIHandler) HandleMCP(ctx context.Context, request serverapi.HandleMCPRequestObject) (serverapi.HandleMCPResponseObject, error) {
	response, err := h.mcpResponse(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body != nil {
		// The generated adapter consumed the body; RawMessage preserves JSON-RPC IDs.
		response.request.Body = io.NopCloser(bytes.NewReader(*request.Body))
		response.request.ContentLength = int64(len(*request.Body))
	}
	return response, nil
}

func (h *APIHandler) StreamMCP(ctx context.Context, _ serverapi.StreamMCPRequestObject) (serverapi.StreamMCPResponseObject, error) {
	return h.mcpResponse(ctx)
}

func (h *APIHandler) DeleteMCP(ctx context.Context, _ serverapi.DeleteMCPRequestObject) (serverapi.DeleteMCPResponseObject, error) {
	return h.mcpResponse(ctx)
}
