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
package autotracing

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/pids"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/shirou/gopsutil/process"
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
	memBurstTracing struct {
		containers map[string]*containerMemBurst
	}
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
	log.Debugf("Checked memory status. active_anon=%v KiB inactive_anon=%v KiB\n", memInfo["Active(anon)"], memInfo["Inactive(anon)"])
	if recordMemoryBurst(currentSum, memTotal, history, currentIndex, isHistoryFull, burstRatio, anonThreshold) {
		topProcesses, err := topMemoryProcesses(topNProcesses, memoryRSS)
		if err == nil {
			return topProcesses, nil
		}
		log.Errorf("Fail to getTopMemoryProcesses")
		return []*processMemInfo{}, err
	}
	return []*processMemInfo{}, nil
}

// Core function
func (c *memBurstTracing) Start(ctx context.Context) error {
	cfg := configSnapshot()
	if err := validateMemBurst(&cfg.MemoryBurst); err != nil {
		return err
	}

	var err error

	historyWindowLength := cfg.MemoryBurst.SlidingWindowLength
	sampleInterval := cfg.MemoryBurst.Interval
	intervalTracing := cfg.MemoryBurst.IntervalTracing
	topNProcesses := cfg.MemoryBurst.DumpProcessMaxNum
	burstRatio := (float64(cfg.MemoryBurst.DeltaMemoryBurst)/100.0 + 1)
	anonThreshold := cfg.MemoryBurst.DeltaAnonThreshold

	memInfo, err := readMemInfo(map[string]bool{"MemTotal": true})
	if err != nil {
		log.Infof("Error reading MemTotal from memory info: %v\n", err)
		return err
	}
	memTotal := memInfo["MemTotal"]
	c.containers = make(map[string]*containerMemBurst)
	c.sampleContainers(&cfg.MemoryBurst, memTotal, time.Now())

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
				c.sampleContainers(&cfg.MemoryBurst, memTotal, time.Now())

				topProcesses, err = checkAndRecordMemoryUsage(&currentIndex, &isHistoryFull, memTotal, history, historyWindowLength, topNProcesses, burstRatio, anonThreshold)
				if err != nil {
					log.Errorf("Fail to checkAndRecordMemoryUsage")
					return err
				}
			}
		}

		currentTime := time.Now().UTC()
		diff := currentTime.Sub(lastReportTime).Seconds()
		if diff < float64(intervalTracing) {
			continue
		}
		lastReportTime = currentTime
		if err := tracing.Save(&tracing.WriteRequest{
			TracerName:       "memburst",
			ContainerID:      "",
			StartedTimestamp: currentTime,
			TracerData:       &MemoryTracingData{TopMemoryUsage: topProcesses},
			TracerRunType:    types.TracerRunTypeAutotracing,
		}); err != nil {
			log.Warnf("failed to save tracing data: %v", err)
		}
	}
}

type containerMemBurst struct {
	path      string
	limit     int
	history   []int
	index     int
	full      bool
	lastTrace time.Time
}

func (s *containerMemBurst) resetHistory() {
	if s == nil {
		return
	}
	// Missing samples invalidate the window, not the trace cooldown.
	s.history = nil
	s.index = 0
	s.full = false
}

func (s *containerMemBurst) recordSample(dir string, current, limit int, cfg *MemBurstConfig, now time.Time) bool {
	if s.path != dir || s.limit != limit {
		s.resetHistory()
		s.path, s.limit = dir, limit
	}
	if len(s.history) == 0 {
		s.history = make([]int, cfg.SlidingWindowLength)
	}
	burst := recordMemoryBurst(current, limit, s.history, &s.index, &s.full,
		1+float64(cfg.DeltaMemoryBurst)/100, cfg.DeltaAnonThreshold)
	return burst && now.Sub(s.lastTrace) >= time.Duration(cfg.IntervalTracing)*time.Second
}

// Keep the existing host window semantics: newest vs oldest retained sample.
func recordMemoryBurst(current, total int, history []int, index *int, full *bool, ratio float64, threshold int) bool {
	history[*index] = current
	if *index == len(history)-1 {
		*full = true
	}
	*index = (*index + 1) % len(history)
	// Preserve the host's integer-KiB threshold without multiplication overflow.
	minimum := total/100*threshold + total%100*threshold/100
	return *full && float64(current) >= ratio*float64(history[*index]) &&
		current >= minimum
}

func (c *memBurstTracing) sampleContainers(cfg *MemBurstConfig, hostTotal int, now time.Time) {
	containers, err := pod.NormalContainers()
	if err != nil {
		for _, state := range c.containers {
			state.resetHistory()
		}
		log.WithError(err).Debug("discover memburst containers")
		return
	}
	manager, err := cgroups.NewManager()
	if err != nil {
		for _, state := range c.containers {
			state.resetHistory()
		}
		return
	}
	unified := cgroups.CgroupMode() == cgroups.Unified
	for id := range c.containers {
		if containers[id] == nil {
			delete(c.containers, id)
		}
	}
	for id, container := range containers {
		raw, err := manager.MemoryStatRaw(container.CgroupPath)
		if err != nil {
			c.containers[id].resetHistory()
			continue
		}
		root := paths.RootfsDefaultPath
		if !unified {
			root = filepath.Join(root, subsystem.SubsystemMemory)
		}
		dir := filepath.Join(root, container.CgroupPath)
		current, limit, err := containerBurstMemory(raw, root, dir, hostTotal, unified)
		if err != nil {
			c.containers[id].resetHistory()
			log.WithError(err).Debug("read container memburst memory")
			continue
		}
		state := c.containers[id]
		if state == nil {
			state = &containerMemBurst{}
			c.containers[id] = state
		}
		if !state.recordSample(dir, current, limit, cfg, now) {
			continue
		}
		procs, err := containerMemoryProcesses(dir, cfg.DumpProcessMaxNum)
		if err != nil {
			log.WithError(err).Debug("capture container memburst processes")
			continue
		}
		if len(procs) == 0 {
			continue
		}
		// Storage failures must not bypass cooldown and repeat process snapshots.
		state.lastTrace = now
		if err := tracing.Save(&tracing.WriteRequest{
			TracerName: "memburst", ContainerID: id, StartedTimestamp: now.UTC(),
			TracerData:    &MemoryTracingData{TopMemoryUsage: procs},
			TracerRunType: types.TracerRunTypeAutotracing,
		}); err != nil {
			log.WithError(err).Warn("save container memburst trace")
		}
	}
}

func containerBurstMemory(raw map[string]uint64, root, dir string, hostTotal int, unified bool) (int, int, error) {
	prefix := "total_"
	if unified {
		prefix = ""
	}
	active, activeOK := raw[prefix+"active_anon"]
	inactive, inactiveOK := raw[prefix+"inactive_anon"]
	if !activeOK || !inactiveOK || hostTotal <= 0 {
		return 0, 0, fmt.Errorf("container memburst requires anonymous LRU counters and positive host MemTotal")
	}
	limit := uint64(hostTotal) * 1024
	if unified {
		for current := dir; ; current = filepath.Dir(current) {
			data, err := os.ReadFile(filepath.Join(current, "memory.max"))
			if err != nil {
				if current == root && os.IsNotExist(err) {
					break
				}
				return 0, 0, err
			}
			if text := strings.TrimSpace(string(data)); text != "max" {
				value, err := strconv.ParseUint(text, 10, 64)
				if err != nil {
					return 0, 0, err
				}
				limit = min(limit, value)
			}
			if current == root {
				break
			}
			if current == filepath.Dir(current) {
				return 0, 0, fmt.Errorf("memburst path %q outside cgroup root %q", dir, root)
			}
		}
	} else {
		value, ok := raw["hierarchical_memory_limit"]
		if !ok {
			return 0, 0, fmt.Errorf("memory.stat missing hierarchical_memory_limit")
		}
		limit = min(limit, value)
	}
	if limit < 1024 || inactive > uint64(^uint(0)>>1) || active > uint64(^uint(0)>>1)-inactive {
		return 0, 0, fmt.Errorf("container memburst memory counters or limit out of range")
	}
	return int((active + inactive) / 1024), int(limit / 1024), nil
}

func containerMemoryProcesses(dir string, topN int) ([]*processMemInfo, error) {
	seen := make(map[int32]bool)
	var procs []*process.Process
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		ids, err := pids.Tasks(path, "cgroup.procs")
		if err != nil {
			return err
		}
		for _, pid := range ids {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			p, err := process.NewProcess(pid)
			if err == nil {
				procs = append(procs, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return topMemoryProcessList(procs, topN, memoryRSS), nil
}
