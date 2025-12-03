// Copyright 2025 The HuaTuo Authors
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

package autotracing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("iotracing", newIoTracing)
}

var IOstat ioTracing

func newIoTracing() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &IOstat,
		Internal:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o $BPF_DIR/iotracing.o

// IOStatusData the data struct need to upload to es
type IOStatusData struct {
	TrigerType int           `json:"triger_type"`
	Reason     string        `json:"reason"`
	Dev        string        `json:"dev"`
	Status     IODevStatus   `json:"status"`
	Pdata      []ProcessData `json:"pdata"`
	IOStack    []IOStack     `json:"io_stack"`
}

// IOStack record io_schedule backtrace
type IOStack struct {
	Pid               uint32       `json:"pid"`
	Comm              string       `json:"comm"`
	ContainerHostname string       `json:"container_hostname"`
	Latency           uint64       `json:"latency_us"`
	Stack             symbol.Stack `json:"stack"`
}

// ProcessData record same process info
type ProcessData struct {
	Pid               uint32   `json:"pid"`
	Comm              string   `json:"comm"`
	ContainerHostname string   `json:"container_hostname"`
	FsRead            uint64   `json:"fs_read"`
	FsWrite           uint64   `json:"fs_write"`
	DiskRead          uint64   `json:"disk_read"`
	DiskWrite         uint64   `json:"disk_write"`
	FileStat          []string `json:"file_stat"`
	FileCount         uint32   `json:"file_count"`
}

// IODevStatus record the io status when the collection is triggered
type IODevStatus struct {
	RThroughput uint64 `json:"read_bps"`
	WThroughput uint64 `json:"write_bps"`
	Riowait     uint64 `json:"read_iowait"`
	Wiowait     uint64 `json:"write_iowait"`
	IOutil      uint64 `json:"io_util"`
	QueueSize   uint64 `json:"queue_size"`
	Riops       uint64 `json:"read_iops"`
	Wiops       uint64 `json:"write_iops"`
}

type ioTracing struct {
	fileAlarmString string
	esData          IOStatusData
	config          ioStatConfig
}

type ioStatConfig struct {
	readThreshold   uint64
	writeThreshold  uint64
	ioutilThreshold uint64
	iowaitThreshold uint64
	periodSecond    uint64
}

// LatencyInfo io latency info
type LatencyInfo struct {
	Count  uint64
	MaxD2C uint64
	SumD2C uint64
	MaxQ2C uint64
	SumQ2C uint64
}

// IOBpfData bpf data for io_source_map
type IOBpfData struct {
	Tgid            uint32
	Pid             uint32
	Dev             uint32
	Flag            uint32
	FsWriteBytes    uint64
	FsReadBytes     uint64
	BlockWriteBytes uint64
	BlockReadBytes  uint64
	InodeNum        uint64
	Blkcg           uint64
	Latency         LatencyInfo
	Comm            [16]byte
	FileName        [64]byte
	Dentry1Name     [64]byte
	Dentry2Name     [64]byte
	Dentry3Name     [64]byte
}

// IODelayData io schedule info from iodelay_perf_events
type IODelayData struct {
	Stack     [symbol.KsymbolStackMinDepth]uint64
	TimeStamp uint64
	Cost      uint64
	StackSize uint32
	Pid       uint32
	Tid       uint32
	CPU       uint32
	Comm      [16]byte
}

type ioDevStat struct {
	dev         string
	disk        string
	rios        uint64
	rsector     uint64
	rticks      uint64
	wios        uint64
	wsector     uint64
	wticks      uint64
	ioTicks     uint64
	timeInQueue uint64
	_rmerge     uint64
	_wmerge     uint64
	_inFlight   uint64
}

const (
	ioTrigerNone = iota
	ioTrigerUtilFull
	ioTrigerReadFull
	ioTrigerWriteFull
	ioTrigerReadLatency
	ioTrigerWriteLatency
)

func parseDiskStatsData(data []byte) []ioDevStat {
	var devData []ioDevStat
	dataSlice := strings.Split(string(data), "\n")
	for _, str := range dataSlice {
		var d ioDevStat
		item := strings.Fields(str)
		// Kernel 4.18-5.4 has 18 fields, kernel 5.5+ has 20 fields
		if len(item) != 18 && len(item) != 20 {
			continue
		}
		if strings.HasPrefix(item[2], "md") {
			continue
		}
		d.dev = item[0] + ":" + item[1]
		d.disk = item[2]
		d.rios, _ = strconv.ParseUint(item[3], 10, 64)
		d.rsector, _ = strconv.ParseUint(item[5], 10, 64)
		d.rticks, _ = strconv.ParseUint(item[6], 10, 64)
		d.wios, _ = strconv.ParseUint(item[7], 10, 64)
		d.wsector, _ = strconv.ParseUint(item[9], 10, 64)
		d.wticks, _ = strconv.ParseUint(item[10], 10, 64)
		d.ioTicks, _ = strconv.ParseUint(item[12], 10, 64)
		d.timeInQueue, _ = strconv.ParseUint(item[13], 10, 64)
		d._inFlight, _ = strconv.ParseUint(item[11], 10, 64)
		d._rmerge, _ = strconv.ParseUint(item[4], 10, 64)
		d._wmerge, _ = strconv.ParseUint(item[8], 10, 64)
		devData = append(devData, d)
	}
	return devData
}

// Check I/O status and determine whether I/O field collection needs to be triggered.
// The data is judged twice, and if both exceed the threshold, it indicates that the abnormal condition
// lasted for at least 2 seconds. The judging conditions are as follows:
//
//	io.util > threshold: If the disk is an nvme disk, the read/write bandwidth is less than 20MB/s,
//	it is considered an exception, or a large number of I/OS (read >2000MB/s or write >1500MB/s) may occur.
//	other types of disks are used, data collection is directly triggered.
//	the I/O latency is determined by checking whether the read/write latency exceeds the threshold
func checkThreshold(disk string, old, now IODevStatus) (int, string) {
	if old.IOutil > IOstat.config.ioutilThreshold && now.IOutil > IOstat.config.ioutilThreshold {
		if strings.HasPrefix(disk, "nvme") {
			if old.RThroughput > IOstat.config.readThreshold*1024*1024 && now.RThroughput > IOstat.config.readThreshold*1024*1024 {
				return ioTrigerReadFull, fmt.Sprintf("io.util %d,%d > %d, and read throughput %d > 2000MB/s", old.IOutil, now.IOutil, IOstat.config.ioutilThreshold, now.RThroughput)
			}
			if old.WThroughput > IOstat.config.writeThreshold*1024*1024 && now.WThroughput > IOstat.config.writeThreshold*1024*1024 {
				return ioTrigerWriteFull, fmt.Sprintf("io.util %d,%d > %d, and write throughput %d > 1500MB/s", old.IOutil, now.IOutil, IOstat.config.ioutilThreshold, now.WThroughput)
			}
		} else {
			return ioTrigerUtilFull, fmt.Sprintf("%s:io.util %d,%d > %d", disk, old.IOutil, now.IOutil, IOstat.config.ioutilThreshold)
		}
	}

	if old.Riowait/1000 > IOstat.config.iowaitThreshold && now.Riowait/1000 > IOstat.config.iowaitThreshold {
		return ioTrigerReadLatency, fmt.Sprintf("read iowait %d,%d > %dms ", old.Riowait/1000, now.Riowait/1000, IOstat.config.iowaitThreshold)
	}
	if old.Wiowait/1000 > IOstat.config.iowaitThreshold && now.Wiowait/1000 > IOstat.config.iowaitThreshold {
		return ioTrigerWriteLatency, fmt.Sprintf("write iowait %d,%d > %dms ", old.Wiowait/1000, now.Wiowait/1000, IOstat.config.iowaitThreshold)
	}
	return ioTrigerNone, ""
}

func detectDiskStats(ctx context.Context) error {
	lastDevIOData := make(map[string]ioDevStat)
	lastDevIOStatus := make(map[string]IODevStatus)
	ticker := time.NewTicker(1 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-ticker.C:
			diskStats, err := os.ReadFile("/proc/diskstats")
			if err != nil {
				return err
			}

			devIOData := parseDiskStatsData(diskStats)
			for _, data := range devIOData {
				if old, ok := lastDevIOData[data.dev]; ok {
					var status IODevStatus
					status.Riops = data.rios - old.rios
					status.Wiops = data.wios - old.wios
					if status.Riops > 0 {
						status.Riowait = (data.rticks - old.rticks) * 1000 / status.Riops // us
					}
					if status.Wiops > 0 {
						status.Wiowait = (data.wticks - old.wticks) * 1000 / status.Wiops
					}
					status.RThroughput = (data.rsector - old.rsector) * 512
					status.WThroughput = (data.wsector - old.wsector) * 512
					status.IOutil = 100 * (data.ioTicks - old.ioTicks) / 1000            // time period 1000ms
					status.QueueSize = 100 * (data.timeInQueue - old.timeInQueue) / 1000 // aqu-sz*100

					tType, reason := checkThreshold(data.disk, lastDevIOStatus[data.dev], status)
					if tType != ioTrigerNone {
						IOstat.esData.TrigerType = tType
						IOstat.esData.Reason = fmt.Sprintf("[%s %s]: %s", data.disk, data.dev, reason)
						IOstat.esData.Dev = data.disk + " " + data.dev
						IOstat.esData.Status = status
						IOstat.fileAlarmString = fmt.Sprintf("#iotracer# dev=[%s %s] reason=[%s] r=%dbytes/s w=%dbytes/s r.iowait=%dus w.iowait=%dus io.util=%d aqu-sz=%d\n",
							data.disk, data.dev, reason, status.RThroughput, status.WThroughput, status.Riowait, status.Wiowait, status.IOutil, status.QueueSize)
						return nil
					}
					lastDevIOStatus[data.dev] = status
				}
				lastDevIOData[data.dev] = data
			}
		}
	}
}

func loadConfig() {
	IOstat.config.ioutilThreshold = conf.Get().AutoTracing.IOTracing.UtilThreshold
	IOstat.config.iowaitThreshold = conf.Get().AutoTracing.IOTracing.WaitThreshold
	IOstat.config.readThreshold = conf.Get().AutoTracing.IOTracing.ReadThreshold
	IOstat.config.writeThreshold = conf.Get().AutoTracing.IOTracing.WriteThreshold

	IOstat.config.periodSecond = conf.Get().AutoTracing.IOTracing.PeriodSecond
	if IOstat.config.periodSecond == 0 {
		IOstat.config.periodSecond = 10
	}
	IOstat.esData = IOStatusData{}
	log.Debugf("iotracer.config: %+v\n", IOstat.config)
}

func startIOTracerWork(ctx context.Context) error {
	loadConfig()

	// Detect the I/O status and determine whether data collection is required
	if err := detectDiskStats(ctx); err != nil {
		return err
	}

	taskID := tracing.NewTask("iotracing", 40*time.Second, tracing.TaskStorageStdout, []string{"--json"})

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-time.After(1 * time.Second):
			result := tracing.Result(taskID)
			if result.TaskStatus == tracing.StatusCompleted {
				if result.TaskErr != nil {
					return fmt.Errorf("task error: %w", result.TaskErr)
				}
				var ioStatusData IOStatusData
				err := json.Unmarshal(result.TaskData, &ioStatusData)
				if err != nil {
					return fmt.Errorf("failed to unmarshal ioStatusData: %w", err)
				}
				submitData(&ioStatusData)
				return nil
			}

			if result.TaskStatus == tracing.StatusFailed {
				return fmt.Errorf("task failed: %w", result.TaskErr)
			}
		}
	}
}

func submitData(ioStatusData *IOStatusData) {
	IOstat.esData.Pdata = ioStatusData.Pdata
	IOstat.esData.IOStack = ioStatusData.IOStack

	log.Info(IOstat.fileAlarmString)
	log.Debugf("submitData: %+v\n", IOstat.esData)
	storage.Save("iotracer", "", time.Now(), &IOstat.esData)
}

// Start do the io tracer work
func (c *ioTracing) Start(ctx context.Context) error {
	return startIOTracerWork(ctx)
}
