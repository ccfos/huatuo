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

package knowledge

import (
	"embed"
	"errors"
	"io/fs"
	"os"
)

//go:embed data
var embedFS embed.FS

// overlayFS prefers files from diskDir; if a file is missing on disk it falls
// back to the embedded copy. Each Open re-reads from disk so operators can
// override knowledge files without a restart.
type overlayFS struct {
	disk    fs.FS
	embed   fs.FS
	diskDir string
}

func NewFS(diskDir string) fs.FS {
	embedSub, err := fs.Sub(embedFS, "data")
	if err != nil {
		errorf("data: failed to create embed sub-FS: %v", err)
		embedSub = embedFS
	}

	if diskDir == "" {
		infof("data: using embedded FS (no external dataDir configured)")
		return embedSub
	}

	info, err := os.Stat(diskDir)
	if err != nil {
		warnf("data: external directory %s not accessible: %v, using embedded FS", diskDir, err)
		return embedSub
	}
	if !info.IsDir() {
		warnf("data: %s is not a directory, using embedded FS", diskDir)
		return embedSub
	}

	infof("data: using overlay FS (disk=%s, embed=fallback)", diskDir)
	return &overlayFS{
		disk:    os.DirFS(diskDir),
		embed:   embedSub,
		diskDir: diskDir,
	}
}

func (o *overlayFS) Open(name string) (fs.File, error) {
	f, err := o.disk.Open(name)
	if err == nil {
		debugf("data: read from disk: %s/%s", o.diskDir, name)
		return f, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		warnf("data: disk read error for %s/%s: %v, falling back to embed", o.diskDir, name, err)
	}
	debugf("data: read from embed: %s", name)
	return o.embed.Open(name)
}

func (o *overlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	// Merge disk and embed entries: disk takes priority on conflicts.
	// Without merging, a partial disk override (e.g. only cpu/ on disk)
	// would hide embed-only siblings (memory/, io/, network/, hardware/).
	seen := make(map[string]bool)
	var merged []fs.DirEntry

	if diskFS, ok := o.disk.(fs.ReadDirFS); ok {
		if entries, err := diskFS.ReadDir(name); err == nil {
			for _, e := range entries {
				seen[e.Name()] = true
				merged = append(merged, e)
			}
		}
	}

	if embedFS, ok := o.embed.(fs.ReadDirFS); ok {
		if entries, err := embedFS.ReadDir(name); err == nil {
			for _, e := range entries {
				if !seen[e.Name()] {
					merged = append(merged, e)
				}
			}
		}
	}

	if len(merged) == 0 {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return merged, nil
}

var (
	_ fs.FS        = (*overlayFS)(nil)
	_ fs.ReadDirFS = (*overlayFS)(nil)
)
