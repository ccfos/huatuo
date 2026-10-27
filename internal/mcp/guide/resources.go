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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const (
	MIMEJSON = "application/json"
)

const (
	MarkdownExtension  = ".md"
	MIMEMarkdown       = "text/markdown"
	YAMLExtension      = ".yaml"
	MIMEYAML           = "text/yaml"
	ShortYAMLExtension = ".yml"
	ResourceDirectory  = "resources"
	PromptDirectory    = "prompts"
	ManifestURI        = "huatuo://data-manifest"
)

type resourceDef struct {
	uri  string
	name string
	desc string
	mime string
	path string
}

var mimeByExt = map[string]string{
	MarkdownExtension:  MIMEMarkdown,
	YAMLExtension:      MIMEYAML,
	ShortYAMLExtension: MIMEYAML,
}

func RegisterResources(dataFS fs.FS, mcpHandler *mcpserver.MCPServer) {
	var all []resourceDef

	all = append(all, discoverResources(dataFS, ResourceDirectory, "")...)
	all = append(all, discoverResources(dataFS, PromptDirectory, PromptDirectory)...)

	sort.Slice(all, func(i, j int) bool { return all[i].uri < all[j].uri })

	for _, def := range all {
		d := def
		resource := mcpgo.NewResource(d.uri, d.name,
			mcpgo.WithResourceDescription(d.desc),
			mcpgo.WithMIMEType(d.mime),
		)
		mcpHandler.AddResource(resource, makeResourceHandler(dataFS, &d))
	}

	infof("resources: auto-discovered %d total", len(all))

	registerManifest(dataFS, mcpHandler)
}

func discoverResources(dataFS fs.FS, dir, uriPrefix string) []resourceDef {
	var defs []resourceDef

	_ = fs.WalkDir(dataFS, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := filepath.Ext(d.Name())
		mime, ok := mimeByExt[ext]
		if !ok {
			return nil
		}

		rel := strings.TrimPrefix(path, dir+"/")
		uriPath := strings.TrimSuffix(rel, ext)
		uriPath = strings.ReplaceAll(uriPath, "_", "-")
		if uriPrefix != "" {
			uriPath = uriPrefix + "/" + uriPath
		}

		base := strings.TrimSuffix(d.Name(), ext)
		if base == "" {
			return nil
		}
		name := strings.ToUpper(base[:1]) + base[1:]
		name = strings.ReplaceAll(name, "_", " ")

		defs = append(defs, resourceDef{
			uri:  ResourceURIPrefix + uriPath,
			name: name,
			desc: "Guide resource: " + uriPath,
			mime: mime,
			path: path,
		})
		return nil
	})

	infof("resources: %s → %d discovered", dir, len(defs))
	return defs
}

func makeResourceHandler(dataFS fs.FS, def *resourceDef) func(ctx context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
	return func(ctx context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		startTime := time.Now()

		content, err := fs.ReadFile(dataFS, def.path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", def.path, err)
		}

		infof("resource read result: uri=%s size=%d dur=%s", def.uri, len(content), time.Since(startTime).Round(time.Millisecond))

		return []mcpgo.ResourceContents{
			mcpgo.TextResourceContents{
				URI:      def.uri,
				MIMEType: def.mime,
				Text:     string(content),
			},
		}, nil
	}
}

func registerManifest(dataFS fs.FS, mcpHandler *mcpserver.MCPServer) {
	manifest := mcpgo.NewResource(ManifestURI, "Data Manifest",
		mcpgo.WithResourceDescription("All data files with SHA256 hashes. Compare to verify data sync status."),
		mcpgo.WithMIMEType(MIMEJSON),
	)
	mcpHandler.AddResource(manifest, handleManifest(dataFS))
}

func handleManifest(dataFS fs.FS) func(ctx context.Context, _ mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
	return func(ctx context.Context, _ mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		type fileEntry struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		}

		var files []fileEntry

		for _, dir := range []string{ResourceDirectory, PromptDirectory} {
			err := fs.WalkDir(dataFS, dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return fmt.Errorf("stat manifest file %s: %w", path, err)
				}
				data, err := fs.ReadFile(dataFS, path)
				if err != nil {
					return fmt.Errorf("read manifest file %s: %w", path, err)
				}
				h := sha256.Sum256(data)
				fileSize := int64(0)
				if info != nil {
					fileSize = info.Size()
				}
				files = append(files, fileEntry{
					Path:   path,
					Size:   fileSize,
					SHA256: hex.EncodeToString(h[:]),
				})
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("build data manifest: %w", err)
			}
		}

		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

		payload, _ := json.MarshalIndent(struct {
			GeneratedAt string      `json:"generated_at"`
			Files       []fileEntry `json:"files"`
		}{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Files:       files,
		}, "", "  ")

		return []mcpgo.ResourceContents{
			mcpgo.TextResourceContents{
				URI:      ManifestURI,
				MIMEType: MIMEJSON,
				Text:     string(payload),
			},
		}, nil
	}
}
