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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/google/pprof/profile"
)

type artifact struct {
	Endpoint string `json:"endpoint"`
	File     string `json:"file"`
}

type resourceSummary struct {
	CPUPercent     *float64         `json:"cpu_percent,omitempty"`
	HeapInuseBytes *int64           `json:"heap_inuse_bytes,omitempty"`
	Goroutines     *int64           `json:"goroutines,omitempty"`
	Profiles       []profileSummary `json:"profiles,omitempty"`
	Artifacts      []artifact       `json:"artifacts,omitempty"`
	Warnings       []string         `json:"warnings,omitempty"`
}

func summarize(directory string, selected []string) (returnErr error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()

	file, err := root.Open("capture.json")
	if err != nil {
		return err
	}

	data, err := io.ReadAll(file)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}

	var endpoints []string
	if err := json.Unmarshal(data, &endpoints); err != nil {
		return err
	}

	if len(endpoints) == 0 {
		return errors.New("capture has no endpoints")
	}

	var result resourceSummary
	seen := make(map[string]bool)
	for _, value := range endpoints {
		endpoint, err := parseEndpoint(value)
		if err != nil {
			return err
		}

		name := endpoint.Path
		if seen[name] {
			return fmt.Errorf("duplicate captured endpoint %q", name)
		}

		seen[name] = true
		if len(selected) != 0 && !slices.Contains(selected, name) {
			continue
		}

		filename := endpointFile(endpoint)
		stat, err := root.Stat(filename)
		if err != nil {
			return err
		}

		if !stat.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", filename)
		}

		if !strings.HasSuffix(filename, ".pprof") {
			result.Artifacts = append(result.Artifacts, artifact{
				Endpoint: value,
				File:     filename,
			})
			continue
		}

		p, err := readProfile(root, filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}

		totals, err := sumSampleValues(p)
		if err != nil {
			return fmt.Errorf("summarize %s: %w", filename, err)
		}

		if err := result.addProfile(
			endpoint,
			value,
			p,
			totals,
		); err != nil {
			return err
		}
	}

	for _, name := range selected {
		if !seen[name] {
			return fmt.Errorf("endpoint %q was not captured", name)
		}
	}

	for _, warning := range result.Warnings {
		fmt.Fprintln(os.Stderr, "pprof-tool: warning:", warning)
	}

	return json.NewEncoder(os.Stdout).Encode(&result)
}

func readProfile(root *os.Root, filename string) (*profile.Profile, error) {
	file, err := root.Open(filename)
	if err != nil {
		return nil, err
	}

	p, parseErr := profile.Parse(file)
	return p, errors.Join(parseErr, file.Close())
}
