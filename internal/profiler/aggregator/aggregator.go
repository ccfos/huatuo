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

package aggregator

import (
	"bytes"
	stdcontext "context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/core/autotracing"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	context "huatuo-bamai/internal/profiler/context"
	flamegraph "huatuo-bamai/internal/profiler/output/flamegraph"
	"huatuo-bamai/pkg/tracing"

	rqueue "github.com/Workiva/go-datastructures/queue"
)

type (
	RecordProcessor    func(rec any)
	AggregatedExporter func(pctx *context.ProfilerContext) (any, error)
)

// Aggregator implements most of the common logic, including scheduled aggregation (default 10s).
type Aggregator struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once

	aggrTracerID string
	aggrCountMap map[string]int64

	pctx               *context.ProfilerContext
	aggrQueue          *rqueue.RingBuffer
	recordProcessor    RecordProcessor
	aggregatedExporter AggregatedExporter
	aggrOverflowCount  int
}

// NewAggregator initializes the base aggregator. If interval <= 0, default is 10 seconds.
func NewAggregator(pctx *context.ProfilerContext, recProcessor RecordProcessor, aggrExporter AggregatedExporter) *Aggregator {
	if pctx.AggrInterval <= 0 {
		pctx.AggrInterval = 10
	}
	return &Aggregator{
		pctx:         pctx,
		aggrQueue:    rqueue.NewRingBuffer(65536),
		aggrCountMap: make(map[string]int64),
		aggrTracerID: tracing.AllocTaskID(),

		recordProcessor:    recProcessor,
		aggregatedExporter: aggrExporter,

		stopCh: make(chan struct{}),
	}
}

// Start scheduled aggregation task.
func (b *Aggregator) Start() {
	b.startAggrWorker()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if b.pctx.OneShotAgg {
			<-b.stopCh
			b.doAggregate(true)
			return
		}

		ticker := time.NewTicker(time.Duration(b.pctx.AggrInterval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				b.doAggregate(false) // Periodic aggregation only
			case <-b.stopCh:
				b.doAggregate(true) // Aggregate and persist on stop
				return
			}
		}
	}()
}

// Stop scheduled aggregation task.
func (b *Aggregator) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
		for b.aggrQueue.Len() > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		b.aggrQueue.Dispose()
		b.wg.Wait()
	})
}

// Add record to MPMC ringbuffer queue
func (b *Aggregator) AddRecord(data any) {
	ok, err := b.aggrQueue.Offer(data)
	if err != nil {
		// The RingBuffer has been disposed, usually occurring after Stop is called
		return
	}
	if !ok {
		b.aggrOverflowCount++
	}
}

func (b *Aggregator) startAggrWorker() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			rec, err := b.aggrQueue.Get()
			if err != nil {
				return
			}
			b.recordProcessor(rec)
		}
	}()
}

func (b *Aggregator) aggregate() (any, error) {
	return b.aggregatedExporter(b.pctx)
}

// Whether upload is enabled (usually based on OutputFormat).
func (b *Aggregator) enableUpload() bool {
	return b.pctx.OutputFormat == "pprof" || b.pctx.OutputFormat == "es"
}

// Send data (network upload).
func (b *Aggregator) uploadToES(data any) error {
	if err := b.save(data); err != nil {
		log.P().Errorf("OnSend Save error")
		return err
	}
	return nil
}

// Write to file (persist to disk).
func (b *Aggregator) writeToFile() error {
	var aggrDataArr [][]byte
	for stack, count := range b.aggrCountMap {
		line := fmt.Sprintf("%s %d", stack, count)
		aggrDataArr = append(aggrDataArr, []byte(line))
	}

	aggrData := bytes.Join(aggrDataArr, []byte("\n"))
	timestamp := time.Now().Unix()
	fileName := fmt.Sprintf("perf_%d.folded", timestamp)

	if err := os.MkdirAll(b.pctx.OutputPath, 0o755); err != nil {
		return err
	}

	filePath := filepath.Join(b.pctx.OutputPath, fileName)
	if err := os.WriteFile(filePath, aggrData, 0o600); err != nil {
		return err
	}

	fmt.Printf("Profiling data written to %s\n", filePath)
	return nil
}

// Private method: after aggregation, decide whether to send or write to file.
// final: whether called during stop.
func (b *Aggregator) doAggregate(final bool) {
	data, err := b.aggregate()
	if err != nil {
		log.P().Infof("aggregate error: %v", err)
		return
	}

	// data can be nil (no data), but still write accumulated result if final.
	if data == nil && !final {
		// No data and not final, just return.
		return
	}

	if b.enableUpload() {
		if data != nil {
			if err := b.uploadToES(data); err != nil {
				log.P().Infof("UploadToES error: %v", err)
			}
		} else {
			log.P().Infof("EnableUpload true but data is nil")
		}
		return
	}

	// For non-upload mode, accumulate newly aggregated data into aggrCountMap.
	if data != nil {
		b.aggregateAllData(data)
	} else {
		log.P().Infof("No new data aggregated this round")
	}

	// On final, write all accumulated data to file regardless of new data.
	if final {
		if len(b.aggrCountMap) == 0 {
			fmt.Fprintln(os.Stderr, "no profiling samples collected; nothing written")
			return
		}

		switch b.pctx.OutputFormat {
		case "raw":
			if err := b.writeToFile(); err != nil {
				log.P().Infof("WriteToFile error: %v", err)
			}
		case "flamegraph", "svg":
			if err := b.writeToSvg(); err != nil {
				log.P().Infof("WriteToSvg error: %v", err)
			}
		default:
			// default to raw format
			if err := b.writeToFile(); err != nil {
				log.P().Infof("WriteToFile error: %v", err)
			}
		}
	}
}

// Accumulate aggregated data into aggrCountMap with locking.
func (b *Aggregator) aggregateAllData(data any) {
	b.mu.Lock()
	defer b.mu.Unlock()

	aData, ok := data.([]byte)
	if !ok {
		log.P().Errorf("Aggregate data type assertion failed: %T", data)
		return
	}

	lines := strings.Split(string(aData), "\n")
	for _, line := range lines {
		idx := strings.LastIndex(line, " ")
		if idx == -1 {
			continue
		}
		stack := line[:idx]
		countStr := strings.TrimSpace(line[idx+1:])

		count, err := strconv.ParseInt(countStr, 10, 64)
		if err != nil {
			continue
		}
		b.aggrCountMap[stack] += count
	}
}

// save into es.
func (b *Aggregator) save(data any) error {
	var autoMeta any
	if len(b.pctx.CpuIdleMetaData) != 0 {
		cpuIdleMeta, err := context.MapToStructByJSON[autotracing.CPUIdleMetaData](b.pctx.CpuIdleMetaData)
		if err != nil {
			return fmt.Errorf("failed to map to struct of cpu idle meta")
		}
		autoMeta = cpuIdleMeta
	}

	if len(b.pctx.CpuSysMetaData) != 0 {
		cpuSysMeta, err := context.MapToStructByJSON[autotracing.CpuSysMetaData](b.pctx.CpuSysMetaData)
		if err != nil {
			return fmt.Errorf("failed to map to struct of cpu sys meta")
		}
		autoMeta = cpuSysMeta
	}

	flameData, ok := data.(*profiler.ProfileData)
	if !ok {
		return fmt.Errorf("invalid pprof data for uploading: %T", data)
	}

	tracerData := &context.TracerData{
		MetricData: newMetrics(b.aggrOverflowCount),
		FlameData:  flameData,
		MetaData:   autoMeta,
	}

	doc := profiler.CreateProfilingDocument(b.pctx.MetaData, b.pctx.ContainerID, b.pctx.ServerAddress)
	if doc == nil {
		return fmt.Errorf("failed to build profiler document")
	}

	doc.TracerData = tracerData
	if doc.TracerID == "" {
		doc.TracerID = b.aggrTracerID
	}

	if b.pctx.DataSaver != nil {
		if err := b.pctx.DataSaver.Save(stdcontext.Background(), doc); err != nil {
			log.Infof("failed to save %#v into profiling metadata store: %v", doc, err)
		}
	}

	fmt.Println(doc.TracerID)
	return nil
}

// writeToSvg writes aggregated data to SVG flame graph file
func (b *Aggregator) writeToSvg() error {
	if len(b.aggrCountMap) == 0 {
		return fmt.Errorf("no data in aggrCountMap to write")
	}

	// convert aggrCountMap to flamegraph or svg stack format
	stacks, err := b.convertAggrCountMapToStacks()
	if err != nil {
		return fmt.Errorf("failed to convert aggrCountMap to stacks: %w", err)
	}

	// create output directory
	if err := os.MkdirAll(b.pctx.OutputPath, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// generate filename
	timestamp := time.Now().Unix()
	fileName := fmt.Sprintf("flamegraph_%d.svg", timestamp)
	filePath := filepath.Join(b.pctx.OutputPath, fileName)

	// create file
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create svg file: %w", err)
	}
	defer file.Close()

	if err := flamegraph.Render(stacks, file); err != nil {
		return fmt.Errorf("failed to render flame graph: %w", err)
	}

	fmt.Printf("Flame graph written to %s\n", filePath)
	return nil
}

// convertAggrCountMapToStacks converts aggrCountMap to Stack slice
func (b *Aggregator) convertAggrCountMapToStacks() ([]flamegraph.Stack, error) {
	var stacks []flamegraph.Stack

	for stackStr, count := range b.aggrCountMap {
		// parse stack string, format like: "process 1234;func1;func2;func3"
		// or: "process 1234:process_name;func1;func2;func3"

		// remove quotes that might exist in process name references
		stackStr = strings.ReplaceAll(stackStr, "\"", "")

		// split stack elements
		parts := strings.Split(stackStr, ";")
		if len(parts) == 0 {
			continue
		}

		// clean each part, remove empty elements
		var names []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				// escape HTML special characters to avoid XML parsing errors
				// this handles symbols like <unknown> which contains '<'
				part = html.EscapeString(part)
				names = append(names, part)
			}
		}

		if len(names) > 0 {
			stacks = append(stacks, flamegraph.Stack{
				Names:   names,
				Samples: count,
			})
		}
	}

	// sort by sample count in descending order for better flame graph display
	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].Samples > stacks[j].Samples
	})

	return stacks, nil
}
