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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildProcessFileIOStatsAggregatesBytesBeforeRounding(t *testing.T) {
	g := &pidGroup{
		Files: []*fileEntry{
			{Record: &bpfFilesystemIO{
				FsReadBytes: 4, FsWriteBytes: 4,
				BlockReadBytes: 4, BlockWriteBytes: 4,
			}},
			{Record: &bpfFilesystemIO{
				FsReadBytes: 4, FsWriteBytes: 4,
				BlockReadBytes: 4, BlockWriteBytes: 4,
			}},
		},
	}

	stats := buildProcessFileIOStats(g, ioConfig{
		durationSecond: 8, maxFilesPerProcess: 1,
	})

	assert.Equal(t, uint64(1), stats.TotalFsReadBps)
	assert.Equal(t, uint64(1), stats.TotalFsWriteBps)
	assert.Equal(t, uint64(1), stats.TotalDiskReadBps)
	assert.Equal(t, uint64(1), stats.TotalDiskWriteBps)
	assert.Equal(t, uint64(2), stats.TotalFileCount)
	if assert.Len(t, stats.TotalFiles, 1) {
		assert.Zero(t, stats.TotalFiles[0].FsReadBps)
		assert.Zero(t, stats.TotalFiles[0].FsWriteBps)
		assert.Zero(t, stats.TotalFiles[0].DiskReadBps)
		assert.Zero(t, stats.TotalFiles[0].DiskWriteBps)
	}
}
