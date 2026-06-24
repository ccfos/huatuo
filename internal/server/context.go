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
	"fmt"
	"net/http"
	"strconv"

	httpGin "github.com/gin-gonic/gin"
	httpBinding "github.com/gin-gonic/gin/binding"
)

const contextKey = "_server_ctx"

// Context is a custom request context that wraps gin.Context internally.
// All gin types are hidden; callers only interact with this type.
type Context struct {
	c       *httpGin.Context // private, never exposed
	UserID  string
	IsAdmin bool
}

// HandlerContextFunc is a middleware/handler that receives a custom Context.
type HandlerContextFunc func(*Context)

// ErrHandlerContextFunc is a handler that returns an error for uniform error handling.
type ErrHandlerContextFunc func(*Context) error

// newContext creates a Context and stores it in the gin.Context.
func newContext(c *httpGin.Context) *Context {
	ctx := &Context{c: c}
	c.Set(contextKey, ctx)
	return ctx
}

// internalContext retrieves the custom Context stored by newContext.
func internalContext(c *httpGin.Context) *Context {
	if v, ok := c.Get(contextKey); ok {
		if ctx, ok := v.(*Context); ok {
			return ctx
		}
	}
	// fallback: create a new one (should not happen in normal flow)
	return newContext(c)
}

// middlewareContext injects a custom Context into every request.
func middlewareContext() httpGin.HandlerFunc {
	return func(c *httpGin.Context) {
		newContext(c)
		c.Next()
	}
}

// Param returns the URL path parameter for the given key (e.g. "id" for "/:id").
func (ctx *Context) Param(key string) string {
	return ctx.c.Param(key)
}

// Query returns the query string value for the given key.
func (ctx *Context) Query(key string) string {
	return ctx.c.Query(key)
}

// ShouldBindJSON decodes the JSON request body into obj.
func (ctx *Context) ShouldBindJSON(obj any) error {
	return ctx.c.ShouldBindJSON(obj)
}

// ShouldBindBodyWith decodes the request body using the given binding.
// Useful for ProtoBuf and other non-JSON encodings.
func (ctx *Context) ShouldBindBodyWith(obj any, b httpBinding.BindingBody) error {
	return ctx.c.ShouldBindBodyWith(obj, b)
}

// JSON writes a JSON response with the given HTTP status code.
func (ctx *Context) JSON(code int, obj any) {
	ctx.c.JSON(code, obj)
}

// ProtoBuf writes a protobuf response with the given HTTP status code.
func (ctx *Context) ProtoBuf(code int, obj any) {
	ctx.c.ProtoBuf(code, obj)
}

// Status writes the given HTTP status code without a body.
func (ctx *Context) Status(code int) {
	ctx.c.Status(code)
}

// Header sets a response header.
func (ctx *Context) Header(key, val string) {
	ctx.c.Header(key, val)
}

// ClientIP returns the client's IP address.
func (ctx *Context) ClientIP() string {
	return ctx.c.ClientIP()
}

// Query returns the query string value for the given key, or the defaultValue if not present.
func (ctx *Context) DefaultQuery(key, defaultValue string) string {
	return ctx.c.DefaultQuery(key, defaultValue)
}

// ShouldBindQuery binds query string parameters into obj using the form binding.
func (ctx *Context) ShouldBindQuery(obj any) error {
	return ctx.c.ShouldBindQuery(obj)
}

// Request returns the underlying *http.Request.
func (ctx *Context) Request() *http.Request {
	return ctx.c.Request
}

// Writer returns the underlying http.ResponseWriter.
func (ctx *Context) Writer() http.ResponseWriter {
	return ctx.c.Writer
}

// Abort prevents pending handlers from being called.
func (ctx *Context) Abort() {
	ctx.c.Abort()
}

// Next executes the pending handlers in the chain.
func (ctx *Context) Next() {
	ctx.c.Next()
}

// CanAccessTask reports whether the current user may access a task owned by taskUserID.
func (ctx *Context) CanAccessTask(taskUserID string) bool {
	return ctx.IsAdmin || ctx.UserID == taskUserID
}

const (
	// DefaultListLimit is the default page size for list endpoints.
	DefaultListLimit = 50
	// MaxListLimit is the maximum allowed page size.
	MaxListLimit = 500
)

// ListParams holds pagination and sorting parameters for list endpoints.
// Sort uses a leading "-" for descending, e.g. "-start_time".
type ListParams struct {
	Limit  int
	Offset int
	Sort   string
}

// ParseListParams reads limit/offset/sort from the query string.
// Defaults: limit=50, offset=0. limit is clamped to [1, 500].
func (ctx *Context) ParseListParams() (ListParams, error) {
	p := ListParams{Limit: DefaultListLimit}

	if v := ctx.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return p, fmt.Errorf("invalid limit %q", v)
		}
		if n > MaxListLimit {
			n = MaxListLimit
		}
		p.Limit = n
	}

	if v := ctx.Query("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, fmt.Errorf("invalid offset %q", v)
		}
		p.Offset = n
	}

	p.Sort = ctx.Query("sort")
	return p, nil
}
