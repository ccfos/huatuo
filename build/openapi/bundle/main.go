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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
)

type bundleTarget struct {
	name       string
	specPath   string
	outputPath string
}

func main() {
	var specRoot string
	var outputRoot string
	flag.StringVar(&specRoot, "spec-root", ".", "repository root containing apis/v1")
	flag.StringVar(&outputRoot, "output-root", ".", "root directory for bundled specifications")
	flag.Parse()

	if err := run(context.Background(), specRoot, outputRoot); err != nil {
		fmt.Fprintf(os.Stderr, "bundle OpenAPI specifications: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, specRoot, outputRoot string) error {
	targets := []bundleTarget{
		{
			name:       "server",
			specPath:   filepath.Join(specRoot, "apis/v1/server/openapi.yaml"),
			outputPath: filepath.Join(outputRoot, "apis/v1/server/openapi.gen.json"),
		},
		{
			name:       "node",
			specPath:   filepath.Join(specRoot, "apis/v1/node/openapi.yaml"),
			outputPath: filepath.Join(outputRoot, "apis/v1/node/openapi.gen.json"),
		},
	}

	for _, target := range targets {
		data, err := bundle(ctx, target.specPath)
		if err != nil {
			return fmt.Errorf("bundle %s specification: %w", target.name, err)
		}
		if err := writeBundle(target.outputPath, data); err != nil {
			return err
		}
	}
	return nil
}

func bundle(ctx context.Context, specPath string) ([]byte, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	document, err := loader.LoadFromFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("load %q: %w", specPath, err)
	}
	if err := validateOperationIDs(document); err != nil {
		return nil, err
	}
	if err := document.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate %q: %w", specPath, err)
	}

	document.InternalizeRefs(ctx, nil)
	if err := document.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate bundled %q: %w", specPath, err)
	}

	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode bundled %q: %w", specPath, err)
	}
	return append(data, '\n'), nil
}

func validateOperationIDs(document *openapi3.T) error {
	if document.Paths == nil {
		return errors.New("paths is required")
	}

	type location struct {
		method string
		path   string
	}
	seen := make(map[string]location)
	paths := make([]string, 0, document.Paths.Len())
	for path := range document.Paths.Map() {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		pathItem := document.Paths.Value(path)
		for method, operation := range pathItem.Operations() {
			if operation.OperationID == "" {
				return fmt.Errorf("%s %s is missing operationId", method, path)
			}
			previous, ok := seen[operation.OperationID]
			if ok {
				return fmt.Errorf(
					"operationId %q is used by %s %s and %s %s",
					operation.OperationID,
					previous.method,
					previous.path,
					method,
					path,
				)
			}
			seen[operation.OperationID] = location{method: method, path: path}
		}
	}
	return nil
}

func writeBundle(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create bundle directory %q: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".openapi-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary bundle for %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary bundle for %q: %w", path, err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set bundle mode for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary bundle for %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace bundle %q: %w", path, err)
	}
	return nil
}
