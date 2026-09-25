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

// Package docscheck collects the repository paths that the documentation tells
// users to download from raw.githubusercontent.com, so that a documented path
// which does not exist in the source tree is caught by the unit tests instead of
// by a user running the deployment guide.
package docscheck

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const rawURLPrefix = "raw.githubusercontent.com/ccfos/huatuo/"

// rawURLPattern matches a raw URL into this repository, stopping at the
// delimiters that surround URLs in markdown (quotes, backticks, parentheses).
var rawURLPattern = regexp.MustCompile(`raw\.githubusercontent\.com/ccfos/huatuo/[^\s"'` + "`" + `)]+`)

// skipDirs are directories that are not documentation and are not part of the
// source tree a document may point at.
var skipDirs = map[string]bool{
	".git":         true,
	".claude":      true,
	"_output":      true,
	"node_modules": true,
	"vendor":       true,
}

// DocumentedRepoPaths walks the markdown files below root and returns, for each
// file that references this repository through raw.githubusercontent.com, the
// repository-relative paths it points at. The git ref between the repository
// name and the path (a tag, a branch, or a variable such as ${HUATUO_VERSION} in
// a documentation snippet) is dropped: only the path is of interest here.
func DocumentedRepoPaths(root string) (map[string][]string, error) {
	found := make(map[string][]string)

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}

		paths, err := rawURLPathsInFile(path)
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return nil
		}
		slices.Sort(paths)
		paths = slices.Compact(paths)

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found[filepath.ToSlash(relative)] = paths

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

func rawURLPathsInFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var paths []string

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		for _, rawURL := range rawURLPattern.FindAllString(scanner.Text(), -1) {
			if repoPath := repoPathFromRawURL(rawURL); repoPath != "" {
				paths = append(paths, repoPath)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return paths, nil
}

// repoPathFromRawURL turns a raw URL into the repository-relative path it
// references, for example
// ".../ccfos/huatuo/main/build/rpm/huatuo-bamai.service" -> "build/rpm/huatuo-bamai.service".
// It returns an empty string when the URL carries no path after the ref.
func repoPathFromRawURL(rawURL string) string {
	rest := strings.TrimPrefix(rawURL, rawURLPrefix)

	_, path, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	if end := strings.IndexAny(path, "#?"); end >= 0 {
		path = path[:end]
	}

	return strings.TrimRight(path, ".,;")
}
