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

package storage

import (
	"errors"
	"os"
	"testing"
	"time"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage/elasticsearch"
	"huatuo-bamai/internal/storage/localfile"
	"huatuo-bamai/internal/storage/null"
	"huatuo-bamai/internal/storage/types"

	mock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Test Main: Restore global variables uniformly to prevent test pollution.
func TestMain(m *testing.M) {
	// Backup original global variables
	origesExporter := esExporter
	origLocalExporter := localFileExporter
	origStorageInitCtx := storageInitCtx
	origContainerLookup := containerLookupFunc

	// Restore globals after all tests finish
	defer func() {
		esExporter = origesExporter
		localFileExporter = origLocalExporter
		storageInitCtx = origStorageInitCtx
		containerLookupFunc = origContainerLookup
	}()

	// Run tests
	exitCode := m.Run()
	os.Exit(exitCode)
}

// TestInitDefaultClients_InvalidConfig verifies InitDefaultClients handles empty config gracefully.
func TestInitDefaultClients_InvalidConfig(t *testing.T) {
	ctx := &InitContext{}
	require.NoError(t, InitDefaultClients(ctx))

	require.IsType(t, &null.StorageClient{}, esExporter)
	require.IsType(t, &null.StorageClient{}, localFileExporter)
}

// TestInitDefaultClients_OnlyLocal tests initializing only local file exporter.
func TestInitDefaultClients_OnlyLocal(t *testing.T) {
	ctx := &InitContext{
		LocalPath:         "/tmp/test.log",
		LocalRotationSize: 10,
		LocalMaxRotation:  3,
		Region:            "cn",
		Hostname:          "host-1",
	}

	require.NoError(t, InitDefaultClients(ctx))

	require.IsType(t, &localfile.StorageClient{}, localFileExporter)
	require.IsType(t, &null.StorageClient{}, esExporter)
	require.Equal(t, "cn", storageInitCtx.Region)
}

// TestInitDefaultClients_OnlyES skips testing ES client initialization due to missing real ES environment.
func TestInitDefaultClients_OnlyES(t *testing.T) {
	t.Skip("TODO: No real ES environment available yet, skipping this test.")

	ctx := &InitContext{
		EsAddresses: "http://es",
		EsUsername:  "u",
		EsPassword:  "p",
		EsIndex:     "idx",
	}

	require.NoError(t, InitDefaultClients(ctx))

	require.IsType(t, &elasticsearch.StorageClient{}, esExporter)
	require.IsType(t, &null.StorageClient{}, localFileExporter)
}

// TestSave_Success verifies that Save calls both exporters successfully.
func TestSave_Success(t *testing.T) {
	es := NewMockWriter(t)
	local := NewMockWriter(t)

	esExporter = es
	localFileExporter = local

	matcher := mock.MatchedBy(func(doc *types.Document) bool {
		return doc.TracerRunType == docTracerRunAuto
	})

	es.On("Write", matcher).Return(nil).Once()
	local.On("Write", matcher).Return(nil).Once()

	Save("cpu", "", time.Now(), "data")

	es.AssertExpectations(t)
	local.AssertExpectations(t)
}

// TestSave_WriteErrors tests that Save does not panic
// and correctly calls exporters under different error scenarios.
func TestSave_WriteErrors(t *testing.T) {
	tests := []struct {
		name     string
		esErr    error
		localErr error
	}{
		{
			name:     "both exporters return errors",
			esErr:    errors.New("es down"),
			localErr: errors.New("disk full"),
		},
		{
			name:     "es exporter returns error",
			esErr:    errors.New("es write failed"),
			localErr: nil,
		},
		{
			name:     "local exporter returns error",
			esErr:    nil,
			localErr: errors.New("local write failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es := NewMockWriter(t)
			local := NewMockWriter(t)

			esExporter = es
			localFileExporter = local

			es.On("Write", mock.Anything).Return(tt.esErr).Once()
			local.On("Write", mock.Anything).Return(tt.localErr).Once()

			require.NotPanics(t, func() {
				Save("cpu", "", time.Now(), "data")
			})

			es.AssertExpectations(t)
			local.AssertExpectations(t)
		})
	}
}

// TestSave_ContainerNotExist verifies Save skips writing when container is not found.
func TestSave_ContainerNotExist(t *testing.T) {
	orig := containerLookupFunc
	defer func() { containerLookupFunc = orig }()

	containerLookupFunc = func(id string) (*pod.Container, error) {
		return nil, errors.New("not found")
	}

	es := NewMockWriter(t)
	local := NewMockWriter(t)

	esExporter = es
	localFileExporter = local

	Save("cpu", "bad-id", time.Now(), "data")

	es.AssertNotCalled(t, "Write", mock.Anything)
	local.AssertNotCalled(t, "Write", mock.Anything)
}

// TestSaveTaskOutput checks SaveTaskOutput writes the expected task output document.
func TestSaveTaskOutput_Success(t *testing.T) {
	es := NewMockWriter(t)
	esExporter = es

	es.On("Write", mock.MatchedBy(func(doc *types.Document) bool {
		data := doc.TracerData.(*TracerBasicData)
		return doc.TracerRunType == docTracerRunTask &&
			doc.TracerID == "task-123" &&
			data.Output == "hello"
	})).Return(nil).Once()

	SaveTaskOutput("disk", "task-123", "", time.Now(), "hello")
}

func TestSaveTaskOutput_ESWriteErrorLogged(t *testing.T) {
	es := NewMockWriter(t)
	esExporter = es

	es.On("Write", mock.Anything).Return(errors.New("es write failed")).Once()

	require.NotPanics(t, func() {
		SaveTaskOutput("cpu", "task-err", "", time.Now(), "output")
	})

	es.AssertExpectations(t)
}

// TestSaveTaskJSONOutput_Success verifies SaveTaskJSONOutput writes a valid JSON output.
func TestSaveTaskJSONOutput_Success(t *testing.T) {
	es := NewMockWriter(t)
	esExporter = es

	es.On("Write", mock.MatchedBy(func(doc *types.Document) bool {
		m, ok := doc.TracerData.(map[string]any)
		return ok &&
			doc.TracerRunType == docTracerRunTask &&
			doc.TracerID == "task-2" &&
			m["status"] == "ok"
	})).Return(nil).Once()

	SaveTaskJSONOutput("cpu", "task-2", "", time.Now(), `{"status":"ok"}`)
}

// TestSaveTaskJSONOutput_EdgeCases tests SaveTaskJSONOutput behavior
// under different JSON input scenarios.
func TestSaveTaskJSONOutput_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		jsonInput     string
		expectWrite   bool
		matchDocument func(*types.Document) bool
	}{
		{
			name:        "invalid json string",
			jsonInput:   `{"broken`,
			expectWrite: false,
		},
		{
			name:        "empty json string",
			jsonInput:   "",
			expectWrite: false,
		},
		{
			name:        "empty json object",
			jsonInput:   "{}",
			expectWrite: true,
			matchDocument: func(doc *types.Document) bool {
				m, ok := doc.TracerData.(map[string]any)
				return ok && len(m) == 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es := NewMockWriter(t)
			esExporter = es

			if tt.expectWrite {
				es.On("Write", mock.MatchedBy(tt.matchDocument)).
					Return(nil).
					Once()
			}

			SaveTaskJSONOutput("cpu", "task-1", "", time.Now(), tt.jsonInput)

			if tt.expectWrite {
				es.AssertExpectations(t)
			} else {
				es.AssertNotCalled(t, "Write", mock.Anything)
			}
		})
	}
}

// TestCreateBaseDocument_NoContainer verifies base document creation without container info.
func TestCreateBaseDocument_NoContainer(t *testing.T) {
	storageInitCtx = InitContext{
		Region:   "cn",
		Hostname: "host",
	}

	ts := time.Unix(100, 0)
	doc := createBaseDocument("cpu", "", ts, map[string]any{"load": 1})

	require.NotNil(t, doc)
	require.Equal(t, "cpu", doc.TracerName)
	require.Equal(t, "cn", doc.Region)
	require.Equal(t, "host", doc.Hostname)
	require.Equal(t, doc.TracerTime, doc.Time)
	require.Equal(t, map[string]any{"load": 1}, doc.TracerData)
}

// TODO(container):
// Container metadata construction and integration have not started yet.
// Container-related behavior is intentionally not tested at this stage.
// This test is a placeholder and will be enabled once container logic is implemented.
func TestSave_WithContainer(t *testing.T) {
	t.Skip("container-related behavior test Save skipped first")
}

func TestSaveTaskOutput_WithContainer(t *testing.T) {
	t.Skip("container-related behavior test SaveTaskOutput skipped first")
}

func TestSaveTaskJSONOutput_WithContainer(t *testing.T) {
	t.Skip("container-related behavior test SaveTaskJSONOutput skipped first")
}

func TestCreateBaseDocument_ContainerFound(t *testing.T) {
	t.Skip("container-related behavior test createBaseDocument skipped first")
}
