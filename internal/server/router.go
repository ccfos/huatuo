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

	httpGin "github.com/gin-gonic/gin"
)

type routerGroup struct {
	g *httpGin.RouterGroup
}

func NewRoot(engine *httpGin.Engine, root string) *routerGroup {
	return &routerGroup{g: engine.Group(root)}
}

func (rg *routerGroup) Group(path string) *routerGroup {
	return &routerGroup{g: rg.g.Group(path)}
}

func (rg *routerGroup) Use(handlers ...HandlerContextFunc) {
	ginHandlers := make([]httpGin.HandlerFunc, len(handlers))
	for i, h := range handlers {
		ginHandlers[i] = wrapHandler(h)
	}
	rg.g.Use(ginHandlers...)
}

func (rg *routerGroup) Handle(method, path string, h ErrHandlerContextFunc) {
	rg.g.Handle(method, path, wrapErrHandler(h))
}

func (rg *routerGroup) GET(path string, h ErrHandlerContextFunc) {
	rg.g.GET(path, wrapErrHandler(h))
}

func (rg *routerGroup) POST(path string, h ErrHandlerContextFunc) {
	rg.g.POST(path, wrapErrHandler(h))
}

func (rg *routerGroup) DELETE(path string, h ErrHandlerContextFunc) {
	rg.g.DELETE(path, wrapErrHandler(h))
}

func (rg *routerGroup) PUT(path string, h ErrHandlerContextFunc) {
	rg.g.PUT(path, wrapErrHandler(h))
}

func wrapHandler(h HandlerContextFunc) httpGin.HandlerFunc {
	return func(c *httpGin.Context) {
		ctx := internalContext(c)
		h(ctx)
	}
}

func wrapErrHandler(h ErrHandlerContextFunc) httpGin.HandlerFunc {
	return func(c *httpGin.Context) {
		ctx := internalContext(c)
		if err := h(ctx); err != nil {
			writeError(ctx, err)
		}
	}
}

func writeError(ctx *Context, err error) {
	type apiErr interface {
		GetHTTPStatus() int
		GetCode() int
		GetMessage() string
	}

	if ae, ok := err.(apiErr); ok {
		ctx.JSON(ae.GetHTTPStatus(), map[string]any{
			"code":    ae.GetCode(),
			"message": ae.GetMessage(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(http.StatusInternalServerError, map[string]any{
		"code":    500,
		"message": err.Error(),
		"data":    nil,
	})
}
