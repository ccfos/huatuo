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
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/tracing"
)

func init() {
	tracing.RegisterEventTracing("memburst", newMemBurst)
}

func newMemBurst() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memBurstTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

type (
	memBurstTracing   struct{}
	MemoryTracingData struct {
		TopMemoryUsage []*processMemInfo `json:"top_memory_usage"`
	}
)

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
) ([]*processMemInfo, error) {
	memInfo, err := readMemInfo(map[string]bool{
		"Active(anon)":   true,
		"Inactive(anon)": true,
	})
	if err != nil {
		return []*processMemInfo{}, fmt.Errorf("read memory info: %w", err)
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
			topProcesses, err := topMemoryProcesses(topNProcesses, memoryRSS)
			if err == nil {
				return topProcesses, nil
			}
			log.Errorf("Fail to getTopMemoryProcesses")
			return []*processMemInfo{}, err
		}
	}
	return []*processMemInfo{}, nil
}

func validateMemBurst(c *MemBurstConfig) error {
	if c.DeltaMemoryBurst <= 0 {
		return fmt.Errorf("memory burst delta must be positive, got %d", c.DeltaMemoryBurst)
	}
	if c.DeltaAnonThreshold < 0 || c.DeltaAnonThreshold > 100 {
		return fmt.Errorf("memory burst anon threshold must be in [0, 100], got %d", c.DeltaAnonThreshold)
	}
	if c.Interval <= 0 {
		return fmt.Errorf("memory burst interval must be positive, got %d", c.Interval)
	}
	if c.IntervalTracing <= 0 {
		return fmt.Errorf("memory burst tracing interval must be positive, got %d", c.IntervalTracing)
	}
	if c.SlidingWindowLength <= 0 {
		return fmt.Errorf("memory burst sliding window length must be positive, got %d", c.SlidingWindowLength)
	}
	if c.DumpProcessMaxNum <= 0 {
		return fmt.Errorf("memory burst dump process max num must be positive, got %d", c.DumpProcessMaxNum)
	}
	return nil
}

// Core function
func (c *memBurstTracing) Start(ctx context.Context) error {
	var err error

	historyWindowLength := cfg.MemoryBurst.SlidingWindowLength
	sampleInterval := cfg.MemoryBurst.Interval
	intervalTracing := cfg.MemoryBurst.IntervalTracing
	topNProcesses := cfg.MemoryBurst.DumpProcessMaxNum
	burstRatio := (float64(cfg.MemoryBurst.DeltaMemoryBurst)/100.0 + 1)
	anonThreshold := cfg.MemoryBurst.DeltaAnonThreshold

	if err := validateMemBurst(&cfg.MemoryBurst); err != nil {
		return err
	}

	memInfo, err := readMemInfo(map[string]bool{"MemTotal": true})
	if err != nil {
		log.Infof("Error reading MemTotal from memory info: %v\n", err)
		return err
	}
	memTotal := memInfo["MemTotal"]
	history := make([]int, historyWindowLength) // circular buffer
	var currentIndex int
	var isHistoryFull bool // don't check memory burst until we have enough data
	lastReportTime := time.Now().Add(-24 * time.Hour)
	_, err = checkAndRecordMemoryUsage(&currentIndex, &isHistoryFull, memTotal, history, historyWindowLength, topNProcesses, burstRatio, anonThreshold)
	if err != nil {
		log.Errorf("Fail to checkAndRecordMemoryUsage")
		return err
	}

	ticker := time.NewTicker(time.Duration(sampleInterval) * time.Second)
	defer ticker.Stop()

	for {
		var topProcesses []*processMemInfo
		for len(topProcesses) == 0 {
			select {
			case <-ctx.Done():
				log.Info("Caller request to stop")
				return nil
			case <-ticker.C:
				topProcesses, err = checkAndRecordMemoryUsage(&currentIndex, &isHistoryFull, memTotal, history, historyWindowLength, topNProcesses, burstRatio, anonThreshold)
				if err != nil {
					log.Errorf("Fail to checkAndRecordMemoryUsage")
					return err
				}
			}
		}

		currentTime := time.Now()
		diff := currentTime.Sub(lastReportTime).Seconds()
		if diff < float64(intervalTracing) {
			continue
		}
		lastReportTime = currentTime
		if err := tracing.Save(&tracing.WriteRequest{
			TracerName:    "memburst",
			ContainerID:   "",
			TracerTime:    time.Now(),
			TracerData:    &MemoryTracingData{TopMemoryUsage: topProcesses},
			TracerRunType: tracing.TracerRunTypeAutotracing,
		}); err != nil {
			log.Warnf("failed to save tracing data: %v", err)
		}
	}
}
