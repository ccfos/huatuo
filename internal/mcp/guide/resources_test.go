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

package guide

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestEmbeddedGuideUsageAndPublicScope(t *testing.T) {
	dataFS := NewFS("")
	_, paths, err := renderGuideStage(t.Context(), dataFS, "overview", "")
	if err != nil {
		t.Fatal(err)
	}
	var guide string
	for _, path := range paths {
		if path != "resources/playbook/guide.md" {
			continue
		}
		content, readErr := fs.ReadFile(dataFS, path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		guide = string(content)
	}
	for _, required := range []string{"isError", "status=partial", "invalid_argument", "retryable=false", "availability=unknown"} {
		if !strings.Contains(guide, required) {
			t.Errorf("overview guide missing Tool usage rule %q", required)
		}
	}
	for _, removed := range []string{"troubleshooting mind map", "single-container latency", "cluster latency", "container_qos", "container_level", "replace the OS", "systems department"} {
		if strings.Contains(guide, removed) {
			t.Errorf("overview guide contains deployment-specific playbook content %q", removed)
		}
	}
	err = fs.WalkDir(dataFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := fs.ReadFile(dataFS, path)
		if readErr != nil {
			return readErr
		}
		if strings.ContainsFunc(string(content), func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Errorf("%s contains untranslated Chinese text", path)
		}
		for _, removed := range []string{"fastfork", "ptsep", "waitrate", "sched-blame-external", "softirq_tracing"} {
			if strings.Contains(string(content), removed) {
				t.Errorf("%s advertises unsupported tracer or field %q", path, removed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResourcesSkipEmptyOverlayNames(t *testing.T) {
	resources := discoverResources(fstest.MapFS{
		"resources/.md":      {Data: []byte("invalid")},
		"resources/.yaml":    {Data: []byte("invalid")},
		"resources/.yml":     {Data: []byte("invalid")},
		"resources/valid.md": {Data: []byte("valid")},
	}, "resources", "")
	if len(resources) != 1 || resources[0].uri != "huatuo://valid" {
		t.Fatalf("resources = %+v", resources)
	}
}

type unreadableManifestFS struct{ fstest.MapFS }

func (f unreadableManifestFS) ReadFile(name string) ([]byte, error) {
	return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrPermission}
}

func TestManifestDoesNotHashUnreadableDataAsEmpty(t *testing.T) {
	dataFS := unreadableManifestFS{fstest.MapFS{"resources/test.md": {Data: []byte("important")}}}
	if _, err := handleManifest(dataFS)(t.Context(), mcpgo.ReadResourceRequest{}); err == nil {
		t.Fatal("unreadable manifest returned success")
	}
}
