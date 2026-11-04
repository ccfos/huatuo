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

package docscheck

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repositoryRoot is the module root, relative to this package.
const repositoryRoot = "../.."

// TestDocumentedRepositoryPathsExist guards the deployment guides against
// pointing at repository files that do not exist: such a path is invisible to
// the compiler, the linters and the build, and only shows up for the user
// following the guide.
func TestDocumentedRepositoryPathsExist(t *testing.T) {
	documented, err := DocumentedRepoPaths(repositoryRoot)
	if err != nil {
		t.Fatalf("collect documented repository paths: %v", err)
	}
	if len(documented) == 0 {
		t.Fatal("no raw.githubusercontent.com URLs found; the check is not scanning the documentation")
	}

	var missing []string

	for file, paths := range documented {
		for _, repoPath := range paths {
			target := filepath.Join(repositoryRoot, filepath.FromSlash(repoPath))
			if _, err := os.Stat(target); err != nil {
				missing = append(missing, file+": "+repoPath+" ("+err.Error()+")")
			}
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("documentation references %d repository path(s) that do not exist:\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

// TestServiceUnitsAreUsable pins the shape the systemd guides rely on: a unit
// file that is empty is reported by systemd as "masked" rather than as a
// missing unit, so a truncated or placeholder unit must not be shipped.
func TestServiceUnitsAreUsable(t *testing.T) {
	units, err := filepath.Glob(filepath.Join(repositoryRoot, "build", "rpm", "*.service"))
	if err != nil {
		t.Fatalf("glob service units: %v", err)
	}
	if len(units) == 0 {
		t.Fatal("no service units found under build/rpm")
	}

	for _, unit := range units {
		content, err := os.ReadFile(unit)
		if err != nil {
			t.Fatalf("read %s: %v", unit, err)
		}

		text := string(content)
		if !strings.HasPrefix(text, "[Unit]") {
			t.Errorf("%s must start with a [Unit] section", unit)
		}
		for _, section := range []string{"[Service]", "[Install]", "ExecStart=", "WantedBy="} {
			if !strings.Contains(text, section) {
				t.Errorf("%s is missing %s", unit, section)
			}
		}
	}
}
