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

package collector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	metricpkg "github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/procfs/xfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXFSData(t *testing.T) {
	stats := &xfs.Stats{
		Name: "sda1",
		ExtentAllocation: xfs.ExtentAllocationStats{
			BlocksAllocated:  1000,
			ExtentsAllocated: 100,
		},
		InodeOperation: xfs.InodeOperationStats{
			Attempts: 500,
			Missed:   10,
		},
		Buffer: xfs.BufferStats{
			GetLockedWaited: 30,
			BusyLocked:      40,
		},
		PushAil: xfs.PushAilStats{
			SleepLogspace: 5,
		},
	}

	data := xfsData(stats)
	require.Len(t, data, 7)

	want := map[string]float64{
		"alloc_blocks_total":      1000,
		"alloc_extents_total":     100,
		"inode_attempts_total":    500,
		"inode_missed_total":      10,
		"buf_locked_waited_total": 30,
		"buf_busy_locked_total":   40,
		"log_space_sleep_total":   5,
	}

	for _, d := range data {
		value, ok := want[d.Name()]
		require.Truef(t, ok, "unexpected metric %q", d.Name())

		assert.Equal(t, value, d.Value)
		assert.Equal(t, metricpkg.MetricTypeCounter, d.Type())
		assert.Equal(t, "sda1", d.Labels()["device"])

		fqName := prometheus.BuildFQName(metricpkg.DefaultNamespace, "xfs", d.Name())
		assert.Truef(t, len(fqName) > len("huatuo_bamai_xfs_"), "unexpected fq name %q", fqName)
	}
}

func TestNewXFSNotSupported(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(t.TempDir())

	// No /proc/fs/xfs/stat: the collector must opt out with ErrNotSupported
	// so the framework marks it inactive.
	_, err := newXFS()
	assert.ErrorIs(t, err, types.ErrNotSupported)
}

func TestNewXFSSupported(t *testing.T) {
	root := t.TempDir()
	procDir := filepath.Join(root, "proc", "fs", "xfs")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "stat"), []byte(""), 0o600))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(root)

	attr, err := newXFS()
	require.NoError(t, err)
	require.NotNil(t, attr)
	assert.Equal(t, tracing.FlagMetric, attr.Flag)
	assert.IsType(t, &xfsCollector{}, attr.TracingData)
}
