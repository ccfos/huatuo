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

package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/oom.c -o $BPF_DIR/oom.o

type OOMActor struct {
	MemoryCgroupCSSAddr string                   `json:"memory_cgroup_css_addr"`
	ContainerID         string                   `json:"container_id,omitempty"`
	ContainerHostname   string                   `json:"container_hostname,omitempty"`
	Pid                 int32                    `json:"pid"`
	Comm                string                   `json:"comm"`
	Cmdline             string                   `json:"cmdline,omitempty"`
	CmdlineTruncated    bool                     `json:"cmdline_truncated,omitempty"`
	Environ             []string                 `json:"environ,omitempty"`
	EnvironTruncated    bool                     `json:"environ_truncated,omitempty"`
	RssAnonBytes        uint64                   `json:"rss_anon_bytes,omitempty"`
	RssFileBytes        uint64                   `json:"rss_file_bytes,omitempty"`
	RssShmemBytes       uint64                   `json:"rss_shmem_bytes,omitempty"`
	TotalVmBytes        uint64                   `json:"total_vm_bytes,omitempty"`
	Cgroup              *OOMCgroupMemorySnapshot `json:"cgroup,omitempty"`
}

type OOMTracingData struct {
	Trigger        OOMActor           `json:"trigger"`
	Victim         OOMActor           `json:"victim"`
	MemorySnapshot *OOMMemorySnapshot `json:"memory_snapshot,omitempty"`
	LanguageInfo   *OOMLanguageInfo   `json:"language_info,omitempty"`
}

type oomMetric struct {
	count            int
	latestVictimComm string
}

type oomCollector struct {
	cgroup cgroups.Cgroup
}

const oomExitWorkerLimit = 8

var (
	outOfMemoryCounterHost      float64
	outOfMemoryCounterContainer = make(map[string]*oomMetric)
	mutex                       sync.Mutex
	pageSize                    = uint64(os.Getpagesize())
)

func init() {
	tracing.RegisterEventTracing("oom", newOOMCollector)
}

func newOOMCollector() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		log.Warnf("failed to initialize cgroup reader for oom snapshot: %v", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &oomCollector{
			cgroup: cgroup,
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *oomCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}

	var metrics []*metric.Data

	mutex.Lock()

	metrics = append(metrics, metric.NewCounterData("host_total", outOfMemoryCounterHost, "host oom counter", nil))
	for _, container := range containers {
		if val, exists := outOfMemoryCounterContainer[container.ID]; exists {
			metrics = append(
				metrics,
				metric.NewContainerCounterData(container, "total", float64(val.count), "containers oom counter", map[string]string{"latest_victim_comm": val.latestVictimComm}),
			)
		}
	}

	mutex.Unlock()
	return metrics, nil
}

/*
 * openOOMBPF opens the optional exit stream before attaching the OOM object.
 * Failure to create that stream only disables exit-context enrichment.
 */
func openOOMBPF(ctx context.Context) (
	bpf.BPF, bpf.PerfEventReader, bpf.PerfEventReader, error,
) {
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return nil, nil, nil, err
	}

	exitReader, err := b.EventPipeByName(ctx, "oom_exit_perf_events", 8192)
	if err != nil {
		log.Warnf("oom exit context disabled: %v", err)
		exitReader = nil
	}

	reader, err := b.AttachAndEventPipe(ctx, "oom_perf_events", 8192)
	if err != nil {
		if exitReader != nil {
			exitReader.Close()
		}
		b.Close()
		return nil, nil, nil, err
	}

	return b, reader, exitReader, nil
}

/*
 * Start reads OOM events continuously while separate workers correlate exit
 * context. A failed exit reader disables context enrichment without
 * interrupting the primary OOM event stream.
 */
func (c *oomCollector) Start(ctx context.Context) error {
	childCtx, cancel := context.WithCancel(ctx)
	b, reader, exitReader, err := openOOMBPF(childCtx)
	if err != nil {
		cancel()
		return err
	}
	defer b.Close()

	var workers sync.WaitGroup
	/*
	 * Preparation and saving touch shared OOM accounting and tracing state.
	 * Exit-context waits deliberately stay outside this lock.
	 */
	var processMu sync.Mutex
	asyncWorkerSlots := make(chan struct{}, oomExitWorkerLimit)
	/*
	 * childCtx owns the collector lifetime. exitWaitCtx can be canceled alone
	 * to disable exit enrichment while the base OOM reader keeps running.
	 */
	exitWaitCtx, cancelExitWait := context.WithCancel(childCtx)
	if exitReader == nil {
		cancelExitWait()
	}

	/*
	 * processEvent preserves the original serialized enrichment and storage.
	 * Only workers with a slot wait for exit context; overflow events retain
	 * their base data instead of creating unbounded goroutines.
	 */
	processEvent := func(data *abi.OOMEvent, eventTime time.Time,
		waitForExit bool,
	) {
		if childCtx.Err() != nil {
			return
		}

		/*
		 * Read live procfs before container synchronization and snapshot
		 * collection give the victim time to disappear.
		 */
		languageInfo := detectLanguageInfo(data.VictimPID)

		processMu.Lock()
		oomData := c.prepareOOMEvent(data, languageInfo)
		processMu.Unlock()

		if waitForExit {
			exitContext := oomExitEvents.wait(
				exitWaitCtx, data.VictimPID, data.Timestamp)
			if childCtx.Err() != nil {
				return
			}
			mergeOOMExitContext(oomData, exitContext)
		}
		completeLanguageInfo(oomData)

		processMu.Lock()
		defer processMu.Unlock()
		if childCtx.Err() != nil {
			return
		}
		saveOOMEvent(oomData, eventTime)
	}

	defer func() {
		cancelExitWait()
		cancel()
		reader.Close()
		if exitReader != nil {
			exitReader.Close()
		}
		workers.Wait()
	}()

	/*
	 * Drain the exit stream independently; otherwise its large records could
	 * fill the perf buffer while a base event is being enriched or stored.
	 */
	if exitReader != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := startOOMExitReader(childCtx, exitReader); err != nil {
				cancelExitWait()
				log.Warnf(
					"oom exit context disabled after reader failure: %v",
					err)
			}
		}()
	}

	b.DetachOnContextDone(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			data := new(abi.OOMEvent)
			if err := reader.ReadInto(data); err != nil {
				if childCtx.Err() != nil {
					return nil
				}
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("failed to read perf event: %w", err)
			}
			eventTime := time.Now()

			/*
			 * Bound asynchronous exit waits to the kernel pending-map
			 * capacity. Registration failures and overflow retain base
			 * collection synchronously.
			 */
			if exitWaitCtx.Err() != nil || data.Timestamp == 0 {
				processEvent(data, eventTime, false)
				continue
			}

			/*
			 * Slot acquisition is non-blocking. During an OOM storm, preserving
			 * the base event takes priority over waiting for optional context.
			 */
			select {
			case asyncWorkerSlots <- struct{}{}:
				workers.Add(1)
				go func(data *abi.OOMEvent, eventTime time.Time) {
					defer workers.Done()
					defer func() { <-asyncWorkerSlots }()
					processEvent(data, eventTime, true)
				}(data, eventTime)
			default:
				processEvent(data, eventTime, false)
			}
		}
	}
}

/*
 * prepareOOMEvent captures container association, resource snapshots, and
 * accounting before waiting for the victim exit event. Container discovery
 * failures degrade enrichment without discarding the base OOM event.
 */
func (c *oomCollector) prepareOOMEvent(
	data *abi.OOMEvent, languageInfo *OOMLanguageInfo,
) *OOMTracingData {
	containers, err := pod.Containers()
	containerLookupSucceeded := err == nil
	if err != nil {
		log.Warnf("failed to fetch containers for oom enrichment: %v", err)
		containers = nil
	}

	oomData := buildTracingData(data, containers, c.cgroup)
	oomData.LanguageInfo = languageInfo

	mutex.Lock()
	if container, ok := containers[oomData.Victim.ContainerID]; ok {
		containerCounterUpdate(container.ID, oomData.Victim.Comm)
	} else if containerLookupSucceeded {
		outOfMemoryCounterHost++
	}
	mutex.Unlock()

	return oomData
}

/*
 * saveOOMEvent writes the enriched base event and any correlated exit context
 * as one tracing record.
 */
func saveOOMEvent(oomData *OOMTracingData, eventTime time.Time) {
	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:  "oom",
		TracerTime:  eventTime,
		TracerData:  oomData,
		ContainerID: oomData.Victim.ContainerID,
	}); err != nil {
		log.Warnf("failed to save tracing data: %v", err)
	}
}

func buildTracingData(data *abi.OOMEvent, containers map[string]*pod.Container, cgroup cgroups.Cgroup) *OOMTracingData {
	cssContainers := pod.BuildCssContainersID(containers, subsystem.SubsystemMemory)

	triggerID := cssContainers[data.TriggerMemcgCSS]
	victimID := cssContainers[data.VictimMemcgCSS]

	oomData := &OOMTracingData{
		Trigger: OOMActor{
			MemoryCgroupCSSAddr: kernaddr.Format(data.TriggerMemcgCSS),
			ContainerID:         triggerID,
			Pid:                 int32(data.TriggerPID),
			Comm:                bytesutil.ToStr(data.TriggerComm[:]),
		},
		Victim: OOMActor{
			MemoryCgroupCSSAddr: kernaddr.Format(data.VictimMemcgCSS),
			ContainerID:         victimID,
			Pid:                 int32(data.VictimPID),
			Comm:                bytesutil.ToStr(data.VictimComm[:]),
			RssAnonBytes:        data.VictimRssAnonPages * pageSize,
			RssFileBytes:        data.VictimRssFilePages * pageSize,
			RssShmemBytes:       data.VictimRssShmemPages * pageSize,
			TotalVmBytes:        data.VictimTotalVmPages * pageSize,
		},
	}

	if container, ok := containers[triggerID]; ok {
		oomData.Trigger.ContainerHostname = container.Hostname
		if snap, err := cgroupMemorySnapshot(cgroup, container); err != nil {
			log.Warnf("trigger cgroup snapshot: %v", err)
		} else {
			oomData.Trigger.Cgroup = snap
		}
	}

	if container, ok := containers[victimID]; ok {
		oomData.Victim.ContainerHostname = container.Hostname
		if snap, err := cgroupMemorySnapshot(cgroup, container); err != nil {
			log.Warnf("victim cgroup snapshot: %v", err)
		} else {
			oomData.Victim.Cgroup = snap
		}
	}

	if snap, err := hostMemorySnapshot(); err != nil {
		log.Warnf("host memory snapshot: %v", err)
	} else {
		oomData.MemorySnapshot = snap
	}
	return oomData
}

func containerCounterUpdate(containerID, comm string) {
	if val, exists := outOfMemoryCounterContainer[containerID]; exists {
		val.count++
		val.latestVictimComm = comm
		return
	}

	outOfMemoryCounterContainer[containerID] = &oomMetric{count: 1, latestVictimComm: comm}
}
