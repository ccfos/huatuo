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
	"bufio"
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/tracing"

	"github.com/shirou/gopsutil/process"
)

func init() {
	tracing.RegisterEventTracing("membust", newMemBurst)
}

func newMemBurst() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memBurstTracing{},
		Internal:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

type memBurstTracing struct{}

type MemoryTracingData struct {
	TopMemoryUsage []ProcessMemoryInfo `json:"top_memory_usage"`
}

// ProcessMemoryInfo holds process information for sorting
type ProcessMemoryInfo struct {
	PID         int32  `json:"pid"`
	ProcessName string `json:"process_name"`
	MemorySize  uint64 `json:"memory_size"`
}

// ByMemory is used to sorting processes by memory usage
type ByMemory []ProcessMemoryInfo

func (a ByMemory) Len() int           { return len(a) }
func (a ByMemory) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByMemory) Less(i, j int) bool { return a[i].MemorySize > a[j].MemorySize }

// getTopMemoryProcesses returns the top N processes consuming the most memory.
func getTopMemoryProcesses(topN int) ([]ProcessMemoryInfo, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, err
	}

	var pmInfos []ProcessMemoryInfo
	for _, p := range processes {
		memInfo, err := p.MemoryInfo()
		if err != nil {
			continue
		}
		name, err := p.Name()
		if err != nil {
			continue
		}
		pmInfos = append(pmInfos, ProcessMemoryInfo{
			PID:         p.Pid,
			ProcessName: name,
			MemorySize:  memInfo.RSS,
		})
	}

	// Sort the processes by memory usage
	sort.Sort(ByMemory(pmInfos))

	if len(pmInfos) < topN {
		return pmInfos, nil
	}
	return pmInfos[:topN], nil
}

// pass required keys and readMemInfo will return their values according to /proc/meminfo
func readMemInfo(requiredKeys map[string]bool) (map[string]int, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	results := make(map[string]int)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := strings.Trim(fields[0], ":")
		if _, ok := requiredKeys[key]; ok {
			value, err := strconv.Atoi(strings.Trim(fields[1], " kB"))
			if err != nil {
				return nil, err
			}
			results[key] = value

			if len(results) == len(requiredKeys) {
				break
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func checkAndRecordMemoryUsage(currentIndex *int, isHistoryFull *bool,
	memTotal int, history []int, historyWindowLength, topNProcesses int,
	burstRatio float64, anonThreshold int,
) ([]ProcessMemoryInfo, error) {
	memInfo, err := readMemInfo(map[string]bool{
		"Active(anon)":   true,
		"Inactive(anon)": true,
	})
	if err != nil {
		log.Errorf("Error reading memory info: %v\n", err)
		return []ProcessMemoryInfo{}, nil
	}

	currentSum := memInfo["Active(anon)"] + memInfo["Inactive(anon)"]
	history[*currentIndex] = currentSum

	if *currentIndex == historyWindowLength-1 {
		*isHistoryFull = true
	}

	*currentIndex = (*currentIndex + 1) % historyWindowLength

	log.Debugf("Checked memory status. active_anon=%v KiB inactive_anon=%v KiB\n", memInfo["Active(anon)"], memInfo["Inactive(anon)"])

	if *isHistoryFull {
		oldestSum := history[*currentIndex] // current index is the oldest element
		if float64(currentSum) >= burstRatio*float64(oldestSum) && currentSum >= (anonThreshold*memTotal/100) {
			topProcesses, err := getTopMemoryProcesses(topNProcesses)
			if err == nil {
				return topProcesses, nil
			}
			log.Errorf("Fail to getTopMemoryProcesses")
			return []ProcessMemoryInfo{}, err
		}
	}
	return []ProcessMemoryInfo{}, nil
}

// Core function
func (c *memBurstTracing) Start(ctx context.Context) error {
	var err error

	historyWindowLength := conf.Get().AutoTracing.MemoryBurst.SlidingWindowLength
	sampleInterval := conf.Get().AutoTracing.MemoryBurst.Interval
	silencePeriod := conf.Get().AutoTracing.MemoryBurst.IntervalContinuousRun
	topNProcesses := conf.Get().AutoTracing.MemoryBurst.DumpProcessMaxNum
	burstRatio := (float64(conf.Get().AutoTracing.MemoryBurst.DeltaMemoryBurst)/100.0 + 1)
	anonThreshold := conf.Get().AutoTracing.MemoryBurst.DeltaAnonThreshold

	memInfo, err := readMemInfo(map[string]bool{"MemTotal": true})
	if err != nil {
		log.Infof("Error reading MemTotal from memory info: %v\n", err)
		return err
	}
	memTotal := memInfo["MemTotal"]
	history := make([]int, historyWindowLength) // circular buffer
	var currentIndex int
	var isHistoryFull bool // don't check memory burst until we have enough data
	var topProcesses []ProcessMemoryInfo
	lastReportTime := time.Now().Add(-24 * time.Hour)

	_, err = checkAndRecordMemoryUsage(&currentIndex, &isHistoryFull, memTotal, history, historyWindowLength, topNProcesses, burstRatio, anonThreshold)
	if err != nil {
		log.Errorf("Fail to checkAndRecordMemoryUsage")
		return err
	}

	for {
		ticker := time.NewTicker(time.Duration(sampleInterval) * time.Second)
		stoppedByUser := false

		for range ticker.C {
			topProcesses, err = checkAndRecordMemoryUsage(&currentIndex, &isHistoryFull, memTotal, history, historyWindowLength, topNProcesses, burstRatio, anonThreshold)
			if err != nil {
				log.Errorf("Fail to checkAndRecordMemoryUsage")
				return err
			}

			select {
			case <-ctx.Done():
				log.Info("Caller request to stop")
				stoppedByUser = true
			default:
			}

			if len(topProcesses) > 0 || stoppedByUser {
				break
			}
		}

		ticker.Stop()

		if stoppedByUser {
			break
		}

		currentTime := time.Now()
		diff := currentTime.Sub(lastReportTime).Seconds()
		if diff < float64(silencePeriod) {
			continue
		}

		lastReportTime = currentTime

		storage.Save("memburst", "", time.Now(), &MemoryTracingData{TopMemoryUsage: topProcesses})
	}

	return nil
}
