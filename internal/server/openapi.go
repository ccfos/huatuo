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
	"errors"
	"fmt"
	"net/http"
	"strings"

	v1 "github.com/ccfos/huatuo/apis/v1"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/server/response"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	httpGin "github.com/gin-gonic/gin"
)

// StrictErrorHandlers adapts generated Strict Server errors to the shared
// error response boundary.
type StrictErrorHandlers struct {
	RequestError  func(ctx *httpGin.Context, err error)
	HandlerError  func(ctx *httpGin.Context, err error)
	ResponseError func(ctx *httpGin.Context, err error)
}

// NewStrictErrorHandlers returns callbacks for generated Strict Server options.
func NewStrictErrorHandlers(statusMapper response.HTTPStatusMapper) StrictErrorHandlers {
	return StrictErrorHandlers{
		RequestError: func(ctx *httpGin.Context, err error) {
			writeGinError(ctx, requestValidationError(err), statusMapper)
		},
		HandlerError: func(ctx *httpGin.Context, err error) {
			if !isAPIError(err) {
				log.WithError(err).Error("strict handler failed")
			}
			writeGinError(ctx, err, statusMapper)
		},
		ResponseError: func(ctx *httpGin.Context, err error) {
			log.WithError(err).Error("strict response serialization failed")
			writeGinError(ctx, response.ErrInternal, statusMapper)
		},
	}
}

// RegisterOpenAPIHandlers validates generated routes against their bundled
// specification before dispatching to the generated Gin adapter.
func (s *Server) RegisterOpenAPIHandlers(
	specification []byte,
	register func(router httpGin.IRouter),
) error {
	if register == nil {
		return errors.New("register OpenAPI handlers: register function is required")
	}
	validator, err := newOpenAPIValidator(specification, s.config.ErrorStatusMapper)
	if err != nil {
		return err
	}

	router := s.engine.Group("")
	router.Use(validator)
	register(router)
	return nil
}

// StrictErrorHandlers returns callbacks using the server's error directory.
func (s *Server) StrictErrorHandlers() StrictErrorHandlers {
	return NewStrictErrorHandlers(s.config.ErrorStatusMapper)
}

func newOpenAPIValidator(
	specification []byte,
	statusMapper response.HTTPStatusMapper,
) (httpGin.HandlerFunc, error) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(specification)
	if err != nil {
		return nil, fmt.Errorf("create OpenAPI validator: load specification: %w", err)
	}
	router, err := legacy.NewRouter(document)
	if err != nil {
		return nil, fmt.Errorf("create OpenAPI validator: %w", err)
	}
	options := &openapi3filter.Options{
		AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	}

	return func(ctx *httpGin.Context) {
		route, pathParams, err := router.FindRoute(ctx.Request)
		if err == nil && route.Operation.RequestBody == nil &&
			ctx.Request.Body != nil && ctx.Request.Body != http.NoBody &&
			ctx.Request.ContentLength != 0 {
			err = response.ErrInvalidRequest.WithMessage("request body is not allowed")
		}
		if err == nil {
			err = openapi3filter.ValidateRequest(ctx.Request.Context(), &openapi3filter.RequestValidationInput{
				Request:    ctx.Request,
				PathParams: pathParams,
				Route:      route,
				Options:    options,
			})
		}
		if err != nil {
			writeGinError(ctx, requestValidationError(err), statusMapper)
			return
		}
		ctx.Next()
	}, nil
}

func isAPIError(err error) bool {
	var apiError interface {
		GetCode() v1.ErrorCode
		GetMessage() string
	}
	return errors.As(err, &apiError)
}

func requestValidationError(err error) *response.APIError {
	var apiError *response.APIError
	if errors.As(err, &apiError) {
		return apiError
	}

	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return response.ErrRequestTooLarge
	}

	converted := openapi3filter.ConvertErrors(err)
	var validationError *openapi3filter.ValidationError
	if !errors.As(converted, &validationError) {
		return response.ErrInvalidRequest
	}

	message := strings.TrimSpace(validationError.Title)
	switch validationError.Status {
	case http.StatusNotFound:
		return response.ErrRouteNotFound.WithMessage(defaultMessage(message, "route not found"))
	case http.StatusMethodNotAllowed:
		return response.ErrMethodNotAllowed.WithMessage(defaultMessage(message, "method not allowed"))
	case http.StatusUnsupportedMediaType:
		return response.ErrUnsupportedMediaType.WithMessage(defaultMessage(
			message,
			"request content type is not supported",
		))
	default:
		return response.ErrInvalidRequest.WithMessage(defaultMessage(
			message,
			"request does not satisfy the API contract",
		))
	}
}

func writeGinError(
	ctx *httpGin.Context,
	err error,
	statusMapper response.HTTPStatusMapper,
) {
	response.Error(ctx, err, statusMapper)
	ctx.Abort()
}

func defaultMessage(message, fallback string) string {
	if message == "" {
		return fallback
	}
	return message
}
