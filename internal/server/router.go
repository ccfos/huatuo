// Copyright 2025 The HuaTuo Authors
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
	"net/http"

	"huatuo-bamai/internal/server/response"

	httpGin "github.com/gin-gonic/gin"
)

type routerGroup struct {
	g *httpGin.RouterGroup
}

// Root group
func NewRoot(engine *httpGin.Engine, root string) *routerGroup {
	return &routerGroup{g: engine.Group(root)}
}

// Group creates a sub-group with the given relative path.
func (rg *routerGroup) Group(path string) *routerGroup {
	return &routerGroup{g: rg.g.Group(path)}
}

// Use attaches middleware HandlerFuncs to this group.
func (rg *routerGroup) Use(handlers ...HandlerContextFunc) {
	ginHandlers := make([]httpGin.HandlerFunc, len(handlers))
	for i, h := range handlers {
		ginHandlers[i] = wrapHandler(h)
	}
	rg.g.Use(ginHandlers...)
}

// Handle registers a route for the given HTTP method.
func (rg *routerGroup) Handle(method, path string, h ErrHandlerContextFunc) {
	rg.g.Handle(method, path, wrapErrHandler(h))
}

// GET registers a GET route.
func (rg *routerGroup) GET(path string, h ErrHandlerContextFunc) {
	rg.g.GET(path, wrapErrHandler(h))
}

// POST registers a POST route.
func (rg *routerGroup) POST(path string, h ErrHandlerContextFunc) {
	rg.g.POST(path, wrapErrHandler(h))
}

// DELETE registers a DELETE route.
func (rg *routerGroup) DELETE(path string, h ErrHandlerContextFunc) {
	rg.g.DELETE(path, wrapErrHandler(h))
}

// PUT registers a PUT route.
func (rg *routerGroup) PUT(path string, h ErrHandlerContextFunc) {
	rg.g.PUT(path, wrapErrHandler(h))
}

// PATCH registers a PATCH route.
func (rg *routerGroup) PATCH(path string, h ErrHandlerContextFunc) {
	rg.g.PATCH(path, wrapErrHandler(h))
}

// wrapHandler converts a HandlerFunc (no error) to httpGin.HandlerFunc.
func wrapHandler(h HandlerContextFunc) httpGin.HandlerFunc {
	return func(c *httpGin.Context) {
		ctx := internalContext(c)
		h(ctx)
	}
}

// wrapErrHandler converts an ErrHandlerFunc to gin.HandlerFunc with uniform error handling.
func wrapErrHandler(h ErrHandlerContextFunc) httpGin.HandlerFunc {
	return func(c *httpGin.Context) {
		ctx := internalContext(c)
		if err := h(ctx); err != nil {
			writeError(ctx, err)
		}
	}
}

// writeError is the internal error writer used by wrapErrHandler.
func writeError(ctx *Context, err error) {
	type apiErr interface {
		GetHTTPStatus() int
		GetCode() int
		GetMessage() string
	}
	if ae, ok := err.(apiErr); ok {
		ctx.JSON(ae.GetHTTPStatus(), response.Response{
			Code:    ae.GetCode(),
			Message: ae.GetMessage(),
		})
		return
	}
	ctx.JSON(http.StatusInternalServerError, response.Response{
		Code:    response.ErrInternal.Code,
		Message: err.Error(),
	})
}
