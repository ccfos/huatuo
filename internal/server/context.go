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
	httpBinding "github.com/gin-gonic/gin/binding"
)

const contextKey = "_server_ctx"

// Context wraps gin.Context and hides gin details from handlers.
type Context struct {
	c       *httpGin.Context
	UserID  string
	IsAdmin bool
}

// HandlerContextFunc is a middleware/handler that receives a custom Context.
type HandlerContextFunc func(*Context)

// ErrHandlerContextFunc is a handler that returns an error for uniform error handling.
type ErrHandlerContextFunc func(*Context) error

func newContext(c *httpGin.Context) *Context {
	ctx := &Context{c: c}
	c.Set(contextKey, ctx)
	return ctx
}

func internalContext(c *httpGin.Context) *Context {
	if v, ok := c.Get(contextKey); ok {
		if ctx, ok := v.(*Context); ok {
			return ctx
		}
	}
	return newContext(c)
}

func middlewareContext() httpGin.HandlerFunc {
	return func(c *httpGin.Context) {
		newContext(c)
		c.Next()
	}
}

func (ctx *Context) Param(key string) string {
	return ctx.c.Param(key)
}

func (ctx *Context) Query(key string) string {
	return ctx.c.Query(key)
}

func (ctx *Context) DefaultQuery(key, defaultValue string) string {
	return ctx.c.DefaultQuery(key, defaultValue)
}

func (ctx *Context) ShouldBindJSON(obj any) error {
	return ctx.c.ShouldBindJSON(obj)
}

func (ctx *Context) ShouldBindBodyWith(obj any, b httpBinding.BindingBody) error {
	return ctx.c.ShouldBindBodyWith(obj, b)
}

func (ctx *Context) ShouldBindQuery(obj any) error {
	return ctx.c.ShouldBindQuery(obj)
}

func (ctx *Context) JSON(code int, obj any) {
	ctx.c.JSON(code, obj)
}

func (ctx *Context) ProtoBuf(code int, obj any) {
	ctx.c.ProtoBuf(code, obj)
}

func (ctx *Context) Status(code int) {
	ctx.c.Status(code)
}

func (ctx *Context) Header(key, val string) {
	ctx.c.Header(key, val)
}

func (ctx *Context) ClientIP() string {
	return ctx.c.ClientIP()
}

func (ctx *Context) Request() *http.Request {
	return ctx.c.Request
}

func (ctx *Context) Writer() http.ResponseWriter {
	return ctx.c.Writer
}

func (ctx *Context) Abort() {
	ctx.c.Abort()
}

func (ctx *Context) Next() {
	ctx.c.Next()
}

func (ctx *Context) CanAccessTask(taskUserID string) bool {
	return ctx.IsAdmin || ctx.UserID == taskUserID
}
