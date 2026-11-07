// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// IOCOST tests cover wait accounting, interval recovery, and session cleanup.
package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	ioCostCaptureTestCSS       = uint64(0x300)
	ioCostCaptureTestCSSSerial = uint64(0x400)
)

type ioCostCaptureTestLane = ioControlWaitCounters

type ioCostCaptureTestDump struct {
	items   []bpf.MapItem
	err     error
	release <-chan struct{}
	entered chan<- string
}

type ioCostCaptureTestRead struct {
	value   []byte
	err     error
	release <-chan struct{}
	entered chan<- string
}

type fakeIOCostCaptureBPF struct {
	bpf.BPF

	mu              sync.Mutex
	mapIDs          map[string]uint32
	mapNames        map[uint32]string
	defaultItems    map[string][]bpf.MapItem
	dumpResponses   map[string][]ioCostCaptureTestDump
	statusValue     []byte
	statusResponses []ioCostCaptureTestRead
	deviceChanges   uint64
	deleteErrors    map[string][]error
	deletedKeys     map[string][][]byte
}

func newFakeIOCostCaptureBPF(possibleCPUs int) *fakeIOCostCaptureBPF {
	object := &fakeIOCostCaptureBPF{
		mapIDs: map[string]uint32{
			ioCostWaitAggregateMap: 1,
			ioCostRetiredMap:       2,
			ioControlStatusMap:     4,
		},
		mapNames: map[uint32]string{
			1: ioCostWaitAggregateMap,
			2: ioCostRetiredMap,
			4: ioControlStatusMap,
		},
		defaultItems:  make(map[string][]bpf.MapItem),
		dumpResponses: make(map[string][]ioCostCaptureTestDump),
		deleteErrors:  make(map[string][]error),
		deletedKeys:   make(map[string][][]byte),
		statusValue:   ioControlTestBytes(ioControlStatus{}),
	}
	lanes := make([]ioCostCaptureTestLane, possibleCPUs)
	for cpu := range lanes {
		lanes[cpu] = ioCostCaptureStableLane(
			uint64(cpu+1), uint64(cpu+1)*1_000,
		)
	}
	object.defaultItems[ioCostWaitAggregateMap] = []bpf.MapItem{
		ioCostCaptureTestAggregateItem(
			ioCostCaptureTestCSS,
			ioCostCaptureTestCSSSerial,
			0,
			0,
			lanes...,
		),
	}
	return object
}

func (object *fakeIOCostCaptureBPF) MapIDByName(name string) uint32 {
	object.mu.Lock()
	defer object.mu.Unlock()
	return object.mapIDs[name]
}

func (object *fakeIOCostCaptureBPF) DumpMap(
	mapID uint32,
) ([]bpf.MapItem, error) {
	object.mu.Lock()
	name := object.mapNames[mapID]
	response := ioCostCaptureTestDump{
		items: cloneIOCostCaptureTestItems(object.defaultItems[name]),
	}
	if queued := object.dumpResponses[name]; len(queued) != 0 {
		response = queued[0]
		object.dumpResponses[name] = queued[1:]
	}
	object.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("unexpected IOCOST map ID %d", mapID)
	}
	if response.release != nil {
		if response.entered != nil {
			response.entered <- name
		}
		<-response.release
	}
	return cloneIOCostCaptureTestItems(response.items), response.err
}

func (object *fakeIOCostCaptureBPF) dumpBatch(mapID uint32) ([]bpf.MapItem, error) {
	return object.DumpMap(mapID)
}

func (object *fakeIOCostCaptureBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	return object.DumpMap(object.MapIDByName(name))
}

func (object *fakeIOCostCaptureBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	object.mu.Lock()
	defer object.mu.Unlock()
	name := object.mapNames[mapID]
	if name == "" {
		return fmt.Errorf("unexpected IOCOST deletion map ID %d", mapID)
	}
	if queued := object.deleteErrors[name]; len(queued) != 0 {
		object.deleteErrors[name] = queued[1:]
		if queued[0] != nil {
			return queued[0]
		}
	}
	for _, key := range keys {
		if name == ioCostRetiredMap {
			for _, item := range object.defaultItems[ioCostWaitAggregateMap] {
				if bytes.HasPrefix(item.Key, key) {
					return errors.New("retired marker deleted before its cumulative rows")
				}
			}
		}
		object.deletedKeys[name] = append(object.deletedKeys[name], append([]byte(nil), key...))
		items := object.defaultItems[name]
		for index, item := range items {
			if bytes.Equal(item.Key, key) {
				object.defaultItems[name] = slices.Delete(items, index, index+1)
				break
			}
		}
	}
	return nil
}

func (object *fakeIOCostCaptureBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	object.mu.Lock()
	name := object.mapNames[mapID]
	response := ioCostCaptureTestRead{value: append([]byte(nil), object.statusValue...)}
	if len(response.value) == ioCostStatusSize {
		binary.LittleEndian.PutUint64(response.value[8:], object.deviceChanges)
	}
	if len(object.statusResponses) != 0 {
		response = object.statusResponses[0]
		object.statusResponses = object.statusResponses[1:]
	}
	object.mu.Unlock()
	if name != ioControlStatusMap || len(key) != ioCostUint32Size ||
		binary.LittleEndian.Uint32(key) != 0 {
		return nil, fmt.Errorf("unexpected IOCOST status read %s key %x", name, key)
	}
	if response.release != nil {
		if response.entered != nil {
			response.entered <- name
		}
		<-response.release
	}
	return append([]byte(nil), response.value...), response.err
}

func (object *fakeIOCostCaptureBPF) queueDumps(
	name string,
	responses ...ioCostCaptureTestDump,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.dumpResponses[name] = append(
		object.dumpResponses[name], responses...,
	)
}

func (object *fakeIOCostCaptureBPF) setDefaultItems(
	name string,
	items []bpf.MapItem,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.defaultItems[name] = cloneIOCostCaptureTestItems(items)
}

func (object *fakeIOCostCaptureBPF) queueStatus(
	responses ...ioCostCaptureTestRead,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.statusResponses = append(object.statusResponses, responses...)
}

func cloneIOCostCaptureTestItems(items []bpf.MapItem) []bpf.MapItem {
	cloned := make([]bpf.MapItem, len(items))
	for index, item := range items {
		cloned[index] = bpf.MapItem{
			Key:   append([]byte(nil), item.Key...),
			Value: append([]byte(nil), item.Value...),
		}
	}
	return cloned
}

func ioCostCaptureStableLane(count, wait10US uint64) ioCostCaptureTestLane {
	return ioCostCaptureTestLane{
		IOCount:  count,
		Wait10US: wait10US,
	}
}

func ioCostCaptureTestAggregateItem(css, cssSerial uint64, operation, reserved uint32,
	lanes ...ioCostCaptureTestLane,
) bpf.MapItem {
	return ioControlWaitTestItem(ioControlWaitKey{
		CSS: css, CSSSerial: cssSerial, Major: 8, Minor: 16,
		Operation: operation, Reserved: reserved,
	}, lanes...)
}

type ioCostCaptureTestSession struct {
	session *ioCostSession
	cancel  context.CancelFunc

	mu          sync.Mutex
	cancelCalls int
}

func newIOCostCaptureTestSession(
	t *testing.T,
	object *fakeIOCostCaptureBPF,
	possibleCPUs int,
) *ioCostCaptureTestSession {
	t.Helper()
	ioControlTestDiskstats(t)
	breaker, cancel := context.WithCancelCause(t.Context())
	harness := &ioCostCaptureTestSession{cancel: func() { cancel(nil) }}
	harness.session = &ioCostSession{
		object:          object,
		hashReader:      ioControlHashReader{lookupBatch: object.dumpBatch},
		possibleCPUs:    possibleCPUs,
		breaker:         breaker,
		previous:        make(ioControlWaitSnapshot),
		containerSource: emptyIOCostContainerSource,
		cancel: func(cause error) {
			harness.mu.Lock()
			harness.cancelCalls++
			harness.mu.Unlock()
			cancel(cause)
		},
	}
	t.Cleanup(harness.cancel)
	return harness
}

func (session *ioCostCaptureTestSession) cancelCount() int {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.cancelCalls
}

const (
	ioCostUint32Size = 4
	ioCostStatusSize = 16
)

type ioCostIntervalTestRow struct {
	css       uint64
	cssSerial uint64
	major     uint32
	minor     uint32
	operation uint32
	lanes     []ioCostCaptureTestLane
}

func ioCostDefaultIntervalTestRow(
	count uint64,
	wait10US uint64,
) ioCostIntervalTestRow {
	return ioCostIntervalTestRow{
		css:       ioCostCaptureTestCSS,
		cssSerial: ioCostCaptureTestCSSSerial,
		major:     8,
		minor:     16,
		operation: 0,
		lanes: []ioCostCaptureTestLane{
			ioCostCaptureStableLane(count, wait10US),
		},
	}
}

func setIOCostIntervalTestRows(object *fakeIOCostCaptureBPF, rows ...ioCostIntervalTestRow) {
	aggregates := make([]bpf.MapItem, 0, len(rows))
	for _, row := range rows {
		item := ioCostCaptureTestAggregateItem(row.css, row.cssSerial, row.operation, 0, row.lanes...)
		binary.LittleEndian.PutUint32(item.Key[16:20], row.major)
		binary.LittleEndian.PutUint32(item.Key[20:24], row.minor)
		aggregates = append(aggregates, item)
	}
	object.setDefaultItems(ioCostWaitAggregateMap, aggregates)
}

type ioCostIntervalTestFixture struct {
	collector *iocostTracing
	harness   *ioCostCaptureTestSession
	object    *fakeIOCostCaptureBPF
	source    *ioControlAttributionTestSource
}

func newIOCostIntervalTestFixture(
	t *testing.T,
	possibleCPUs int,
	containers map[string]*pod.Container,
) *ioCostIntervalTestFixture {
	t.Helper()
	object := newFakeIOCostCaptureBPF(possibleCPUs)
	harness := newIOCostCaptureTestSession(t, object, possibleCPUs)
	source := &ioControlAttributionTestSource{containers: containers}
	harness.session.containerSource = source.read
	harness.session.previous = make(ioControlWaitSnapshot)
	return &ioCostIntervalTestFixture{
		collector: &iocostTracing{session: harness.session},
		harness:   harness,
		object:    object,
		source:    source,
	}
}

func ioCostMetricValues(
	t *testing.T,
	metrics []*metric.Data,
) map[string]float64 {
	t.Helper()
	values := make(map[string]float64, len(metrics))
	for _, data := range metrics {
		kind := data.Labels()["scope"]
		if strings.HasPrefix(data.Name(), "container_") {
			kind = "container"
		}
		values[kind+"/"+data.Name()] = data.Value
	}
	return values
}

func requireIOCostHostAndOtherInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Len(t, metrics, 4)
	require.InDeltaMapValues(t, map[string]float64{
		"host/waitq_io_count":   count,
		"host/average_wait_ms":  averageMS,
		"other/waitq_io_count":  count,
		"other/average_wait_ms": averageMS,
	}, ioCostMetricValues(t, metrics), 1e-12)
}

func requireIOCostHostAndContainerInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Len(t, metrics, 4)
	require.Equal(t, map[string]float64{
		"host/waitq_io_count":                 count,
		"host/average_wait_ms":                averageMS,
		"container/container_waitq_io_count":  count,
		"container/container_average_wait_ms": averageMS,
	}, ioCostMetricValues(t, metrics))
}

func TestLoadIOCostKernelProfile(t *testing.T) {
	for _, test := range []struct {
		name          string
		missing       string
		isUnsupported bool
	}{
		{
			name:          "required hook missing",
			missing:       ioCostWakeSymbol,
			isUnsupported: true,
		},
		{
			name:    "optional caller absent",
			missing: ioCostOverBudgetCaller,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := syntheticIOCostKallsymsProfile()
			delete(profile, test.missing)
			got, err := loadIOCostKernelProfileWith(func(
				[]string,
				symbol.KsymbolLookupMode,
			) (map[string]symbol.KsymbolRange, error) {
				return profile, nil
			})
			if test.isUnsupported {
				require.ErrorIs(t, err, types.ErrNotSupported)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, profile[ioCostThrottleCaller], got.throttleRange)
			require.Zero(t, got.overBudgetRange)
		})
	}
}

func syntheticIOCostKallsymsProfile() map[string]symbol.KsymbolRange {
	return map[string]symbol.KsymbolRange{
		ioCostKickSymbol:       {Start: 0x1000},
		ioCostWakeSymbol:       {Start: 0x2000},
		ioCostPDFreeSymbol:     {Start: 0x4000},
		ioCostThrottleCaller:   {Start: 0x5000, End: 0x5100},
		ioCostOverBudgetCaller: {Start: 0x6000, End: 0x6080},
	}
}

func TestIOCostTransactionFailuresRebaselineBeforeDelta(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(3, 900))
	data, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, data, 3, 3)

	fixture.object.queueDumps(ioCostWaitAggregateMap,
		ioCostCaptureTestDump{err: unix.EINTR})
	data, err = fixture.collector.Update()
	require.ErrorIs(t, err, unix.EINTR)
	require.Nil(t, data)
	require.Nil(t, fixture.collector.session.previous)
	require.Zero(t, fixture.harness.cancelCount())

	setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(5, 1700))
	data, err = fixture.collector.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Nil(t, data)
	require.NotNil(t, fixture.collector.session.previous)

	setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(7, 2300))
	data, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(t, data, 2, 3)
}

func TestIOCostRetiredIntervals(t *testing.T) {
	identity := ioControlRetiredKey{
		CSS:       ioCostCaptureTestCSS,
		CSSSerial: ioCostCaptureTestCSSSerial,
		Major:     8,
		Minor:     16,
	}
	marker := bpf.MapItem{Key: ioControlTestBytes(identity), Value: []byte{1}}
	for _, test := range []struct {
		name          string
		isSampled     bool
		deleteMap     string
		deleteResults []error
	}{
		{name: "first collection after release"},
		{name: "final increment only", isSampled: true},
		{
			name:          "counter deletion retry",
			deleteMap:     ioCostWaitAggregateMap,
			deleteResults: []error{unix.EIO},
		},
		{
			name:          "marker deletion retry",
			deleteMap:     ioCostRetiredMap,
			deleteResults: []error{unix.EIO},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := newIOControlAttributionTestContainer(
				"exited-container", identity.CSS, time.Unix(100, 0),
			)
			fixture := newIOCostIntervalTestFixture(t, 2, map[string]*pod.Container{container.ID: container})
			if test.isSampled {
				baseline := ioCostDefaultIntervalTestRow(3, 900)
				baseline.lanes = append(baseline.lanes, ioCostCaptureStableLane(0, 0))
				setIOCostIntervalTestRows(fixture.object, baseline)
				metrics, err := fixture.collector.Update()
				require.NoError(t, err)
				requireIOCostHostAndContainerInterval(t, metrics, 3, 3)
			}
			row := ioCostDefaultIntervalTestRow(5, 1_500)
			row.lanes = append(row.lanes, ioCostCaptureStableLane(2, 600))
			setIOCostIntervalTestRows(fixture.object, row)
			fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
			fixture.object.deleteErrors[test.deleteMap] = test.deleteResults
			metrics, err := fixture.collector.Update()
			require.NoError(t, err)
			count := float64(7)
			if test.isSampled {
				count -= 3
			}
			requireIOCostHostAndOtherInterval(t, metrics, count, 3)
			require.NotNil(t, fixture.harness.session.previous,
				"reclamation errors must not invalidate a published baseline")
			if test.deleteMap != "" {
				require.NotEmpty(t, fixture.object.defaultItems[ioCostRetiredMap])
			}
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			for _, data := range metrics {
				require.Zero(t, data.Value, "a retry cannot republish the final increment")
			}
			require.Empty(t, fixture.object.defaultItems[ioCostWaitAggregateMap])
			require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
			require.Empty(t, fixture.harness.session.previous)
			require.Zero(t, fixture.harness.cancelCount())
		})
	}

	t.Run("retired tail joins surviving owners in the same interval", func(t *testing.T) {
		containers := make(map[string]*pod.Container)
		rows := make([]ioCostIntervalTestRow, 3)
		for index := range rows {
			rows[index] = ioCostDefaultIntervalTestRow(1, 100)
			rows[index].css += uint64(index)
			rows[index].cssSerial += uint64(index)
			id := fmt.Sprintf("owner-%d", index)
			container := newIOControlAttributionTestContainer(id, rows[index].css, time.Unix(100, 0))
			container.Name = id
			containers[id] = container
		}
		fixture := newIOCostIntervalTestFixture(t, 1, containers)
		setIOCostIntervalTestRows(fixture.object, rows...)
		_, err := fixture.collector.Update()
		require.NoError(t, err)
		for index := range rows {
			rows[index].lanes[0] = ioCostCaptureStableLane(2, uint64(index+2)*100)
		}
		setIOCostIntervalTestRows(fixture.object, rows...)
		retired := ioControlRetiredKey{
			CSS:       rows[2].css,
			CSSSerial: rows[2].cssSerial,
			Major:     rows[2].major,
			Minor:     rows[2].minor,
		}
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{
			{
				Key:   ioControlTestBytes(retired),
				Value: []byte{1},
			},
		})
		entered := make(chan string, 1)
		release := make(chan struct{})
		fixture.object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{
			items:   cloneIOCostCaptureTestItems(fixture.object.defaultItems[ioCostWaitAggregateMap]),
			entered: entered,
			release: release,
		})
		var metrics []*metric.Data
		done := make(chan error, 1)
		releaseRead := sync.OnceFunc(func() { close(release) })
		var workers sync.WaitGroup
		defer func() {
			releaseRead()
			workers.Wait()
		}()
		workers.Add(1)
		go func() {
			defer workers.Done()
			var err error
			metrics, err = fixture.collector.Update()
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("IOCOST did not reach the cumulative snapshot")
		}
		// Metadata disappears while the captured counters still contain its tail.
		fixture.source.containers = map[string]*pod.Container{
			"owner-0": containers["owner-0"],
			"owner-1": containers["owner-1"],
		}
		releaseRead()
		require.NoError(t, <-done)
		values := make(map[string]float64)
		for _, data := range metrics {
			scope := data.Labels()["scope"]
			if strings.HasPrefix(data.Name(), "container_") {
				scope = data.Labels()["container_name"]
			}
			values[scope+"/"+data.Name()] = data.Value
		}
		require.Equal(t, map[string]float64{
			"host/waitq_io_count":               3,
			"host/average_wait_ms":              2,
			"other/waitq_io_count":              1,
			"other/average_wait_ms":             3,
			"owner-0/container_waitq_io_count":  1,
			"owner-0/container_average_wait_ms": 1,
			"owner-1/container_waitq_io_count":  1,
			"owner-1/container_average_wait_ms": 2,
		}, values)
		metrics, err = fixture.collector.Update()
		require.NoError(t, err)
		for _, data := range metrics {
			require.Zero(t, data.Value, "the retired tail must not repeat")
		}
	})

	t.Run("retirement during capture waits for the next collection", func(t *testing.T) {
		fixture := newIOCostIntervalTestFixture(t, 1, nil)
		row := ioCostDefaultIntervalTestRow(3, 900)
		setIOCostIntervalTestRows(fixture.object, row)
		entered := make(chan string, 1)
		release := make(chan struct{})
		fixture.object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{
			items:   cloneIOCostCaptureTestItems(fixture.object.defaultItems[ioCostWaitAggregateMap]),
			entered: entered,
			release: release,
		})
		var metrics []*metric.Data
		done := make(chan error, 1)
		releaseRead := sync.OnceFunc(func() { close(release) })
		var workers sync.WaitGroup
		defer func() {
			releaseRead()
			workers.Wait()
		}()
		workers.Add(1)
		go func() {
			defer workers.Done()
			var err error
			metrics, err = fixture.collector.Update()
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("IOCOST did not reach the cumulative snapshot")
		}
		// Publish retirement after the older counter snapshot was captured.
		// Reading R after that snapshot would discard this final increment.
		row.lanes[0] = ioCostCaptureStableLane(5, 1_500)
		setIOCostIntervalTestRows(fixture.object, row)
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
		releaseRead()
		require.NoError(t, <-done)
		requireIOCostHostAndOtherInterval(t, metrics, 3, 3)
		require.Empty(t, fixture.object.deletedKeys)
		metrics, err := fixture.collector.Update()
		require.NoError(t, err)
		requireIOCostHostAndOtherInterval(t, metrics, 2, 3)
		require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
	})

	t.Run("partial reclamation preserves other generations and devices", func(t *testing.T) {
		container := newIOControlAttributionTestContainer(
			"current-container", identity.CSS, time.Unix(100, 0),
		)
		fixture := newIOCostIntervalTestFixture(t, 1, map[string]*pod.Container{container.ID: container})
		read := ioCostDefaultIntervalTestRow(2, 200)
		write := ioCostDefaultIntervalTestRow(3, 600)
		write.operation = 1
		generation := ioCostDefaultIntervalTestRow(5, 1_500)
		generation.cssSerial++
		disk := ioCostDefaultIntervalTestRow(7, 2_800)
		disk.minor++
		setIOCostIntervalTestRows(
			fixture.object,
			read,
			write,
			generation,
			disk,
		)
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
		fixture.object.deleteErrors[ioCostWaitAggregateMap] = []error{nil, unix.EIO}
		metrics, err := fixture.collector.Update()
		require.NoError(t, err)
		counts := make(map[string]float64)
		for _, data := range metrics {
			if data.Name() != ioCostWaitCountName && data.Name() != "container_"+ioCostWaitCountName {
				continue
			}
			scope := data.Labels()["scope"]
			if strings.HasPrefix(data.Name(), "container_") {
				scope = "container"
			}
			counts[scope+"/"+data.Labels()["operation"]] = data.Value
		}
		require.Equal(t, map[string]float64{
			"host/read": 7, "host/write": 3,
			"other/read": 2, "other/write": 3,
			"container/read": 5,
		}, counts)
		require.Len(t, fixture.object.defaultItems[ioCostWaitAggregateMap], 3)
		require.Len(t, fixture.object.defaultItems[ioCostRetiredMap], 1)
		require.Len(t, fixture.harness.session.previous, 3)
		metrics, err = fixture.collector.Update()
		require.NoError(t, err)
		for _, data := range metrics {
			require.Zero(t, data.Value)
		}
		require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
		require.Len(t, fixture.object.defaultItems[ioCostWaitAggregateMap], 2)
		for key := range fixture.harness.session.previous {
			require.True(t, key.CSSSerial == generation.cssSerial || key.Minor == disk.minor)
		}
	})

	t.Run("failed collection retains final counters until rebaseline", func(t *testing.T) {
		fixture := newIOCostIntervalTestFixture(t, 1, nil)
		fixture.object.setDefaultItems(ioCostRetiredMap, []bpf.MapItem{marker})
		setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(3, 900))
		fixture.object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{err: unix.EINTR})
		_, err := fixture.collector.Update()
		require.ErrorIs(t, err, unix.EINTR)
		require.Empty(t, fixture.object.deletedKeys)
		require.Nil(t, fixture.harness.session.previous)
		metrics, err := fixture.collector.Update()
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Nil(t, metrics)
		require.NotNil(t, fixture.harness.session.previous)
		metrics, err = fixture.collector.Update()
		require.NoError(t, err)
		require.Empty(t, metrics, "failed windows must not be backfilled")
		require.Empty(t, fixture.object.defaultItems[ioCostRetiredMap])
	})
}

func TestIOCostTransactionTerminalErrorsAndTopologyPriority(t *testing.T) {
	for _, failure := range []string{
		"ABI", "health", "device", "teardown after ABI",
		"device during capture", "device during capture with unchanged count",
	} {
		t.Run(failure, func(t *testing.T) {
			fixture := newIOCostIntervalTestFixture(t, 1, nil)
			duringCapture := strings.HasPrefix(failure, "device during capture")
			switch failure {
			case "ABI", "teardown after ABI":
				item := fixture.object.defaultItems[ioCostWaitAggregateMap][0]
				item.Key = item.Key[:len(item.Key)-1]
				fixture.object.setDefaultItems(ioCostWaitAggregateMap, []bpf.MapItem{item})
				if failure == "teardown after ABI" {
					changed := ioControlTestBytes(ioControlStatus{})
					binary.LittleEndian.PutUint64(changed[8:], 1)
					fixture.object.queueStatus(ioCostCaptureTestRead{value: ioControlTestBytes(ioControlStatus{})},
						ioCostCaptureTestRead{value: changed})
				}
			case "health":
				fixture.object.statusValue = ioControlTestBytes(ioControlTestStatus(ioCostFailureWakeFrame, 0))
			case "device":
				fixture.object.deviceChanges = 1
			case "device during capture", "device during capture with unchanged count":
				setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(100, 1_000))
				_, err := fixture.collector.Update()
				require.NoError(t, err)
				count := uint64(1)
				if failure == "device during capture with unchanged count" {
					count = 100
				}
				setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(count, 10))
				fixture.object.queueStatus(
					ioCostCaptureTestRead{value: ioControlTestBytes(ioControlStatus{})},
					ioCostCaptureTestRead{value: ioControlTestBytes(ioControlStatus{DevicesChanged: 1})},
				)
			}
			data, err := fixture.collector.Update()
			if duringCapture {
				requireIOCostHostAndOtherInterval(t, data, 0, 0)
			} else {
				require.Nil(t, data)
			}
			require.Nil(t, fixture.collector.session.previous)
			require.Equal(t, 1, fixture.harness.cancelCount())
			if failure == "device" || failure == "teardown after ABI" || duringCapture {
				require.ErrorIs(t, err, errIOControlDevicesChanged)
				require.NotErrorIs(t, err, types.ErrTracingStopped)
			} else {
				require.ErrorIs(t, err, types.ErrTracingStopped)
			}
			data, err = fixture.collector.Update()
			require.Nil(t, data)
			require.ErrorIs(t, err, metric.ErrNoData)
		})
	}
}

func TestIOCostDeviceReadFailureDefersTerminalStop(t *testing.T) {
	fixture := newIOCostIntervalTestFixture(t, 1, nil)
	_, err := fixture.collector.Update()
	require.NoError(t, err)
	require.NotEmpty(t, fixture.collector.session.previous)

	failure := ioControlTestBytes(ioControlTestStatus(ioCostFailureWakeFrame, 0))
	changed := append([]byte(nil), failure...)
	binary.LittleEndian.PutUint64(changed[8:], 1)
	fixture.object.queueStatus(
		ioCostCaptureTestRead{value: failure},
		ioCostCaptureTestRead{err: unix.ENOMEM},
		ioCostCaptureTestRead{value: changed},
	)

	metrics, err := fixture.collector.Update()
	require.ErrorIs(t, err, unix.ENOMEM)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Nil(t, metrics)
	require.Nil(t, fixture.collector.session.previous)
	require.Nil(t, context.Cause(fixture.collector.session.breaker))
	require.Zero(t, fixture.harness.cancelCount())

	metrics, err = fixture.collector.Update()
	require.ErrorIs(t, err, errIOControlDevicesChanged)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Nil(t, metrics)
	require.ErrorIs(t, context.Cause(fixture.collector.session.breaker), errIOControlDevicesChanged)
	require.Equal(t, 1, fixture.harness.cancelCount())
}

func TestIOCostTransactionUpdateMutexCancelWithdrawClose(t *testing.T) {
	object := &closeAwareIOCostCaptureBPF{fakeIOCostCaptureBPF: newFakeIOCostCaptureBPF(1)}
	harness := newIOCostCaptureTestSession(t, object.fakeIOCostCaptureBPF, 1)
	harness.session.object = object
	harness.session.hashReader.lookupBatch = object.dumpBatch
	collector := &iocostTracing{session: harness.session}
	entered := make(chan string, 1)
	release := make(chan struct{})
	releaseRead := sync.OnceFunc(func() { close(release) })
	var workers sync.WaitGroup
	defer func() {
		releaseRead()
		workers.Wait()
	}()
	object.queueDumps(ioCostWaitAggregateMap, ioCostCaptureTestDump{
		items: object.defaultItems[ioCostWaitAggregateMap], entered: entered, release: release,
	})
	result := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := collector.Update()
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Update did not start its map read")
	}
	harness.cancel()
	cleanupStarted := make(chan struct{})
	cleaned := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		close(cleanupStarted)
		collector.withdrawSession()
		_ = object.Close()
		close(cleaned)
	}()
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not start")
	}
	// Keep the read blocked while cleanup has an opportunity to withdraw it.
	select {
	case <-cleaned:
		t.Fatal("session cleanup completed while its map read was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	closeCalls, _, _ := object.closeState()
	require.Zero(t, closeCalls)
	releaseRead()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Update did not finish after releasing its map read")
	}
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not finish after releasing its map read")
	}
	require.Nil(t, collector.session)
	closeCalls, readAfterClose, closeDuringRead := object.closeState()
	require.Equal(t, 1, closeCalls)
	require.False(t, readAfterClose)
	require.False(t, closeDuringRead)
}

func TestIOCostAttributionReusedCSSKeepsCounterBaselinesSeparate(t *testing.T) {
	oldContainer := newIOControlAttributionTestContainer(
		"old-container",
		ioCostCaptureTestCSS,
		time.Unix(100, 0),
	)
	newContainer := newIOControlAttributionTestContainer(
		"new-container",
		ioCostCaptureTestCSS,
		time.Unix(200, 0),
	)
	fixture := newIOCostIntervalTestFixture(
		t,
		1,
		map[string]*pod.Container{oldContainer.ID: oldContainer},
	)
	setIOCostIntervalTestRows(
		fixture.object,
		ioCostDefaultIntervalTestRow(10, 1000),
	)
	metrics, err := fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 10, 1)

	fixture.source.containers = map[string]*pod.Container{
		newContainer.ID: newContainer,
	}
	newRow := ioCostDefaultIntervalTestRow(1, 200)
	newRow.cssSerial++
	setIOCostIntervalTestRows(fixture.object, newRow)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 1, 2)
	require.NotContains(t, fixture.collector.session.previous, ioControlWaitKey{
		CSS: newRow.css, Major: newRow.major, Minor: newRow.minor, CSSSerial: ioCostCaptureTestCSSSerial,
	})
	require.Contains(t, fixture.collector.session.previous, ioControlWaitKey{
		CSS: newRow.css, Major: newRow.major, Minor: newRow.minor, CSSSerial: newRow.cssSerial,
	})

	newRow.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane(2, 500),
	}
	setIOCostIntervalTestRows(fixture.object, newRow)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 1, 3)

	newRow.lanes = []ioCostCaptureTestLane{
		ioCostCaptureStableLane(3, 900),
	}
	setIOCostIntervalTestRows(fixture.object, newRow)
	metrics, err = fixture.collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndContainerInterval(t, metrics, 1, 4)
	require.Zero(t, fixture.harness.cancelCount())
}

func TestIOCostContainerFailuresPreserveHostIntervals(t *testing.T) {
	for _, failure := range []string{"query", "labels", "empty"} {
		t.Run(failure, func(t *testing.T) {
			container := newIOControlAttributionTestContainer(
				"stable-container", ioCostCaptureTestCSS, time.Unix(100, 0),
			)
			fixture := newIOCostIntervalTestFixture(t, 1,
				map[string]*pod.Container{container.ID: container})
			switch failure {
			case "query":
				// Even a nonempty result is unusable when its source failed.
				fixture.source.err = errors.New("kubelet not running")
			case "labels":
				container.Labels[ioControlHostNamespaceKey] = 123
			case "empty":
				fixture.source.containers = nil
			}

			// An unavailable catalog must not block the first or later intervals.
			for count := uint64(3); count <= 6; count += 3 {
				setIOCostIntervalTestRows(fixture.object,
					ioCostDefaultIntervalTestRow(count, count*300))
				metrics, err := fixture.collector.Update()
				require.NoError(t, err)
				requireIOCostHostAndOtherInterval(t, metrics, 3, 3)
				require.NotNil(t, fixture.collector.session.previous)
				require.Zero(t, fixture.harness.cancelCount())
			}

			container.Labels[ioControlHostNamespaceKey] = "host"
			fixture.source.err = nil
			fixture.source.containers = map[string]*pod.Container{container.ID: container}
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(8, 2600))
			metrics, err := fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndContainerInterval(t, metrics, 2, 4)

			// A later outage must not reset an established container baseline.
			fixture.source.err = errors.New("kubelet request timeout")
			setIOCostIntervalTestRows(fixture.object, ioCostDefaultIntervalTestRow(9, 2900))
			metrics, err = fixture.collector.Update()
			require.NoError(t, err)
			requireIOCostHostAndOtherInterval(t, metrics, 1, 3)

			// Only a raw capture failure requires rebaselining. Unavailable
			// metadata must not prevent that recovery from completing either.
			captureErr := errors.New("temporary capture failure")
			fixture.object.queueDumps(ioCostWaitAggregateMap,
				ioCostCaptureTestDump{err: captureErr})
			_, err = fixture.collector.Update()
			require.ErrorIs(t, err, captureErr)
			if failure == "labels" {
				fixture.source.err = nil
				container.Labels[ioControlHostNamespaceKey] = 123
			}
			metrics, err = fixture.collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Nil(t, metrics)
			require.NotNil(t, fixture.collector.session.previous)
			require.Zero(t, fixture.harness.cancelCount())
		})
	}
}

type closeAwareIOCostCaptureBPF struct {
	*fakeIOCostCaptureBPF

	mu              sync.Mutex
	activeReads     int
	closed          bool
	closeCalls      int
	readAfterClose  bool
	closeDuringRead bool
}

func (object *closeAwareIOCostCaptureBPF) beginRead() {
	object.mu.Lock()
	defer object.mu.Unlock()
	if object.closed {
		object.readAfterClose = true
	}
	object.activeReads++
}

func (object *closeAwareIOCostCaptureBPF) endRead() {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.activeReads--
}

func (object *closeAwareIOCostCaptureBPF) DumpMap(
	mapID uint32,
) ([]bpf.MapItem, error) {
	object.beginRead()
	defer object.endRead()
	return object.fakeIOCostCaptureBPF.DumpMap(mapID)
}

func (object *closeAwareIOCostCaptureBPF) dumpBatch(mapID uint32) ([]bpf.MapItem, error) {
	return object.DumpMap(mapID)
}

func (object *closeAwareIOCostCaptureBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	object.beginRead()
	defer object.endRead()
	return object.fakeIOCostCaptureBPF.ReadMap(mapID, key)
}

func (object *closeAwareIOCostCaptureBPF) Close() error {
	object.mu.Lock()
	defer object.mu.Unlock()
	object.closeCalls++
	if object.activeReads != 0 {
		object.closeDuringRead = true
	}
	object.closed = true
	return nil
}

func (object *closeAwareIOCostCaptureBPF) closeState() (
	closeCalls int,
	readAfterClose bool,
	closeDuringRead bool,
) {
	object.mu.Lock()
	defer object.mu.Unlock()
	return object.closeCalls, object.readAfterClose, object.closeDuringRead
}

type fakeIOCostBPF struct {
	bpf.BPF

	mu              sync.Mutex
	options         []bpf.AttachOption
	attachCalls     int
	attachFailAt    int
	attachErr       error
	closeCalls      int
	closeErr        error
	sessionVisible  func() bool
	visibleAtAttach bool
	visibleAtClose  bool
}

func (object *fakeIOCostBPF) AttachWithOptions(
	options []bpf.AttachOption,
) error {
	visible := false
	if object.sessionVisible != nil {
		visible = object.sessionVisible()
	}
	object.mu.Lock()
	defer object.mu.Unlock()
	object.attachCalls++
	object.visibleAtAttach = object.visibleAtAttach || visible
	for _, option := range options {
		object.options = append(object.options, option)
		if object.attachFailAt == len(object.options) {
			return object.attachErr
		}
	}
	return nil
}

func (object *fakeIOCostBPF) Close() error {
	visible := false
	if object.sessionVisible != nil {
		visible = object.sessionVisible()
	}
	object.mu.Lock()
	object.closeCalls++
	object.visibleAtClose = object.visibleAtClose || visible
	object.mu.Unlock()
	return object.closeErr
}

type fakeIOCostBPFState struct {
	options        []bpf.AttachOption
	attachCalls    int
	closeCalls     int
	visibleAtClose bool
}

func (object *fakeIOCostBPF) state() fakeIOCostBPFState {
	object.mu.Lock()
	defer object.mu.Unlock()
	return fakeIOCostBPFState{
		options:        append([]bpf.AttachOption(nil), object.options...),
		attachCalls:    object.attachCalls,
		closeCalls:     object.closeCalls,
		visibleAtClose: object.visibleAtClose,
	}
}

func ioCostSessionTestProfile() *ioCostKernelProfile {
	return &ioCostKernelProfile{
		throttleRange: symbol.KsymbolRange{
			Start: 0x2000,
			End:   0x2100,
		},
		overBudgetRange: symbol.KsymbolRange{
			Start: 0x3000,
			End:   0x3100,
		},
	}
}

func ioCostSessionTestAttachOptions(t *testing.T) []bpf.AttachOption {
	t.Helper()
	options, err := ioControlDeviceHooks()
	require.NoError(t, err)
	options = append(options, []bpf.AttachOption{
		{ProgramName: ioCostWakeReturnProgram, Symbol: ioCostWakeSymbol},
		{ProgramName: ioCostWakeEntryProgram, Symbol: ioCostWakeSymbol},
		{ProgramName: ioCostPDFreeProgram, Symbol: ioCostPDFreeSymbol},
	}...)
	return append(options, bpf.AttachOption{
		ProgramName: ioCostKickProgram,
		Symbol:      ioCostKickSymbol,
	})
}

func emptyIOCostContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

func TestIOCostAttachFailureClosesObject(t *testing.T) {
	attachErr := fmt.Errorf("attach failure: %w", unix.EPERM)
	closeErr := errors.New("close failure")
	object := &fakeIOCostBPF{}
	object.attachFailAt = len(ioCostSessionTestAttachOptions(t))
	object.attachErr = attachErr
	object.closeErr = closeErr
	collector := &iocostTracing{}
	object.sessionVisible = func() bool {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		return collector.session != nil
	}
	load := func(string, map[string]any) (bpf.BPF, error) { return object, nil }
	err := collector.startWithProfile(
		t.Context(),
		load,
		ioCostSessionTestProfile(),
		1,
		emptyIOCostContainerSource,
	)
	require.ErrorIs(t, err, attachErr)
	require.ErrorIs(t, err, closeErr)
	require.Nil(t, collector.session)
	state := object.state()
	require.Equal(t, 1, state.closeCalls)
	require.False(t, state.visibleAtClose)
	require.False(t, object.visibleAtAttach)
}

func TestIOCostStartPrePublishFailures(t *testing.T) {
	loadErr := errors.New("load failure")
	object := &fakeIOCostBPF{}
	collector := &iocostTracing{}
	err := collector.startWithProfile(
		t.Context(),
		func(string, map[string]any) (bpf.BPF, error) { return object, loadErr },
		ioCostSessionTestProfile(),
		1,
		emptyIOCostContainerSource,
	)
	require.ErrorIs(t, err, loadErr)
	state := object.state()
	require.Zero(t, state.attachCalls)
	require.Equal(t, 1, state.closeCalls)
	require.Nil(t, collector.session)
}

// Compose the attach and capture fakes to verify that Update's terminal cause
// survives Start's session cleanup.
type ioCostSessionCaptureBPF struct {
	*fakeIOCostBPF
	capture *fakeIOCostCaptureBPF
}

func (object *ioCostSessionCaptureBPF) MapIDByName(name string) uint32 {
	return object.capture.MapIDByName(name)
}

func (object *ioCostSessionCaptureBPF) ReadMap(id uint32, key []byte) ([]byte, error) {
	return object.capture.ReadMap(id, key)
}

func (object *ioCostSessionCaptureBPF) DumpMap(id uint32) ([]bpf.MapItem, error) {
	return object.capture.DumpMap(id)
}

func (object *ioCostSessionCaptureBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	return object.capture.DumpMapByName(name)
}

func (object *ioCostSessionCaptureBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	return object.capture.DeleteMapItems(id, keys)
}

func (object *ioCostSessionCaptureBPF) dumpBatch(id uint32) ([]bpf.MapItem, error) {
	return object.DumpMap(id)
}

func waitIOCostTestSession(t *testing.T, collector *iocostTracing) *ioCostSession {
	t.Helper()
	var session *ioCostSession
	require.Eventually(t, func() bool {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		session = collector.session
		if session != nil {
			if object, ok := session.object.(ioControlTestBPF); ok {
				session.hashReader.lookupBatch = object.dumpBatch
			}
		}
		return session != nil
	}, time.Second, time.Millisecond)
	return session
}

func TestIOCostStartStopsOnAccountingFailure(t *testing.T) {
	const workerEnv = "HUATUO_TEST_IOCOST_FAILURE_WORKER"
	if os.Getenv(workerEnv) == "" {
		// Symbol fixtures must not populate the parent process's symbol cache.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(
			ctx,
			os.Args[0],
			"-test.run=^TestIOCostStartStopsOnAccountingFailure$",
			"-test.v",
		)
		command.Env = append(os.Environ(), workerEnv+"=1")
		output, err := command.CombinedOutput()
		require.NoError(
			t,
			err,
			"%s",
			output,
		)
		t.Logf("%s", output)
		return
	}

	status := ioControlTestBytes(ioControlTestStatus(ioCostFailurePendingDelete, -int32(unix.EIO)))
	const reason = "pending missing or deletion failed"
	ioControlTestDiskstats(t)
	require.NoError(t, os.WriteFile(procfs.Path("kallsyms"), nil, 0o600))
	object := &ioCostSessionCaptureBPF{
		fakeIOCostBPF: &fakeIOCostBPF{},
		capture:       newFakeIOCostCaptureBPF(1),
	}
	collector := &iocostTracing{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var startErr error
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("IOCOST Start did not finish cleanup")
		}
	})
	go func() {
		defer close(done)
		startErr = collector.startWithProfile(
			ctx,
			func(string, map[string]any) (bpf.BPF, error) { return object, nil },
			ioCostSessionTestProfile(),
			1,
			emptyIOCostContainerSource,
		)
	}()
	session := waitIOCostTestSession(t, collector)
	data, err := collector.Update()
	require.NoError(t, err)
	requireIOCostHostAndOtherInterval(
		t,
		data,
		1,
		10,
	)
	setIOCostIntervalTestRows(object.capture, ioCostDefaultIntervalTestRow(2, 2_000))
	object.capture.queueStatus(ioCostCaptureTestRead{value: status})

	data, err = collector.Update()
	require.Nil(t, data)
	require.ErrorIs(t, err, types.ErrTracingStopped)
	require.ErrorContains(t, err, reason)
	select {
	case <-done:
		require.ErrorIs(t, startErr, types.ErrTracingStopped)
		require.ErrorContains(t, startErr, reason)
	case <-time.After(time.Second):
		t.Fatal("accounting failure did not stop IOCOST Start")
	}
	require.Nil(t, session.previous)
	require.Nil(t, collector.session)
	require.Equal(t, 1, object.state().closeCalls)

	data, err = collector.Update()
	require.Nil(t, data)
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Equal(t, 1, object.state().closeCalls)
}

func TestIOCostStartSequenceAndCleanup(t *testing.T) {
	object := &fakeIOCostBPF{}
	collector := &iocostTracing{}
	object.sessionVisible = func() bool {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		return collector.session != nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- collector.startWithProfile(ctx,
			func(name string, constants map[string]any) (bpf.BPF, error) {
				if name != ioCostObjectName {
					return nil, fmt.Errorf("object %s", name)
				}
				return object, nil
			}, ioCostSessionTestProfile(), 4, emptyIOCostContainerSource)
	}()
	session := waitIOCostTestSession(t, collector)
	require.Same(t, object, session.object)
	require.Equal(t, 4, session.possibleCPUs)
	require.NotNil(t, session.previous)
	require.Empty(t, session.previous)
	require.Equal(t, ioCostSessionTestAttachOptions(t), object.state().options)
	require.False(t, object.visibleAtAttach)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("IOCOST Start did not finish")
	}
	require.Nil(t, collector.session)
	state := object.state()
	require.Equal(t, 1, state.closeCalls)
	require.False(t, state.visibleAtClose)
}

func TestIOCostStartStopsOnUpdate(t *testing.T) {
	object := &ioCostSessionCaptureBPF{
		fakeIOCostBPF: &fakeIOCostBPF{},
		capture:       newFakeIOCostCaptureBPF(1),
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	collector := &iocostTracing{}
	done := make(chan error, 1)
	go func() {
		done <- collector.startWithProfile(
			ctx,
			func(string, map[string]any) (bpf.BPF, error) { return object, nil },
			ioCostSessionTestProfile(),
			1,
			emptyIOCostContainerSource,
		)
	}()
	waitIOCostTestSession(t, collector)
	object.capture.deviceChanges = 1
	data, err := collector.Update()
	require.Nil(t, data)
	require.ErrorIs(t, err, errIOControlDevicesChanged)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Update did not end IOCOST Start")
	}
	require.Nil(t, collector.session)
	require.Equal(t, 1, object.state().closeCalls)
}
