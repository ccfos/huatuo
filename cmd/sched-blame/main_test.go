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
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStandaloneCLIRequiresOutputFile(t *testing.T) {
	app := newApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	require.ErrorContains(t, app.Run([]string{schedBlameToolName}), "output-file")
}

func TestStandaloneCLIRejectsPositionalArguments(t *testing.T) {
	app := newApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err := app.Run([]string{schedBlameToolName, "--output-file", "events.txt", "bpf/"})
	require.ErrorContains(t, err, "unexpected arguments")
}

func TestStandaloneCLIVersion(t *testing.T) {
	app := newApp()
	var stdout bytes.Buffer
	app.Writer, app.ErrWriter = &stdout, io.Discard
	require.NoError(t, app.Run([]string{schedBlameToolName, "--version"}))
	assert.Contains(t, stdout.String(), "sched-blame:")
	assert.Contains(t, stdout.String(), "git_commit:")
}

func TestStandalonePprofListenFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, listener.Close()) })
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, nil, 0o600))
	app := newApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err = app.Run([]string{
		schedBlameToolName, "--config", configPath,
		"--output-file", filepath.Join(t.TempDir(), "events.txt"),
		"--pprof-addr", listener.Addr().String(),
	})
	require.ErrorContains(t, err, "listen for pprof")
}
