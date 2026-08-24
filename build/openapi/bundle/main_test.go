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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestBundleInternalizesExternalReferences(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "components.yaml"), `
openapi: 3.0.3
info:
  title: Components
  version: v1
paths: {}
components:
  schemas:
    Message:
      type: object
      required: [value]
      properties:
        value:
          type: string
`)
	rootPath := filepath.Join(directory, "openapi.yaml")
	writeTestFile(t, rootPath, `
openapi: 3.0.3
info:
  title: Test
  version: v1
paths:
  /message:
    get:
      operationId: getMessage
      responses:
        '200':
          description: Message
          content:
            application/json:
              schema:
                $ref: './components.yaml#/components/schemas/Message'
`)

	data, err := bundle(t.Context(), rootPath)
	if err != nil {
		t.Fatalf("bundle() error = %v, want nil", err)
	}
	if strings.Contains(string(data), "components.yaml") {
		t.Errorf("bundle() retained external reference: %s", data)
	}
	loader := openapi3.NewLoader()
	if _, err := loader.LoadFromData(data); err != nil {
		t.Errorf("LoadFromData(bundle()) error = %v, want nil", err)
	}
}

func TestValidateOperationIDs(t *testing.T) {
	t.Parallel()

	document := &openapi3.T{
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/first", &openapi3.PathItem{
				Get: &openapi3.Operation{OperationID: "read"},
			}),
			openapi3.WithPath("/second", &openapi3.PathItem{
				Post: &openapi3.Operation{OperationID: "read"},
			}),
		),
	}

	err := validateOperationIDs(document)
	if err == nil {
		t.Fatal("validateOperationIDs() error = nil, want duplicate operationId")
	}
	if want := `operationId "read" is used by GET /first and POST /second`; err.Error() != want {
		t.Errorf("validateOperationIDs() error = %q, want %q", err, want)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}
