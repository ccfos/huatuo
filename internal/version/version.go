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

// Package version centralizes CLI build/version metadata across cmd/*.
// It mirrors kubectl's version.Info schema so downstream tooling can
// parse the JSON form interchangeably.
package version

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/urfave/cli/v2"
)

// Devel is the placeholder used when no value is injected via -ldflags
// and runtime/debug.ReadBuildInfo cannot supply one (e.g. `go run`,
// `go test`, ad-hoc local builds).
const Devel = "(devel)"

const (
	treeStateClean   = "clean"
	treeStateDirty   = "dirty"
	treeStateUnknown = "unknown"
)

const versionFormatFlag = "version-format"

// Info captures CLI release identity. Field order mirrors kubectl's
// version.Info to ease integration with downstream tooling.
type Info struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	GitCommit    string `json:"git_commit"`
	GitTreeState string `json:"git_tree_state"`
	BuildTime    string `json:"build_time"`
	GoVersion    string `json:"go_version"`
	Compiler     string `json:"compiler"`
	Platform     string `json:"platform"`
}

// Seed carries values injected by the Makefile via -ldflags -X. Empty
// fields are backfilled from runtime/debug.ReadBuildInfo when available.
type Seed struct {
	Name      string
	Version   string
	GitCommit string
	BuildTime string
}

// Resolve returns Info populated from Seed first, then runtime/debug,
// then runtime constants. Missing string values fall back to Devel so
// callers always get a renderable value.
func Resolve(s Seed) Info {
	info := Info{
		Name:      s.Name,
		Version:   s.Version,
		BuildTime: s.BuildTime,
		GoVersion: runtime.Version(),
		Compiler:  runtime.Compiler,
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	// Makefile uses `git describe --dirty` whose output may carry a
	// "-dirty" suffix; split it before any backfill so the Seed wins
	// over debug.BuildInfo's vcs.modified.
	info.GitCommit, info.GitTreeState = splitDirty(s.GitCommit)

	if info.GitCommit == "" || info.BuildTime == "" || info.GitTreeState == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			backfillFromBuildInfo(&info, bi)
		}
	}

	if info.Version == "" {
		info.Version = Devel
	}
	if info.GitCommit == "" {
		info.GitCommit = Devel
	}
	if info.BuildTime == "" {
		info.BuildTime = Devel
	}
	if info.GitTreeState == "" {
		info.GitTreeState = treeStateUnknown
	}

	return info
}

func splitDirty(commit string) (string, string) {
	if commit == "" {
		return "", ""
	}

	if strings.HasSuffix(commit, "-dirty") {
		return strings.TrimSuffix(commit, "-dirty"), treeStateDirty
	}

	return commit, ""
}

func backfillFromBuildInfo(i *Info, bi *debug.BuildInfo) {
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			if i.GitCommit == "" {
				i.GitCommit = kv.Value
			}
		case "vcs.time":
			if i.BuildTime == "" {
				i.BuildTime = kv.Value
			}
		case "vcs.modified":
			if i.GitTreeState != "" {
				continue
			}

			if kv.Value == "true" {
				i.GitTreeState = treeStateDirty
			} else {
				i.GitTreeState = treeStateClean
			}
		}
	}
}

// Multiline returns a kubectl-style multi-line block, suitable as
// urfave/cli's app.Version output.
func (i Info) Multiline() string {
	return strings.Join([]string{
		i.Name + ":",
		fmt.Sprintf("   version:        %s", i.Version),
		fmt.Sprintf("   git_commit:     %s", i.GitCommit),
		fmt.Sprintf("   git_tree_state: %s", i.GitTreeState),
		fmt.Sprintf("   build_time:     %s", i.BuildTime),
		fmt.Sprintf("   go_version:     %s", i.GoVersion),
		fmt.Sprintf("   compiler:       %s", i.Compiler),
		fmt.Sprintf("   platform:       %s", i.Platform),
	}, "\n")
}

// Short returns "<name> <version> (<short-commit>[-dirty])".
func (i Info) Short() string {
	commit := i.GitCommit

	const shortLen = 12
	if len(commit) > shortLen {
		commit = commit[:shortLen]
	}

	if i.GitTreeState == treeStateDirty {
		commit += "-dirty"
	}

	return fmt.Sprintf("%s %s (%s)", i.Name, i.Version, commit)
}

// JSON returns the canonical indented JSON serialization.
func (i Info) JSON() string {
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return "{}"
	}

	return string(b)
}

// Wire resolves Seed, sets app.Version to the multi-line form, registers
// the global --version-format flag (text|json|short), and overrides
// cli.VersionPrinter so `-v` honors the chosen format. Returns the
// resolved Info so callers can reuse it (e.g. as a toolstream version).
//
// Must be called after the caller has populated app.Flags, since Wire
// appends to that slice.
func Wire(app *cli.App, s Seed) Info {
	info := Resolve(s)
	app.Version = info.Multiline()
	app.Flags = append(app.Flags, &cli.StringFlag{
		Name:  versionFormatFlag,
		Value: "text",
		Usage: "version output format: text|json|short (only used with --version)",
	})

	cli.VersionPrinter = func(c *cli.Context) {
		printVersion(c.App.Writer, info, c.String(versionFormatFlag))
	}

	return info
}

func printVersion(w io.Writer, i Info, format string) {
	switch format {
	case "json":
		fmt.Fprintln(w, i.JSON())
	case "short":
		fmt.Fprintln(w, i.Short())
	default:
		fmt.Fprintln(w, i.Multiline())
	}
}
