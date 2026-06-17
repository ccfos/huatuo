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

package version

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestResolveSeedWins(t *testing.T) {
	got := Resolve(Seed{
		Name:      "tool",
		Version:   "1.2.3",
		GitCommit: "abc123",
		BuildTime: "2026-06-17T00:00:00Z",
	})

	if got.Name != "tool" {
		t.Errorf("Resolve().Name = %q, want %q", got.Name, "tool")
	}
	if got.Version != "1.2.3" {
		t.Errorf("Resolve().Version = %q, want %q", got.Version, "1.2.3")
	}
	if got.GitCommit != "abc123" {
		t.Errorf("Resolve().GitCommit = %q, want %q", got.GitCommit, "abc123")
	}
	if got.BuildTime != "2026-06-17T00:00:00Z" {
		t.Errorf("Resolve().BuildTime = %q, want %q", got.BuildTime, "2026-06-17T00:00:00Z")
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("Resolve().GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
	if got.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("Resolve().Platform = %q, want %q", got.Platform, runtime.GOOS+"/"+runtime.GOARCH)
	}
}

func TestResolveDirtySuffix(t *testing.T) {
	got := Resolve(Seed{Name: "tool", GitCommit: "abc123-dirty"})

	if got.GitCommit != "abc123" {
		t.Errorf("Resolve().GitCommit = %q, want %q", got.GitCommit, "abc123")
	}
	if got.GitTreeState != treeStateDirty {
		t.Errorf("Resolve().GitTreeState = %q, want %q", got.GitTreeState, treeStateDirty)
	}
}

func TestResolveDevelFallback(t *testing.T) {
	// debug.ReadBuildInfo can backfill `go test` runs with the test binary's
	// own VCS info; assert that any unfilled string still ends up renderable.
	got := Resolve(Seed{Name: "tool"})

	if got.Version != Devel {
		t.Errorf("Resolve().Version = %q, want %q", got.Version, Devel)
	}
	if got.GitCommit == "" {
		t.Errorf("Resolve().GitCommit = %q, want non-empty", got.GitCommit)
	}
	if got.BuildTime == "" {
		t.Errorf("Resolve().BuildTime = %q, want non-empty", got.BuildTime)
	}
	if got.GitTreeState == "" {
		t.Errorf("Resolve().GitTreeState = %q, want non-empty", got.GitTreeState)
	}
}

func TestSplitDirty(t *testing.T) {
	tests := []struct {
		input      string
		wantCommit string
		wantState  string
	}{
		{"", "", ""},
		{"abc123", "abc123", ""},
		{"abc123-dirty", "abc123", treeStateDirty},
		{"v1.2.3-1-gabc123-dirty", "v1.2.3-1-gabc123", treeStateDirty},
	}

	for _, tc := range tests {
		gotCommit, gotState := splitDirty(tc.input)
		if gotCommit != tc.wantCommit || gotState != tc.wantState {
			t.Errorf("splitDirty(%q) = (%q, %q), want (%q, %q)",
				tc.input, gotCommit, gotState, tc.wantCommit, tc.wantState)
		}
	}
}

func TestInfoMultilineContainsAllFields(t *testing.T) {
	info := Info{
		Name:         "tool",
		Version:      "1.2.3",
		GitCommit:    "abc123",
		GitTreeState: treeStateClean,
		BuildTime:    "2026-06-17T00:00:00Z",
		GoVersion:    "go1.23.4",
		Compiler:     "gc",
		Platform:     "linux/amd64",
	}

	got := info.Multiline()
	for _, want := range []string{
		"tool:",
		"version:        1.2.3",
		"git_commit:     abc123",
		"git_tree_state: clean",
		"build_time:     2026-06-17T00:00:00Z",
		"go_version:     go1.23.4",
		"compiler:       gc",
		"platform:       linux/amd64",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Info.Multiline() missing %q in:\n%s", want, got)
		}
	}
}

func TestInfoShort(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "clean short commit",
			info: Info{Name: "tool", Version: "1.2.3", GitCommit: "abc", GitTreeState: treeStateClean},
			want: "tool 1.2.3 (abc)",
		},
		{
			name: "long commit truncated to 12",
			info: Info{Name: "tool", Version: "1.2.3", GitCommit: "abcdef0123456789", GitTreeState: treeStateClean},
			want: "tool 1.2.3 (abcdef012345)",
		},
		{
			name: "dirty appended after truncation",
			info: Info{Name: "tool", Version: "1.2.3", GitCommit: "abcdef0123456789", GitTreeState: treeStateDirty},
			want: "tool 1.2.3 (abcdef012345-dirty)",
		},
	}

	for _, tc := range tests {
		got := tc.info.Short()
		if got != tc.want {
			t.Errorf("[%s] Info.Short() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestInfoJSONRoundTrip(t *testing.T) {
	want := Info{
		Name:         "tool",
		Version:      "1.2.3",
		GitCommit:    "abc123",
		GitTreeState: treeStateClean,
		BuildTime:    "2026-06-17T00:00:00Z",
		GoVersion:    "go1.23.4",
		Compiler:     "gc",
		Platform:     "linux/amd64",
	}

	var got Info
	if err := json.Unmarshal([]byte(want.JSON()), &got); err != nil {
		t.Fatalf("json.Unmarshal(Info.JSON()) error = %v", err)
	}
	if got != want {
		t.Errorf("round-trip Info = %+v, want %+v", got, want)
	}
}

func TestInfoJSONFieldNames(t *testing.T) {
	got := Info{Name: "tool"}.JSON()
	for _, want := range []string{
		`"name"`,
		`"version"`,
		`"git_commit"`,
		`"git_tree_state"`,
		`"build_time"`,
		`"go_version"`,
		`"compiler"`,
		`"platform"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Info.JSON() missing field %s in:\n%s", want, got)
		}
	}
}
