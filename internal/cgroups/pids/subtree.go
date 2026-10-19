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

package pids

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Subtree reads process IDs without following links outside the cgroup tree.
func Subtree(root, file string) ([]int32, error) {
	var result []int32
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path != root && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		values, err := Tasks(path, file)
		if err != nil {
			if path != root && errors.Is(err, os.ErrNotExist) {
				return filepath.SkipDir
			}
			return fmt.Errorf("read cgroup processes %q: %w", path, err)
		}
		result = append(result, values...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}
