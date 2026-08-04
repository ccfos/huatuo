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

package collector

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"huatuo-bamai/internal/ioobserve/health"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/types"

	"golang.org/x/sys/unix"
)

type recordingEvidenceSubmitter struct {
	requests []health.EvidenceRequest
	accept   bool
}

func (s *recordingEvidenceSubmitter) Submit(request health.EvidenceRequest) bool {
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

	for _, test := range []struct {
		release string
		want    ioHealthNVMeStateLayout
	}{
		{release: "4.18.0-193.6.3.el8_2", want: ioHealthNVMeStateLinux418},
		{release: "5.10.0-216.0.0.115.oe2203sp4", want: ioHealthNVMeStateLinux510},
		{release: "7.2.0-rc6", want: ioHealthNVMeStateLinux510},
		{release: "5.4.0", want: ioHealthNVMeStateUnknown},
		{release: "not-a-version", want: ioHealthNVMeStateUnknown},
	} {
		if got := ioHealthNVMeStateLayoutForRelease(test.release); got != test.want {
			t.Errorf("state layout for %q = %d, want %d", test.release, got, test.want)
		}
	}
	if got := ioHealthNVMeStateName(ioHealthNVMeStateLinux418, 5); got != "deleting" {
		t.Fatalf("Linux 4.18 NVMe state 5 = %q, want deleting", got)
	}
	if got := ioHealthNVMeStateName(ioHealthNVMeStateLinux510, 5); got != "deleting_noio" {
		t.Fatalf("Linux 5.10 NVMe state 5 = %q, want deleting_noio", got)
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
		request.Protocol != health.EvidenceProtocolSCSI ||
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
	collector.handleEvidenceResult(health.EvidenceResult{
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
	submitter := &recordingEvidenceSubmitter{accept: true}

	collector.handleKernelEvent(ioHealthPerfEvent{
		Dev:  dev,
		Type: ioHealthEventNVMeTimeout,
	}, submitter)
	if len(submitter.requests) != 1 ||
		submitter.requests[0].Target != "nvme7" ||
		submitter.requests[0].Protocol != health.EvidenceProtocolNVMe {
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
		request.Protocol != health.EvidenceProtocolSCSI ||
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

	for _, change := range []health.MDChange{
		{
			Array:      "md0",
			Field:      health.MDFieldSyncAction,
			OldState:   "idle",
			NewState:   "recover",
			ObservedAt: observedAt,
		},
		{
			Array:      "md0",
			Field:      health.MDFieldDegraded,
			OldState:   "0",
			NewState:   "1",
			ObservedAt: observedAt,
		},
		{
			Array:      "md0",
			Member:     "sdb",
			Field:      health.MDFieldMemberState,
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
		health.MDMemberStateRemoved,
	} {
		collector.handleMDChange(health.MDChange{
			Array:      "md0",
			Member:     "sdb",
			Field:      health.MDFieldMemberState,
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
			request.Protocol != health.EvidenceProtocolSCSI ||
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
	collector.handleEvidenceResult(health.EvidenceResult{
		Target:      "sda",
		TriggeredAt: time.Unix(1, 2),
		Event:       event,
		Reasons:     []string{health.CollectionReasonParseError},
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
