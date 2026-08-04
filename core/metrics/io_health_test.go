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

// Tests cover IO health events, target identity, hook selection, and collector lifecycle.
package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/cilium/ebpf/btf"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"golang.org/x/sys/unix"
)

// IO health events and target identity.

// Kernel event routing and evidence accounting.

type recordingEvidenceSubmitter struct {
	requests []iohealth.EvidenceRequest
	accept   bool
}

//nolint:gocritic // Match the production value-transfer interface.
func (s *recordingEvidenceSubmitter) Submit(request iohealth.EvidenceRequest) bool {
	s.requests = append(s.requests, request)
	return s.accept
}

type recordedIOHealthEvent struct {
	at    time.Time
	event types.IOHealthEvent
}

type ioHealthEventRecorder struct {
	events []recordedIOHealthEvent
}

//nolint:gocritic // Record the same immutable value passed to storage.
func (r *ioHealthEventRecorder) save(
	at time.Time,
	event types.IOHealthEvent,
) error {
	r.events = append(r.events, recordedIOHealthEvent{at: at, event: event})
	return nil
}

func newRecordingIOHealthCollector(
	t *testing.T,
	root string,
) (*ioHealthCollector, *ioHealthEventRecorder) {
	t.Helper()
	collector := newIOHealthCollector(root, filepath.Join(root, "proc", "mdstat"))
	recorder := &ioHealthEventRecorder{}
	collector.saveEvent = recorder.save
	return collector, recorder
}

func writeIOHealthBlockDevice(
	t *testing.T,
	root, device string,
	major, minor uint32,
) uint32 {
	t.Helper()
	blockPath := filepath.Join(root, "class", "block", device)
	if err := os.MkdirAll(filepath.Join(blockPath, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	devBlockPath := filepath.Join(root, "dev", "block")
	if err := os.MkdirAll(devBlockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		blockPath,
		filepath.Join(devBlockPath, ioHealthDevName(major, minor)),
	); err != nil {
		t.Fatal(err)
	}
	return major<<20 | minor
}

func ioHealthDevName(major, minor uint32) string {
	return fmt.Sprintf("%d:%d", major, minor)
}

func ioHealthControllerBytes(name string) [ioHealthNVMeControllerNameLength]uint8 {
	var raw [ioHealthNVMeControllerNameLength]uint8
	copy(raw[:], name)
	return raw
}

func TestIOHealthKernelABIAndLabels(t *testing.T) {
	if size := binary.Size(ioHealthPerfEvent{}); size != 56 {
		t.Fatalf("ioHealthPerfEvent binary size = %d, want 56", size)
	}
	eventTypes := []int{
		ioHealthEventBlockError,
		ioHealthEventSCSITimeout,
		ioHealthEventSCSIDispatchError,
		ioHealthEventNVMeTimeout,
		ioHealthEventNVMeReset,
		ioHealthEventNVMeStateChange,
	}
	if want := []int{1, 2, 3, 4, 5, 6}; !reflect.DeepEqual(eventTypes, want) {
		t.Fatalf("kernel event wire values = %v, want %v", eventTypes, want)
	}

	for _, test := range []struct {
		raw  uint8
		want string
	}{
		{raw: reqOpRead, want: "read"},
		{raw: reqOpWrite, want: "write"},
		{raw: reqOpFlush, want: "flush"},
		{raw: reqOpDiscard, want: "discard"},
		{raw: 255, want: "unknown"},
	} {
		if got := ioHealthOperation(test.raw); got != test.want {
			t.Errorf("ioHealthOperation(%d) = %q, want %q", test.raw, got, test.want)
		}
	}

	for _, test := range []struct {
		raw  int32
		want string
	}{
		{raw: -int32(unix.ETIMEDOUT), want: "timeout"},
		{raw: -int32(unix.ENODATA), want: "medium_error"},
		{raw: -int32(unix.ENOMEM), want: "resource"},
		{raw: -int32(unix.ENODEV), want: "offline"},
		{raw: 16, want: "io_error"},
	} {
		if got := ioHealthBlockStatus(test.raw); got != test.want {
			t.Errorf("ioHealthBlockStatus(%d) = %q, want %q", test.raw, got, test.want)
		}
	}

	if got := ioHealthSCSIDispatchStatus(scsiMLQueueTargetBusy); got != "target_busy" {
		t.Fatalf("target-busy status = %q", got)
	}
	if got := ioHealthControllerName(ioHealthControllerBytes("nvme12")); got != "nvme12" {
		t.Fatalf("controller name = %q, want nvme12", got)
	}
	if got := ioHealthControllerName(ioHealthControllerBytes("sda")); got != "unknown" {
		t.Fatalf("invalid controller name = %q, want unknown", got)
	}

}

func TestIOHealthPersistsEachEvidenceTriggerExactlyOnce(t *testing.T) {
	root := t.TempDir()
	dev := writeIOHealthBlockDevice(t, root, "sda", 8, 0)
	collector, recorder := newRecordingIOHealthCollector(t, root)
	triggeredAt := []time.Time{time.Unix(123, 456), time.Unix(124, 456)}
	nextTime := 0
	collector.now = func() time.Time {
		at := triggeredAt[nextTime]
		nextTime++
		return at
	}
	submitter := &recordingEvidenceSubmitter{accept: true}
	raw := ioHealthPerfEvent{
		Dev:       dev,
		Status:    -int32(unix.ETIMEDOUT),
		Type:      ioHealthEventBlockError,
		Operation: reqOpRead,
	}

	collector.handleKernelEvent(raw, submitter)
	if len(recorder.events) != 0 {
		t.Fatalf("accepted request saved %d immediate events", len(recorder.events))
	}
	if len(submitter.requests) != 1 {
		t.Fatalf("submitted requests = %d, want 1", len(submitter.requests))
	}
	request := submitter.requests[0]
	if request.Target != "sda" ||
		request.Protocol != iohealth.EvidenceProtocolSCSI ||
		request.TriggeredAt != triggeredAt[0] {
		t.Fatalf("request = %+v", request)
	}
	if request.Trigger.Device != "sda" ||
		request.Trigger.Operation != "read" ||
		request.Trigger.Status != "timeout" ||
		request.Trigger.Sector == nil ||
		*request.Trigger.Sector != 0 {
		t.Fatalf("trigger = %+v", request.Trigger)
	}

	submitter.accept = false
	collector.handleKernelEvent(raw, submitter)
	if len(recorder.events) != 1 || recorder.events[0].event.Type != ioHealthTypeBlockError {
		t.Fatalf("suppressed event records = %+v", recorder.events)
	}
	collector.handleEvidenceResult(iohealth.EvidenceResult{
		Target:      submitter.requests[0].Target,
		TriggeredAt: submitter.requests[0].TriggeredAt,
		Event:       submitter.requests[0].Trigger,
	})
	if len(recorder.events) != 2 {
		t.Fatalf("two raw events produced %d records", len(recorder.events))
	}
	recordsByTime := make(map[time.Time]int)
	for _, record := range recorder.events {
		recordsByTime[record.at]++
	}
	for _, at := range triggeredAt {
		if recordsByTime[at] != 1 {
			t.Fatalf("records by trigger time = %v, want one for %v", recordsByTime, at)
		}
	}
	key := ioHealthCounterKey{
		kind:      ioHealthCounterBlockError,
		device:    "sda",
		operation: "read",
		status:    "timeout",
	}
	if got := collector.counters[key]; got != 2 {
		t.Fatalf("block error counter = %d, want 2", got)
	}
}

func TestIOHealthRoutesNVMeEventsWithoutResetEvidence(t *testing.T) {
	root := t.TempDir()
	dev := writeIOHealthBlockDevice(t, root, "nvme0c7n1", 259, 0)
	collector, recorder := newRecordingIOHealthCollector(t, root)
	collector.nvmeStates = map[uint32]string{1: "live"}
	submitter := &recordingEvidenceSubmitter{accept: true}

	collector.handleKernelEvent(ioHealthPerfEvent{
		Dev:  dev,
		Type: ioHealthEventNVMeTimeout,
	}, submitter)
	if len(submitter.requests) != 1 ||
		submitter.requests[0].Target != "nvme7" ||
		submitter.requests[0].Protocol != iohealth.EvidenceProtocolNVMe {
		t.Fatalf("NVMe timeout requests = %+v", submitter.requests)
	}

	collector.handleKernelEvent(ioHealthPerfEvent{
		Type: ioHealthEventNVMeTimeout,
	}, submitter)
	collector.handleKernelEvent(ioHealthPerfEvent{
		Type:       ioHealthEventNVMeReset,
		Controller: ioHealthControllerBytes("nvme7"),
	}, submitter)
	collector.handleKernelEvent(ioHealthPerfEvent{
		Type: ioHealthEventNVMeReset,
	}, submitter)
	collector.handleKernelEvent(ioHealthPerfEvent{
		Type:        ioHealthEventNVMeStateChange,
		Controller:  ioHealthControllerBytes("nvme7"),
		NewStateRaw: 1,
	}, submitter)

	if len(submitter.requests) != 1 {
		t.Fatalf("admin/reset/state submitted requests = %+v", submitter.requests)
	}
	if len(recorder.events) != 4 {
		t.Fatalf("direct NVMe events = %+v", recorder.events)
	}
	for index, want := range []struct {
		typeName string
		device   string
	}{
		{typeName: ioHealthTypeNVMeTimeout, device: "unknown"},
		{typeName: ioHealthTypeNVMeReset, device: "nvme7"},
		{typeName: ioHealthTypeNVMeReset, device: "unknown"},
		{typeName: ioHealthTypeNVMeStateChange, device: "nvme7"},
	} {
		got := recorder.events[index].event
		if got.Type != want.typeName || got.Device != want.device {
			t.Fatalf("NVMe event %d = %+v, want %+v", index, got, want)
		}
	}
	state := recorder.events[3].event
	if state.NewState != "live" ||
		state.NewStateRaw == nil || *state.NewStateRaw != 1 {
		t.Fatalf("NVMe state event = %+v", state)
	}
	if got := collector.counters[ioHealthCounterKey{
		kind:   ioHealthCounterNVMeReset,
		device: "nvme7",
	}]; got != 1 {
		t.Fatalf("NVMe reset counter = %d, want 1", got)
	}
}

func TestIOHealthRoutesFullSCSIHCTL(t *testing.T) {
	root := t.TempDir()
	const (
		host    = uint32(70000)
		channel = uint32(80000)
		target  = uint32(90000)
		lun     = uint32(100000)
	)
	if err := os.MkdirAll(filepath.Join(
		root,
		"class",
		"scsi_device",
		"70000:80000:90000:100000",
		"device",
		"block",
		"sdz",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	collector, _ := newRecordingIOHealthCollector(t, root)
	submitter := &recordingEvidenceSubmitter{accept: true}

	collector.handleKernelEvent(ioHealthPerfEvent{
		Type:    ioHealthEventSCSIDispatchError,
		Status:  scsiMLQueueTargetBusy,
		Host:    host,
		Channel: channel,
		Target:  target,
		LUN:     lun,
	}, submitter)

	if len(submitter.requests) != 1 {
		t.Fatalf("SCSI requests = %+v", submitter.requests)
	}
	request := submitter.requests[0]
	if request.Target != "sdz" ||
		request.Protocol != iohealth.EvidenceProtocolSCSI ||
		request.Trigger.Status != "target_busy" {
		t.Fatalf("SCSI request = %+v", request)
	}
}

func TestIOHealthRoutesMDTransitionsAndFaultEvidence(t *testing.T) {
	root := t.TempDir()
	writeIOHealthBlockDevice(t, root, "sdb", 8, 16)
	collector, recorder := newRecordingIOHealthCollector(t, root)
	submitter := &recordingEvidenceSubmitter{accept: true}
	observedAt := time.Unix(321, 654)

	for _, change := range []iohealth.MDChange{
		{
			Array:      "md0",
			Field:      iohealth.MDFieldSyncAction,
			OldState:   "idle",
			NewState:   "recover",
			ObservedAt: observedAt,
		},
		{
			Array:      "md0",
			Field:      iohealth.MDFieldDegraded,
			OldState:   "0",
			NewState:   "1",
			ObservedAt: observedAt,
		},
		{
			Array:      "md0",
			Member:     "sdb",
			Field:      iohealth.MDFieldMemberState,
			OldState:   "spare",
			NewState:   "in_sync",
			ObservedAt: observedAt,
		},
	} {
		collector.handleMDChange(change, submitter)
	}
	if len(recorder.events) != 3 || len(submitter.requests) != 0 {
		t.Fatalf("ordinary MD records=%+v requests=%+v", recorder.events, submitter.requests)
	}

	for _, state := range []string{
		"faulty",
		"blocked",
		"in_sync,write_error",
		iohealth.MDMemberStateRemoved,
	} {
		collector.handleMDChange(iohealth.MDChange{
			Array:      "md0",
			Member:     "sdb",
			Field:      iohealth.MDFieldMemberState,
			OldState:   "in_sync",
			NewState:   state,
			ObservedAt: observedAt,
		}, submitter)
	}
	if len(submitter.requests) != 4 {
		t.Fatalf("fault evidence requests = %+v", submitter.requests)
	}
	for _, request := range submitter.requests {
		if request.Target != "sdb" ||
			request.Protocol != iohealth.EvidenceProtocolSCSI ||
			request.Trigger.Type != ioHealthTypeMDMemberState ||
			request.Trigger.Array != "md0" ||
			request.Trigger.Member != "sdb" {
			t.Fatalf("MD fault request = %+v", request)
		}
	}
}

func TestIOHealthEvidenceResultAddsOnlyCountersToScrape(t *testing.T) {
	collector, recorder := newRecordingIOHealthCollector(t, t.TempDir())
	if _, err := collector.Update(); !metric.IsNoDataError(err) {
		t.Fatalf("empty Update() error = %v, want ErrNoData", err)
	}

	event := types.IOHealthEvent{
		Type:   ioHealthTypeSCSITimeout,
		Device: "sda",
		SCSI:   &types.SCSIHealthEvidence{},
	}
	collector.handleEvidenceResult(iohealth.EvidenceResult{
		Target:      "sda",
		TriggeredAt: time.Unix(1, 2),
		Event:       event,
		Reasons:     []string{iohealth.CollectionReasonParseError},
	})
	if len(recorder.events) != 1 || recorder.events[0].event.SCSI == nil {
		t.Fatalf("saved evidence events = %+v", recorder.events)
	}
	metrics, err := collector.Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(metrics) != 1 || metrics[0].Value != 1 {
		t.Fatalf("scrape metrics = %+v, want one collection-error counter", metrics)
	}
}

// Target resolution and generation identity.

func TestIOHealthResolverNormalizesRecursivePartitionLeaf(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "block", "sda")
	partition := filepath.Join(physical, "sda1")
	if err := os.MkdirAll(filepath.Join(physical, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(partition, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partition, "partition"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	classBlock := filepath.Join(root, "class", "block")
	if err := os.MkdirAll(filepath.Join(classBlock, "dm-0", "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, filepath.Join(classBlock, "sda")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(partition, filepath.Join(classBlock, "sda1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		partition,
		filepath.Join(classBlock, "dm-0", "slaves", "sda1"),
	); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("dm-0")
	if target.eventDevice != "dm-0" ||
		target.target != "sda" ||
		target.protocol != ioHealthProtocolSCSI ||
		target.reason != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverRejectsMultipleLeaves(t *testing.T) {
	root := t.TempDir()
	classBlock := filepath.Join(root, "class", "block")
	for _, device := range []string{"sda", "sdb"} {
		if err := os.MkdirAll(
			filepath.Join(classBlock, device, "slaves"),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(classBlock, "dm-0", "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"sda", "sdb"} {
		if err := os.Symlink(
			filepath.Join(classBlock, device),
			filepath.Join(classBlock, "dm-0", "slaves", device),
		); err != nil {
			t.Fatal(err)
		}
	}

	target := newIOHealthResolver(root).resolveBlockName("dm-0")
	if target.reason != iohealth.CollectionReasonTargetUnsupported ||
		target.target != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverUsesExplicitNVMeControllerPath(t *testing.T) {
	root := t.TempDir()
	classBlock := filepath.Join(root, "class", "block")
	if err := os.MkdirAll(
		filepath.Join(classBlock, "nvme0c1n1", "slaves"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("nvme0c1n1")
	if target.target != "nvme1" ||
		target.protocol != ioHealthProtocolNVMe ||
		target.reason != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverRejectsUnattributedNVMeMultipathHead(t *testing.T) {
	root := t.TempDir()
	namespace := filepath.Join(
		root,
		"devices",
		"virtual",
		"nvme-subsystem",
		"nvme-subsys0",
		"nvme0n1",
	)
	if err := os.MkdirAll(namespace, 0o755); err != nil {
		t.Fatal(err)
	}
	classNamespace := filepath.Join(root, "class", "block", "nvme0n1")
	if err := os.MkdirAll(filepath.Join(classNamespace, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(namespace, filepath.Join(classNamespace, "device")); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("nvme0n1")
	if target.reason != iohealth.CollectionReasonTargetUnsupported || target.target != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverTreatsMissingDeviceAsUnresolved(t *testing.T) {
	target := newIOHealthResolver(t.TempDir()).resolveBlockName("gone")
	if target.reason != iohealth.CollectionReasonTargetUnresolved {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverPrimesOnlyControllerStateFiles(t *testing.T) {
	root := t.TempDir()
	nvmeClass := filepath.Join(root, "class", "nvme")
	controllerState := filepath.Join(nvmeClass, "nvme0", "state")
	if err := os.MkdirAll(filepath.Dir(controllerState), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controllerState, []byte("live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignoredState := filepath.Join(nvmeClass, "nvme0n1", "state")
	if err := os.MkdirAll(ignoredState, 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := newIOHealthResolver(root)
	if err := resolver.primeNVMeControllerNames(); err != nil {
		t.Fatalf("primeNVMeControllerNames() error = %v", err)
	}
	if err := os.Remove(controllerState); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(controllerState, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := resolver.primeNVMeControllerNames(); err == nil {
		t.Fatal("primeNVMeControllerNames() error = nil, want state read error")
	}
}

// IO health hooks and collector lifecycle.

// Collector lifecycle and persistence boundaries.

type fakeIOHealthMDWatcher struct {
	startErr error
	changes  chan iohealth.MDChange
	stop     chan error
	ctx      context.Context
}

type fakeIOHealthPerfReader struct {
	ctx context.Context
}

func (r *fakeIOHealthPerfReader) ReadInto(any) error {
	<-r.ctx.Done()
	return types.ErrExitByCancelCtx
}

func (r *fakeIOHealthPerfReader) ReadBatch(func() any) (bpf.PerfEventBatch, error) {
	return bpf.PerfEventBatch{}, errors.New("unexpected batch read")
}

func (r *fakeIOHealthPerfReader) Close() error {
	return nil
}

type fakeIOHealthRuntimeBPF struct {
	bpf.BPF
}

func (b *fakeIOHealthRuntimeBPF) AttachWithOptions([]bpf.AttachOption) error {
	return nil
}

func (b *fakeIOHealthRuntimeBPF) DetachProgram(string) error {
	return nil
}

func (b *fakeIOHealthRuntimeBPF) EventPipeByName(
	ctx context.Context,
	_ string,
	_ uint32,
) (bpf.PerfEventReader, error) {
	return &fakeIOHealthPerfReader{ctx: ctx}, nil
}

func (b *fakeIOHealthRuntimeBPF) Close() error {
	return nil
}

func newFakeIOHealthMDWatcher() *fakeIOHealthMDWatcher {
	return &fakeIOHealthMDWatcher{
		changes: make(chan iohealth.MDChange, 1),
		stop:    make(chan error, 1),
	}
}

func (w *fakeIOHealthMDWatcher) Start(ctx context.Context) error {
	if w.startErr != nil {
		return w.startErr
	}
	w.ctx = ctx
	return nil
}

func (w *fakeIOHealthMDWatcher) Wait() error {
	select {
	case err := <-w.stop:
		return err
	case <-w.ctx.Done():
		return nil
	}
}

func (w *fakeIOHealthMDWatcher) Changes() <-chan iohealth.MDChange {
	return w.changes
}

func waitIOHealthAttempt(t *testing.T, attempts <-chan int, want int) {
	t.Helper()
	select {
	case got := <-attempts:
		if got != want {
			t.Fatalf("watcher attempt = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for watcher attempt %d", want)
	}
}

func TestIOHealthMDSupervisorRetriesStartAndRuntimeFailures(t *testing.T) {
	watchers := []*fakeIOHealthMDWatcher{
		{startErr: errors.New("incomplete baseline")},
		newFakeIOHealthMDWatcher(),
		newFakeIOHealthMDWatcher(),
	}
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	attempts := make(chan int, len(watchers))
	next := 0
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		next++
		attempts <- next
		return watchers[next-1]
	}
	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	retry := make(chan time.Time, 2)
	done := make(chan struct{})
	go func() {
		collector.superviseMDWatcher(
			ctx,
			&recordingEvidenceSubmitter{accept: true},
			retry,
		)
		close(done)
	}()

	waitIOHealthAttempt(t, attempts, 1)
	retry <- time.Now()
	waitIOHealthAttempt(t, attempts, 2)
	watchers[1].stop <- errors.New("poll failed")
	retry <- time.Now()
	waitIOHealthAttempt(t, attempts, 3)
	watchers[2].changes <- iohealth.MDChange{
		Array:      "md0",
		Field:      iohealth.MDFieldSyncAction,
		OldState:   "idle",
		NewState:   "recover",
		ObservedAt: time.Unix(1, 2),
	}
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeMDSyncAction || event.Array != "md0" {
			t.Fatalf("saved MD event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for MD event after restart")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MD supervisor did not stop after cancellation")
	}
}

func TestIOHealthFinishMDWatcherDrainsChanges(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	changes := make(chan iohealth.MDChange, 1)
	changes <- iohealth.MDChange{
		Array:      "md0",
		Field:      iohealth.MDFieldSyncAction,
		OldState:   "idle",
		NewState:   "recover",
		ObservedAt: time.Unix(1, 2),
	}

	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}
	err := collector.finishMDWatcher(
		errors.New("poll failed"),
		changes,
		&recordingEvidenceSubmitter{accept: true},
	)
	if err == nil || err.Error() != "poll failed" {
		t.Fatalf("watcher error = %v, want poll failed", err)
	}
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeMDSyncAction {
			t.Fatalf("saved drained MD event = %+v", event)
		}
	default:
		t.Fatal("buffered MD event was not drained")
	}
}

func TestIOHealthStartKeepsMDActiveAcrossBPFLoadFailures(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	watcher := newFakeIOHealthMDWatcher()
	mdStarted := make(chan struct{}, 1)
	mdAttempts := 0
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		mdAttempts++
		mdStarted <- struct{}{}
		return watcher
	}
	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}
	bpfAttempts := make(chan int, 2)
	releaseFirstLoad := make(chan struct{})
	attempt := 0
	loadBPF := func(string, map[string]any) (bpf.BPF, error) {
		attempt++
		bpfAttempts <- attempt
		if attempt == 1 {
			<-releaseFirstLoad
			return nil, errors.New("BPF unavailable")
		}
		return &fakeIOHealthRuntimeBPF{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- collector.start(ctx, loadBPF, time.Millisecond)
	}()

	select {
	case <-mdStarted:
	case <-time.After(time.Second):
		t.Fatal("MD watcher did not start")
	}
	watcher.changes <- iohealth.MDChange{
		Array:      "md0",
		Field:      iohealth.MDFieldSyncAction,
		OldState:   "idle",
		NewState:   "recover",
		ObservedAt: time.Unix(1, 2),
	}
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeMDSyncAction {
			t.Fatalf("saved MD event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("MD watcher did not consume changes during BPF failures")
	}
	waitIOHealthAttempt(t, bpfAttempts, 1)
	close(releaseFirstLoad)
	waitIOHealthAttempt(t, bpfAttempts, 2)
	select {
	case err := <-done:
		t.Fatalf("collector stopped after BPF failure: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("collector stop error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after cancellation")
	}
	if mdAttempts != 1 {
		t.Fatalf("MD watcher starts = %d, want 1", mdAttempts)
	}
}

func TestIOHealthCancellationDuringBPFLoadIsNormal(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	watcher := newFakeIOHealthMDWatcher()
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		return watcher
	}
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	loadBPF := func(string, map[string]any) (bpf.BPF, error) {
		close(loadStarted)
		<-releaseLoad
		return nil, errors.New("BPF unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- collector.start(ctx, loadBPF, time.Hour)
	}()
	<-loadStarted
	cancel()
	close(releaseLoad)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("collector stop error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after cancellation")
	}
}

// Optional hook selection and attachment recovery.

type fakeIOHealthAttachBPF struct {
	bpf.BPF
	attachErrors map[string]error
	detachErr    error
	detachAfter  []int
	detached     []string
	programs     []string
	symbols      []string
}

func (f *fakeIOHealthAttachBPF) AttachWithOptions(options []bpf.AttachOption) error {
	if len(options) != 1 {
		return errors.New("test expects one independent attach option")
	}
	f.programs = append(f.programs, options[0].ProgramName)
	f.symbols = append(f.symbols, options[0].Symbol)
	return f.attachErrors[options[0].ProgramName]
}

func (f *fakeIOHealthAttachBPF) DetachProgram(programName string) error {
	f.detachAfter = append(f.detachAfter, len(f.programs))
	f.detached = append(f.detached, programName)
	return f.detachErr
}

func TestAttachIOHealthHooksDetachesNVMeBootstrapAfterHotplugHooks(t *testing.T) {
	object := &fakeIOHealthAttachBPF{}
	primeAfter := -1
	attached := attachIOHealthHooks(object, func() error {
		primeAfter = len(object.programs)
		return nil
	})

	if want := len(ioHealthHooks) + 2; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	wantPrograms := []string{
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_del",
		"kprobe_nvme_sysfs_show_state",
		"trace_block_rq_error",
		"kretprobe_nvme_change_state",
		"kprobe_nvme_change_state",
	}
	for _, hook := range ioHealthHooks {
		wantPrograms = append(wantPrograms, hook.program)
	}
	if !reflect.DeepEqual(object.programs, wantPrograms) {
		t.Fatalf("attach programs = %v, want %v", object.programs, wantPrograms)
	}
	if got := object.symbols[4]; got != "block/block_rq_error" {
		t.Fatalf("block error symbol = %q, want block/block_rq_error", got)
	}
	if primeAfter != 4 {
		t.Fatalf("NVMe bootstrap ran after %d attaches, want 4", primeAfter)
	}
	if !reflect.DeepEqual(object.detachAfter, []int{4}) {
		t.Fatalf("NVMe bootstrap detach points = %v, want [4]", object.detachAfter)
	}
	if !reflect.DeepEqual(object.detached, []string{"kprobe_nvme_sysfs_show_state"}) {
		t.Fatalf("detached programs = %v", object.detached)
	}
}

func TestAttachIOHealthHooksDegradesOptionalSources(t *testing.T) {
	object := &fakeIOHealthAttachBPF{attachErrors: map[string]error{
		"trace_block_rq_error":         errors.New("remote attach failed"),
		"kprobe_nvme_sysfs_show_state": errors.New("mapping unavailable"),
		"kretprobe_nvme_change_state":  errors.New("state unavailable"),
	}}
	primed := false
	attached := attachIOHealthHooks(object, func() error {
		primed = true
		return nil
	})

	if primed {
		t.Fatal("NVMe bootstrap ran without the mapping hook")
	}
	if want := len(ioHealthHooks) + 1; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	if len(object.detachAfter) != 0 {
		t.Fatalf("detached without the NVMe bootstrap hook: %v", object.detachAfter)
	}
	for _, program := range object.programs {
		if program == "kprobe_nvme_change_state" {
			t.Fatalf("state entry attached after return hook failed: %v", object.programs)
		}
	}
	wantPrefix := []string{
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_del",
		"kprobe_nvme_sysfs_show_state",
		"trace_block_rq_error",
		"trace_block_rq_complete_error",
		"kretprobe_nvme_change_state",
	}
	if !reflect.DeepEqual(object.programs[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("attach prefix = %v, want %v", object.programs, wantPrefix)
	}
	if got := object.symbols[4:6]; !reflect.DeepEqual(got, []string{
		"block/block_rq_error",
		"block_rq_complete",
	}) {
		t.Fatalf("block error symbols = %v", got)
	}
}

func TestAttachIOHealthHooksContinuesAfterNVMePrimeFailure(t *testing.T) {
	object := &fakeIOHealthAttachBPF{}
	attached := attachIOHealthHooks(object, func() error {
		return errors.New("read controller state")
	})

	if want := len(ioHealthHooks) + 2; attached != want {
		t.Fatalf("attached event sources = %d, want %d", attached, want)
	}
	if len(object.programs) != len(ioHealthHooks)+7 {
		t.Fatalf("attach programs after prime failure = %v", object.programs)
	}
	if !reflect.DeepEqual(object.detachAfter, []int{4}) {
		t.Fatalf("NVMe bootstrap detach points = %v, want [4]", object.detachAfter)
	}
}

func TestAttachIOHealthHooksStopsWhenNVMeBootstrapCannotDetach(t *testing.T) {
	object := &fakeIOHealthAttachBPF{detachErr: errors.New("detach failed")}
	attached := attachIOHealthHooks(object, func() error { return nil })

	if attached != 0 {
		t.Fatalf("attached event sources = %d, want 0", attached)
	}
	if !reflect.DeepEqual(object.programs, []string{
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_del",
		"kprobe_nvme_sysfs_show_state",
	}) {
		t.Fatalf("programs after detach failure = %v", object.programs)
	}
}

func ioHealthEnumSpec(t *testing.T, enum *btf.Enum) *btf.Spec {
	t.Helper()
	builder, err := btf.NewBuilder([]btf.Type{enum})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := builder.Marshal(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestIOHealthNVMeStatesFromBTF(t *testing.T) {
	// Member values, including vendor backports, determine the exported name.
	for _, test := range []struct {
		name  string
		value uint64
	}{
		{"ADMIN_ONLY", 2},
		{"RESETTING", 2},
		{"DELETING_NOIO", 5},
		{"DELETING_NOIO", 9},
	} {
		t.Run(fmt.Sprintf("%s-%d", test.name, test.value), func(t *testing.T) {
			spec := ioHealthEnumSpec(t, &btf.Enum{
				Name: "nvme_ctrl_state", Size: 4,
				Values: []btf.EnumValue{{Name: "NVME_CTRL_" + test.name, Value: test.value}},
			})
			states := ioHealthNVMeStates(spec)
			want := strings.ToLower(test.name)
			if got := ioHealthNVMeStateName(states, uint32(test.value)); got != want {
				t.Fatalf("state %d = %q, want %q", test.value, got, want)
			}
			if got := ioHealthNVMeStateName(states, 255); got != "unknown" {
				t.Fatalf("unknown state = %q", got)
			}
		})
	}
	spec := ioHealthEnumSpec(t, &btf.Enum{Name: "other_enum", Size: 4})
	if got := ioHealthNVMeStateName(ioHealthNVMeStates(spec), 1); got != "unknown" {
		t.Fatalf("missing NVMe enum = %q", got)
	}
}

func TestIOHealthRequestQuietFlagsFromBTF(t *testing.T) {
	for _, test := range []struct {
		name   string
		enum   *btf.Enum
		wanted uint32
	}{
		{"macro flags", &btf.Enum{Name: "other_enum", Size: 4}, 1 << 11},
		{"enum flags", &btf.Enum{
			Name: "rqf_flags", Size: 4,
			Values: []btf.EnumValue{{Name: "__RQF_QUIET", Value: 7}},
		}, 1 << 7},
		{"renumbered flags", &btf.Enum{
			Name: "rqf_flags", Size: 4,
			Values: []btf.EnumValue{{Name: "__RQF_QUIET", Value: 12}},
		}, 1 << 12},
		{"absent quiet flag", &btf.Enum{Name: "rqf_flags", Size: 4}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ioHealthRequestQuietMask(ioHealthEnumSpec(t, test.enum)); got != test.wanted {
				t.Fatalf("quiet mask = %#x, want %#x", got, test.wanted)
			}
		})
	}
}

func TestIOHealthRetainsUnknownNVMeState(t *testing.T) {
	collector, recorder := newRecordingIOHealthCollector(t, t.TempDir())
	collector.handleKernelEvent(ioHealthPerfEvent{
		Type: ioHealthEventNVMeStateChange, NewStateRaw: 253,
		Controller: ioHealthControllerBytes("nvme0"),
	}, nil)
	if len(recorder.events) != 1 || recorder.events[0].event.NewState != "unknown" ||
		recorder.events[0].event.NewStateRaw == nil || *recorder.events[0].event.NewStateRaw != 253 {
		t.Fatalf("unknown kernel state was not retained: %+v", recorder.events)
	}
}

func TestIOHealthPreservesMDTriggerDuringEvidenceCooldown(t *testing.T) {
	root := t.TempDir()
	dev := writeIOHealthBlockDevice(t, root, "sda", 8, 0)
	collector, recorder := newRecordingIOHealthCollector(t, root)
	worker := &recordingEvidenceSubmitter{accept: true}
	collector.handleKernelEvent(ioHealthPerfEvent{
		Type: ioHealthEventBlockError, Dev: dev, Status: -int32(unix.EIO),
	}, worker)
	worker.accept = false
	at := time.Unix(200, 0)
	collector.handleMDChange(iohealth.MDChange{
		Array: "md0", Member: "sda", Field: iohealth.MDFieldMemberState,
		NewState: "faulty", ObservedAt: at,
	}, worker)
	if len(recorder.events) != 1 || recorder.events[0].at != at ||
		recorder.events[0].event.Type != ioHealthTypeMDMemberState ||
		recorder.events[0].event.NewState != "faulty" {
		t.Fatalf("MD trigger was suppressed with its evidence: %+v", recorder.events)
	}
	collector.handleEvidenceResult(iohealth.EvidenceResult{
		Target: worker.requests[0].Target, TriggeredAt: worker.requests[0].TriggeredAt,
		Event: worker.requests[0].Trigger,
	})
	if len(recorder.events) != 2 || recorder.events[1].event.Type != ioHealthTypeBlockError {
		t.Fatalf("original block trigger was not retained: %+v", recorder.events)
	}
}
