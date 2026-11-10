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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

func loadConfigForTest(t *testing.T, content string, flags ...string) (*standaloneConfig, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	app := newApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	var config *standaloneConfig
	app.Action = func(ctx *cli.Context) error {
		var err error
		config, err = loadStandaloneConfig(ctx)
		return err
	}
	args := []string{schedBlameToolName, "--config", path, "--output-file", "events.txt"}
	err := app.Run(append(args, flags...))
	return config, err
}

func TestStandaloneConfigDefaults(t *testing.T) {
	config, err := loadConfigForTest(t, "")
	require.NoError(t, err)
	assert.Equal(t, "Info", config.Log.Level)
	assert.Equal(t, uint32(10255), config.Pod.KubeletReadOnlyPort)
	assert.Equal(t, uint32(10250), config.Pod.KubeletAuthorizedPort)
	assert.Equal(t, "1.24", config.Pod.DockerAPIVersion)
	assert.Equal(t, "normal", config.AutoTracing.SchedBlame.TargetContainerScope)
	assert.Equal(t, 1.0, config.AutoTracing.SchedBlame.ExternalAnomalyK)
	assert.Equal(t, uint32(128), config.AutoTracing.SchedBlame.SliceBatchSize)
	assert.Equal(t, uint32(524288), config.AutoTracing.SchedBlame.PerfEventPerCPUBufferBytes)
	assert.Equal(t, uint32(8192), config.AutoTracing.SchedBlame.PerfEventWatermarkBytes)
	assert.Equal(t, 8192, config.AutoTracing.SchedBlame.PerfEventQueueRecords)
	assert.Equal(t, uint32(100), config.AutoTracing.SchedBlame.PollIntervalMs)
}

func TestStandaloneConfigAcceptsDaemonSections(t *testing.T) {
	config, err := loadConfigForTest(t, `
[Storage.Elasticsearch]
Address = "unused"
[HTTPServer.Auth]
BearerToken = "unused"
[Pod]
KubeletReadOnlyPort = 12345
KubeletClientCertPath = "client.pem, client-key.pem"
[AutoTracing.SchedBlame]
SliceDropPercent = 0
ExternalAnomalyK = 0.0
HighlightContainer = "configured"
ExternalRatioDebugFile = "configured.csv"
`)
	require.NoError(t, err)
	assert.Equal(t, uint32(12345), config.Pod.KubeletReadOnlyPort)
	assert.Equal(t, "client.pem, client-key.pem", config.Pod.KubeletClientCertPath)
	assert.Zero(t, config.AutoTracing.SchedBlame.SliceDropPercent)
	assert.Zero(t, config.AutoTracing.SchedBlame.ExternalAnomalyK)
	assert.Equal(t, "configured", config.AutoTracing.SchedBlame.HighlightContainer)
	assert.Equal(t, "configured.csv", config.AutoTracing.SchedBlame.ExternalRatioDebugFile)
}

func TestStandaloneConfigCLIOverrides(t *testing.T) {
	const content = `
[AutoTracing.SchedBlame]
HighlightContainer = "configured"
ExternalRatioDebugFile = "configured.csv"
`
	config, err := loadConfigForTest(t, content,
		"--highlight-container", "selected", "--ratio-debug-file", "selected.csv")
	require.NoError(t, err)
	assert.Equal(t, "selected", config.AutoTracing.SchedBlame.HighlightContainer)
	assert.Equal(t, "selected.csv", config.AutoTracing.SchedBlame.ExternalRatioDebugFile)
	config, err = loadConfigForTest(t, content,
		"--highlight-container=", "--ratio-debug-file=")
	require.NoError(t, err)
	assert.Empty(t, config.AutoTracing.SchedBlame.HighlightContainer)
	assert.Empty(t, config.AutoTracing.SchedBlame.ExternalRatioDebugFile)
}

func TestStandaloneConfigRejectsInvalidSettings(t *testing.T) {
	for _, test := range []struct {
		content string
		error   string
	}{
		{"[AutoTracing.SchedBlame]\nSliceDropPercent = 100", "slice drop percent"},
		{"[AutoTracing.SchedBlame]\nPerfEventQueueRecords = 0", "queue records"},
		{"[AutoTracing.SchedBlame]\nPollIntervalMs = 1001", "poll interval"},
		{"[Pod]\nKubeletAuthorizedPort = 65536", "kubelet ports"},
		{"[Log]\nLevel = 'invalid'", "invalid log level"},
		{"[AutoTracing.SchedBlame", "decode config"},
	} {
		_, err := loadConfigForTest(t, test.content)
		require.ErrorContains(t, err, test.error)
	}
}
