// Copyright 2025, 2026 The HuaTuo Authors
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

// Collect per-device XFS metrics from sysfs. Each update shares one statistics
// snapshot between the counters and log-space calculation, without caching it.
package collector

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs/dev"
	"github.com/ccfos/huatuo/internal/procfs/xfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
)

const metricsPerDevice = 8

const (
	XFS_SB_MAGIC = 0x58465342
	XFSLABEL_MAX = 12
)

// xfsDevLogSizes stores the maximum log size (in bytes) for each XFS device
var (
	xfsLogSizeMap map[string]float64
)

type xfsCollector struct {
	deviceMetrics map[string][]*metric.Data
}

// Only the magic number, block size and log block count are named;
// unused superblock fields remain padding.
type xfsSuperBlock struct {
	SbMagicnum  uint32
	SbBlocksize uint32
	_           [16]byte
	_           [7]uint64
	_           [4]uint32
	SbLogblocks uint32
	_           [6]uint16
	_           [XFSLABEL_MAX]byte
	_           [12]uint8
	_           [8]uint64
	_           [12]uint32
	_           [16]byte
}

func init() {
	tracing.RegisterEventTracing("xfs", newXFSCollector)
}

func newXFSCollector() (*tracing.EventTracingAttr, error) {
	statsList, err := getXFSStatsList()
	if err != nil {
		log.Errorf("XFS filesystem may not exists, err : %v", err)
		return nil, err
	}

	devMetrics := make(map[string][]*metric.Data)
	for _, stats := range statsList {
		device := stats.Name
		metrics := make([]*metric.Data, 0, metricsPerDevice)
		label := map[string]string{"device": device}

		metricMetas := []struct {
			name        string
			description string
		}{
			{"log_free_bytes", "Available space in XFS log space"},
			{"log_space_sleep_total", "Total sleep times in XFS log space"},
			{"alloc_blocks_total", "Blocks allocated on XFS filesystem"},
			{"alloc_extents_total", "Extents allocated on XFS filesystem"},
			{"inode_missed_total", "Missed times in XFS inode cache"},
			{"inode_attempts_total", "Attempt times in XFS inode cache"},
			{"buf_busy_locked_total", "Busy locked times in XFS buffer"},
			{"buf_locked_waited_total", "Waited times in XFS locked buffer"},
		}

		for _, meta := range metricMetas {
			newData := metric.NewCounterData
			if meta.name == "log_free_bytes" {
				newData = metric.NewGaugeData
			}
			metrics = append(metrics, newData(
				meta.name,
				0,
				meta.description,
				label,
			))
		}
		devMetrics[device] = metrics
	}

	xfsLogSizeMap = getXFSLogMaxSize(statsList)

	return &tracing.EventTracingAttr{
		TracingData: &xfsCollector{
			deviceMetrics: devMetrics,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *xfsCollector) Update() ([]*metric.Data, error) {
	statsList, err := getXFSStatsList()
	if err != nil {
		log.Warnf("failed to get XFS system statslist: %v", err)
		return nil, err
	}

	logFreeSizeMap := getXFSLogFreeSize(statsList)
	allMetrics := make([]*metric.Data, 0, len(statsList)*metricsPerDevice)
	snapshots := make([]metric.Data, len(statsList)*metricsPerDevice)

	for _, devStats := range statsList {
		devName := devStats.Name
		dMetrics, exists := c.deviceMetrics[devName]
		if !exists || len(dMetrics) != 8 {
			log.Warnf("skipping device %s (metrics not initialized)", devName)
			continue
		}

		logFreeSize, hasLogFreeSize := logFreeSizeMap[devName]
		values := [metricsPerDevice]float64{
			// XFS log Metrics
			logFreeSize,
			float64(devStats.PushAil.SleepLogspace),

			// Measures XFS fragmentation via blocks/extent ratio:
			// xfs_avg_blocks_per_extent = increase(xfs_alloc_blocks_total)/increase(xfs_alloc_extents_total)
			float64(devStats.ExtentAllocation.BlocksAllocated),
			float64(devStats.ExtentAllocation.ExtentsAllocated),

			// Measures XFS inode cache hit rate via (attempts - missed)/attempts:
			// xfs_inode_cache_hit_percent = (increase(attempts_total) - increase(missed_total))/increase(attempts_total)
			float64(devStats.InodeOperation.Missed),
			float64(devStats.InodeOperation.Attempts),

			// Retrieve the following XFS buffer metrics and calculate their growth rates:
			// xfs_buf_locked_waited_rate = rate(xfs_buf_locked_waited_total)
			// xfs_buf_busy_locked_rate = rate(xfs_buf_busy_locked_total)
			float64(devStats.Buffer.BusyLocked),
			float64(devStats.Buffer.GetLockedWaited),
		}

		// Consumers read after Update returns; only immutable metadata is shared.
		for i, m := range dMetrics {
			if i == 0 && !hasLogFreeSize {
				// An unavailable log reading is not zero free space.
				continue
			}
			snapshot := &snapshots[len(allMetrics)]
			*snapshot = *m
			snapshot.Value = values[i]
			allMetrics = append(allMetrics, snapshot)
		}
	}

	return allMetrics, nil
}

func getXFSStatsList() ([]*xfs.Stats, error) {
	fs, err := xfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	return fs.SysStats()
}

// getXLogMaxBytes returns the maximum xlog size (in bytes) for each XFS device
func getXFSLogMaxSize(statsList []*xfs.Stats) map[string]float64 {
	logMaxSizeMap := make(map[string]float64)

	for _, stats := range statsList {
		name := stats.Name
		size, err := xfsLogSize(dev.Path(name))
		if err != nil {
			continue
		}

		logMaxSizeMap[name] = size
	}

	return logMaxSizeMap
}

// Calculate the Xlog size from superblock
func xfsLogSize(path string) (float64, error) {
	file, err := os.Open(path)
	if err != nil {
		log.Infof("open failed: %v", err)
		return -1, err
	}
	defer file.Close()

	var sb xfsSuperBlock
	err = binary.Read(file, binary.BigEndian, &sb)
	if err != nil {
		log.Infof("read superblock failed: err%v", err)
		return -1, err
	}

	// Check Magic Number of Super Block
	if sb.SbMagicnum != XFS_SB_MAGIC {
		log.Infof("Not a valid XFS superblock (Magic: 0x%x)", sb.SbMagicnum)
		return -1, fmt.Errorf("invalid XFS superblock magic: 0x%x", sb.SbMagicnum)
	}

	xlogBytes := float64(sb.SbLogblocks * sb.SbBlocksize)
	return xlogBytes, nil
}

// devXLogFreeBytes returns the available xlog space (in bytes) for each XFS device
func getXFSLogFreeSize(statsList []*xfs.Stats) map[string]float64 {
	logFreeSizeMap := make(map[string]float64)

	for _, stats := range statsList {
		dev := stats.Name
		maxSize, exists := xfsLogSizeMap[dev]
		if !exists {
			log.Infof("max log size not found for device: %s", dev)
			continue
		}

		freeSize, err := calcuXlogFreeBytes(xfs.Path(dev, "log"), maxSize)
		if err != nil {
			log.Infof("calculate xlog free bytes failed for %s, err: %v", dev, err)
			continue
		}

		logFreeSizeMap[dev] = freeSize
	}

	return logFreeSizeMap
}

// Follow xlog_space_left: only the next cycle is a single wrap. An overrun
// leaves no free space; a head behind the tail is treated as an empty log.
func calcuXlogFreeBytes(logPath string, maxSize float64) (float64, error) {
	logFiles := map[string]string{
		"tail":  filepath.Join(logPath, "log_tail_lsn"),
		"rhead": filepath.Join(logPath, "reserve_grant_head"),
	}

	for name, path := range logFiles {
		if _, err := os.Stat(path); err != nil {
			log.Infof("%s file not found, err: %v", name, err)
			return -1, err
		}
	}

	tCycle, tBlocks, err := readCycleValue(logFiles["tail"])
	if err != nil {
		log.Infof("read log_tail_lsn failed, err: %v", err)
		return -1, err
	}

	rhCycle, rhBytes, err := readCycleValue(logFiles["rhead"])
	if err != nil {
		log.Infof("read reserve_grant_head failed, err: %v", err)
		return -1, err
	}

	tailBytes := tBlocks * 512
	if tCycle == rhCycle && rhBytes >= tailBytes {
		return maxSize - (rhBytes - tailBytes), nil
	}
	if tCycle+1 < rhCycle {
		return 0, nil
	}
	if tCycle < rhCycle {
		return tailBytes - rhBytes, nil
	}
	return maxSize, nil
}

func readCycleValue(filePath string) (float64, float64, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Errorf("read file failed, err: %v", err)
		return -1, -1, err
	}

	line := strings.TrimSpace(string(data))
	parts := strings.Split(line, ":")
	if len(parts) != 2 {
		return -1, -1, fmt.Errorf("invalid format")
	}

	cycle, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		log.Infof("invalid cycle value, err: %v", err)
		return -1, -1, err
	}

	value, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		log.Infof("invalid numeric value, err: %v", err)
		return -1, -1, err
	}

	return cycle, value, nil
}
