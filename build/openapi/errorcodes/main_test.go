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

package main

import (
	"strings"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	t.Parallel()

	definitions, err := parseCatalog([]byte(`
openapi: 3.0.3
x-error-codes:
  service_unavailable:
    httpStatus: 503
    description: Service is unavailable.
  invalid_request:
    httpStatus: 400
    description: Request is invalid.
`))
	if err != nil {
		t.Fatalf("parseCatalog() error = %v, want nil", err)
	}
	if len(definitions) != 2 {
		t.Fatalf("len(parseCatalog()) = %d, want 2", len(definitions))
	}
	if definitions[0].Code != "invalid_request" || definitions[0].HTTPStatus != 400 {
		t.Errorf("parseCatalog()[0] = %+v, want invalid_request/400", definitions[0])
	}
}

func TestParseCatalogRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		catalog string
		wantErr string
	}{
		{
			name: "duplicate code",
			catalog: `
openapi: 3.0.3
x-error-codes:
  invalid_request:
    httpStatus: 400
    description: First.
  invalid_request:
    httpStatus: 400
    description: Second.
`,
			wantErr: `duplicate error code "invalid_request"`,
		},
		{
			name: "invalid name",
			catalog: `
openapi: 3.0.3
x-error-codes:
  InvalidRequest:
    httpStatus: 400
    description: Invalid.
`,
			wantErr: `error code "InvalidRequest" must use lower snake_case`,
		},
		{
			name: "success status",
			catalog: `
openapi: 3.0.3
x-error-codes:
  invalid_request:
    httpStatus: 200
    description: Invalid.
`,
			wantErr: `httpStatus 200 must be between 400 and 599`,
		},
		{
			name: "missing description",
			catalog: `
openapi: 3.0.3
x-error-codes:
  invalid_request:
    httpStatus: 400
`,
			wantErr: `error code "invalid_request" description is required`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseCatalog([]byte(tt.catalog))
			if err == nil {
				t.Fatalf("parseCatalog() error = nil, want containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("parseCatalog() error = %q, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateCatalogsRejectsConflictingDefinitions(t *testing.T) {
	t.Parallel()

	err := validateCatalogs([]catalog{
		{
			name: "server",
			definitions: []definition{{
				Code:        "execution_failed",
				HTTPStatus:  500,
				Description: "Execution failed.",
			}},
		},
		{
			name: "node",
			definitions: []definition{{
				Code:        "execution_failed",
				HTTPStatus:  500,
				Description: "Node execution failed.",
			}},
		},
	})
	if err == nil {
		t.Fatal("validateCatalogs() error = nil, want conflict")
	}
	if want := `error code "execution_failed" differs between server and node catalogs`; err.Error() != want {
		t.Errorf("validateCatalogs() error = %q, want %q", err, want)
	}
}

func TestGoNamePreservesInitialisms(t *testing.T) {
	t.Parallel()

	if got, want := goName("request_id_conflict"), "RequestIDConflict"; got != want {
		t.Errorf("goName() = %q, want %q", got, want)
	}
	if got, want := goName("internal_error"), "Internal"; got != want {
		t.Errorf("goName() = %q, want %q", got, want)
	}
}
