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
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

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

func TestIOHealthFactoryUsesFilesystemPrefix(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	root := t.TempDir()
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	sysRoot := filepath.Join(root, "sys")
	mdstat := filepath.Join(root, "proc", "mdstat")
	require.NoError(t, os.MkdirAll(filepath.Dir(mdstat), 0o755))
	require.NoError(t, os.WriteFile(mdstat, []byte("unused devices: <none>\n"), 0o600))
	dev := writeIOHealthBlockDevice(
		t,
		sysRoot,
		"sda",
		8,
		0,
	)

	attr, err := newIOHealth()
	require.NoError(t, err)
	collector := attr.TracingData.(*ioHealthCollector)
	require.Equal(t, mdstat, collector.procMDStatPath)
	require.Equal(t, filepath.Join(sysRoot, "block"), collector.sysBlockPath)
	require.Equal(t, newIOHealthResolver(sysRoot), collector.resolver)

	watcher := collector.newMDWatcher(collector.procMDStatPath, collector.sysBlockPath)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	require.NoError(t, watcher.Start(ctx))
	cancel()
	require.NoError(t, watcher.Wait())

	worker := &recordingEvidenceSubmitter{accept: true}
	collector.handleKernelEvent(abi.IOHealthEvent{
		Type: ioHealthEventBlockError, Dev: dev, Status: -5,
	}, worker, nil)
	require.Len(t, worker.requests, 1)
	require.Equal(t, "sda", worker.requests[0].Target)
	require.Equal(t, iohealth.EvidenceProtocolSCSI, worker.requests[0].Protocol)
	require.NotEmpty(t, worker.requests[0].Identity)
	require.True(t, collector.resolver.evidenceTargetCurrent(worker.requests[0]))
}

func TestSaveIOHealthEventUsesObservedTimestamp(t *testing.T) {
	tracing.DisableDocumentWriter()
	t.Cleanup(tracing.DisableDocumentWriter)
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(t.Context()); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	if err := tracing.EnableDocumentWriter(store, document.New("health-region")); err != nil {
		t.Fatal(err)
	}
	documents, unsubscribe := store.Subscribe()
	defer unsubscribe()
	triggeredAt := time.Unix(123, 456)
	event := types.IOHealthEvent{
		Type:   ioHealthTypeBlockError,
		Device: "sda",
		Status: "io_error",
	}
	if err := saveIOHealthEvent(triggeredAt, event); err != nil {
		t.Fatal(err)
	}
	select {
	case saved := <-documents:
		if saved.TracerName != ioHealthName || saved.TracerRunType != types.TracerRunTypeEvent ||
			saved.ObservedTimestamp == nil || !saved.ObservedTimestamp.Equal(triggeredAt) ||
			saved.StartedTimestamp != nil || saved.KernelObservedTimestamp != nil ||
			!reflect.DeepEqual(saved.TracerData, event) {
			t.Fatalf("saved health event = %+v", saved)
		}
	case <-time.After(time.Second):
		t.Fatal("health event was not published through the document writer")
	}
}

func TestIOHealthKernelABIAndLabels(t *testing.T) {
	if abi.IOHealthEventSize != 64 {
		t.Fatalf("IO health event size = %d, want 64", abi.IOHealthEventSize)
	}
	// Keep the perf wire layout stable, including both raw-operation bytes.
	sample := make([]byte, abi.IOHealthEventSize)
	binary.NativeEndian.PutUint64(sample[0:8], ^uint64(0))
	binary.NativeEndian.PutUint32(sample[8:12], 0x12345678)
	binary.NativeEndian.PutUint32(sample[12:16], 0x80000000)
	binary.NativeEndian.PutUint32(sample[16:20], ^uint32(0))
	binary.NativeEndian.PutUint32(sample[20:24], 2)
	binary.NativeEndian.PutUint32(sample[24:28], 3)
	binary.NativeEndian.PutUint32(sample[28:32], 4)
	sample[32], sample[47] = 0x80, 0xff
	binary.NativeEndian.PutUint32(sample[48:52], 5)
	sample[52], sample[53] = ioHealthEventNVMeStateChange, reqOpWrite
	sample[54], sample[55] = 0xaa, 0xbb
	binary.NativeEndian.PutUint64(sample[56:64], 7)
	var decoded abi.IOHealthEvent
	if _, err := binary.Decode(sample, binary.NativeEndian, &decoded); err != nil {
		t.Fatal(err)
	}
	wantEvent := abi.IOHealthEvent{
		Sector:             ^uint64(0),
		Dev:                0x12345678,
		Status:             -1 << 31,
		Host:               ^uint32(0),
		Channel:            2,
		Target:             3,
		Lun:                4,
		Controller:         [ioHealthNVMeControllerNameLength]uint8{0: 0x80, 15: 0xff},
		NewStateRaw:        5,
		Type:               ioHealthEventNVMeStateChange,
		Operation:          reqOpWrite,
		Rwbs:               [2]uint8{0xaa, 0xbb},
		TopologyGeneration: 7,
	}
	if decoded != wantEvent {
		t.Fatalf("decoded IO health event = %+v, want %+v", decoded, wantEvent)
	}
	eventTypes := []uint8{
		ioHealthEventBlockError,
		ioHealthEventSCSITimeout,
		ioHealthEventSCSIDispatchError,
		ioHealthEventNVMeTimeout,
		ioHealthEventNVMeReset,
		ioHealthEventNVMeStateChange,
	}
	if want := []uint8{1, 2, 3, 4, 5, 6}; !reflect.DeepEqual(eventTypes, want) {
		t.Fatalf("kernel event wire values = %v, want %v", eventTypes, want)
	}

	for _, test := range []struct {
		raw  uint8
		rwbs [2]uint8
		want string
	}{
		{raw: reqOpRead, want: "read"},
		{raw: reqOpWrite, want: "write"},
		{raw: reqOpFlush, want: "flush"},
		{raw: reqOpDiscard, want: "discard"},
		{raw: 255, want: "unknown"},
		{raw: 255, rwbs: [2]uint8{'R'}, want: "read"},
		{raw: 255, rwbs: [2]uint8{'W'}, want: "write"},
		{raw: 255, rwbs: [2]uint8{'D'}, want: "discard"},
		{raw: 255, rwbs: [2]uint8{'F'}, want: "flush"},
		{raw: 255, rwbs: [2]uint8{'F', 'W'}, want: "write"},
		{raw: 255, rwbs: [2]uint8{'F', 'R'}, want: "read"},
		{raw: 255, rwbs: [2]uint8{'F', 'D'}, want: "discard"},
		{raw: reqOpRead, rwbs: [2]uint8{'N'}, want: "unknown"},
	} {
		if got := ioHealthOperation(abi.IOHealthEvent{Operation: test.raw, Rwbs: test.rwbs}); got != test.want {
			t.Errorf("ioHealthOperation(%d, %q) = %q, want %q", test.raw, test.rwbs, got, test.want)
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
		{raw: -int32(unix.EOVERFLOW), want: "resource"},
		{raw: -int32(unix.EIO), want: "io_error"},
		{raw: 16, want: "unknown"},
		{raw: 255, want: "unknown"},
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

func ioHealthBTFSpec(t *testing.T, types ...btf.Type) *btf.Spec {
	t.Helper()
	builder, err := btf.NewBuilder(types)
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
		{name: "ADMIN_ONLY", value: 2},
		{name: "RESETTING", value: 2},
		{name: "DELETING_NOIO", value: 5},
		{name: "DELETING_NOIO", value: 9},
	} {
		t.Run(fmt.Sprintf("%s-%d", test.name, test.value), func(t *testing.T) {
			spec := ioHealthBTFSpec(t, &btf.Enum{
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
	spec := ioHealthBTFSpec(t, &btf.Enum{Name: "other_enum", Size: 4})
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
		{name: "macro flags", enum: &btf.Enum{Name: "other_enum", Size: 4}, wanted: 1 << 11},
		{
			name: "enum flags", wanted: 1 << 7,
			enum: &btf.Enum{
				Name: "rqf_flags", Size: 4,
				Values: []btf.EnumValue{{Name: "__RQF_QUIET", Value: 7}},
			},
		},
		{
			name: "renumbered flags", wanted: 1 << 12,
			enum: &btf.Enum{
				Name: "rqf_flags", Size: 4,
				Values: []btf.EnumValue{{Name: "__RQF_QUIET", Value: 12}},
			},
		},
		{name: "absent quiet flag", enum: &btf.Enum{Name: "rqf_flags", Size: 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ioHealthRequestQuietMask(ioHealthBTFSpec(t, test.enum)); got != test.wanted {
				t.Fatalf("quiet mask = %#x, want %#x", got, test.wanted)
			}
		})
	}
}

func TestIOHealthBlockErrorLayout(t *testing.T) {
	integer := &btf.Int{Name: "int", Size: 4, Encoding: btf.Signed}
	name := &btf.Pointer{Target: &btf.Const{Type: &btf.Int{Name: "char", Size: 1}}}
	array := func(tag bool, count uint32) *btf.Array {
		entry := &btf.Struct{Size: 16, Members: []btf.Member{
			{Name: "errno", Type: integer}, {Name: "name", Type: name, Offset: 64},
		}}
		if tag {
			entry.Size = 24
			entry.Members[1].Name = "tag"
			entry.Members = append(entry.Members, btf.Member{Name: "name", Type: name, Offset: 128})
		}
		return &btf.Array{Index: integer, Type: &btf.Const{Type: entry}, Nelems: count}
	}
	for _, test := range []struct {
		name  string
		tag   bool
		count uint32
		size  uint32
	}{
		{name: "errno-name", count: 18, size: 16},
		{name: "errno-tag-name", tag: true, count: 20, size: 24},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Kernel BTF need not retain a VAR named blk_errors.
			stride, count, err := ioHealthBlockErrorLayout(ioHealthBTFSpec(t, array(test.tag, test.count)))
			require.NoError(t, err)
			require.Equal(t, test.size, stride)
			require.Equal(t, test.count, count)
		})
	}
	for _, test := range []struct {
		name  string
		types []btf.Type
	}{
		{name: "missing", types: []btf.Type{integer}},
		{name: "short", types: []btf.Type{array(false, 3)}},
		{name: "too large", types: []btf.Type{array(false, 257)}},
		{name: "ambiguous size", types: []btf.Type{array(false, 18), array(true, 18)}},
		{name: "ambiguous length", types: []btf.Type{array(false, 18), array(false, 20)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := ioHealthBlockErrorLayout(ioHealthBTFSpec(t, test.types...))
			require.Error(t, err)
		})
	}
	for _, mutate := range []func(*btf.Struct){
		func(e *btf.Struct) { e.Members[0].Offset = 32 },
		func(e *btf.Struct) { e.Members[0].Type = &btf.Int{Size: 4} },
		func(e *btf.Struct) { e.Members[0].BitfieldSize = 1 },
		func(e *btf.Struct) { e.Members[1].Name = "other" },
		func(e *btf.Struct) { e.Members[1].Type = integer },
	} {
		candidate := array(false, 18)
		mutate(btf.UnderlyingType(candidate.Type).(*btf.Struct))
		_, _, err := ioHealthBlockErrorLayout(ioHealthBTFSpec(t, candidate))
		require.Error(t, err)
	}
}

func TestIOHealthRoutesBlockStatusCompletion(t *testing.T) {
	root := t.TempDir()
	dev := writeIOHealthBlockDevice(t, root, "sda", 8, 0)
	collector, recorder := newRecordingIOHealthCollector(t, root)

	collector.handleKernelEvent(abi.IOHealthEvent{
		Dev:       dev,
		Status:    -int32(unix.ETIMEDOUT),
		Type:      ioHealthEventBlockError,
		Operation: reqOpWrite,
	}, nil, nil)

	if len(recorder.events) != 1 {
		t.Fatalf("saved block-status events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0].event
	if event.Type != ioHealthTypeBlockError ||
		event.Device != "sda" ||
		event.Operation != "write" ||
		event.Status != "timeout" {
		t.Fatalf("block-status event = %+v", event)
	}
}

func TestIOHealthPersistsSuppressedEvidenceTriggers(t *testing.T) {
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
	raw := abi.IOHealthEvent{
		Dev:       dev,
		Status:    -int32(unix.ETIMEDOUT),
		Type:      ioHealthEventBlockError,
		Operation: reqOpRead,
	}

	collector.handleKernelEvent(raw, submitter, nil)
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
	collector.handleKernelEvent(raw, submitter, nil)
	if len(recorder.events) != 1 || recorder.events[0].at != triggeredAt[1] {
		t.Fatalf("suppressed trigger was not retained: %+v", recorder.events)
	}
	collector.handleEvidenceResult(iohealth.EvidenceResult{
		Target:      submitter.requests[0].Target,
		TriggeredAt: submitter.requests[0].TriggeredAt,
		Event:       submitter.requests[0].Trigger,
	})
	if len(recorder.events) != 2 {
		t.Fatalf("two triggers produced %d records, want 2", len(recorder.events))
	}
	if recorder.events[1].at != triggeredAt[0] {
		t.Fatalf("evidence trigger time = %v, want %v", recorder.events[1].at, triggeredAt[0])
	}
	if collector.persistenceFailures != 0 {
		t.Fatalf("successful persistence was reported as failure: %d", collector.persistenceFailures)
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
	if err := os.MkdirAll(
		filepath.Join(root, "class", "nvme", "nvme7"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	dev := writeIOHealthBlockDevice(t, root, "nvme0c7n1", 259, 0)
	collector, recorder := newRecordingIOHealthCollector(t, root)
	collector.nvmeStates = map[uint32]string{1: "live"}
	submitter := &recordingEvidenceSubmitter{accept: true}

	collector.handleKernelEvent(abi.IOHealthEvent{
		Dev:  dev,
		Type: ioHealthEventNVMeTimeout,
	}, submitter, nil)
	if len(submitter.requests) != 1 ||
		submitter.requests[0].Target != "nvme7" ||
		submitter.requests[0].Protocol != iohealth.EvidenceProtocolNVMe {
		t.Fatalf("NVMe timeout requests = %+v", submitter.requests)
	}

	for index, test := range []struct {
		raw      abi.IOHealthEvent
		typeName string
		device   string
		state    string
	}{
		{raw: abi.IOHealthEvent{Type: ioHealthEventNVMeTimeout}, typeName: ioHealthTypeNVMeTimeout, device: "unknown"},
		{
			raw:      abi.IOHealthEvent{Type: ioHealthEventNVMeReset, Controller: ioHealthControllerBytes("nvme7")},
			typeName: ioHealthTypeNVMeReset, device: "nvme7",
		},
		{raw: abi.IOHealthEvent{Type: ioHealthEventNVMeReset}, typeName: ioHealthTypeNVMeReset, device: "unknown"},
		{
			raw: abi.IOHealthEvent{
				Type: ioHealthEventNVMeStateChange, Controller: ioHealthControllerBytes("nvme7"), NewStateRaw: 1,
			},
			typeName: ioHealthTypeNVMeStateChange, device: "nvme7", state: "live",
		},
		{
			raw: abi.IOHealthEvent{
				Type: ioHealthEventNVMeStateChange, Controller: ioHealthControllerBytes("nvme0"), NewStateRaw: 253,
			},
			typeName: ioHealthTypeNVMeStateChange, device: "nvme0", state: "unknown",
		},
	} {
		collector.handleKernelEvent(test.raw, submitter, nil)
		if len(submitter.requests) != 1 || len(recorder.events) != index+1 {
			t.Fatalf(
				"direct NVMe event %d: requests=%+v events=%+v",
				index,
				submitter.requests,
				recorder.events,
			)
		}
		got := recorder.events[index].event
		if got.Type != test.typeName || got.Device != test.device {
			t.Fatalf(
				"NVMe event %d = %+v, want %+v",
				index,
				got,
				test,
			)
		}
		if test.state != "" && (got.NewState != test.state ||
			got.NewStateRaw == nil || *got.NewStateRaw != test.raw.NewStateRaw) {
			t.Fatalf(
				"NVMe state event = %+v, want state %s/%d",
				got,
				test.state,
				test.raw.NewStateRaw,
			)
		}
	}
	if got := collector.counters[ioHealthCounterKey{
		kind:   ioHealthCounterNVMeReset,
		device: "nvme7",
	}]; got != 1 {
		t.Fatalf("NVMe reset counter = %d, want 1", got)
	}
}

func TestIOHealthPreservesMDTriggerDuringEvidenceCooldown(t *testing.T) {
	root := t.TempDir()
	dev := writeIOHealthBlockDevice(t, root, "sda", 8, 0)
	collector, recorder := newRecordingIOHealthCollector(t, root)
	worker := &recordingEvidenceSubmitter{accept: true}
	collector.handleKernelEvent(abi.IOHealthEvent{
		Type: ioHealthEventBlockError, Dev: dev, Status: -int32(unix.EIO),
	}, worker, nil)
	worker.accept = false
	at := time.Unix(200, 0)
	collector.handleMDChange(iohealth.MDChange{
		Array: "md0", Member: "sda", Field: iohealth.MDFieldMemberState,
		NewState: "faulty", ObservedAt: at,
		MemberIdentity: iohealth.BlockDeviceIdentity(filepath.Join(root, "class", "block", "sda")),
	}, worker)
	if len(worker.requests) != 2 || worker.requests[1].Trigger.Type != ioHealthTypeMDMemberState {
		t.Fatalf("MD evidence did not reach cooldown: %+v", worker.requests)
	}
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

func TestIOHealthRoutesFullSCSIHCTL(t *testing.T) {
	const (
		host    = uint32(70000)
		channel = uint32(80000)
		target  = uint32(90000)
	)
	for _, test := range []struct {
		name    string
		lun     uint32
		devices map[uint64]string
		want    string
	}{
		{name: "32-bit LUN", lun: 100000, devices: map[uint64]string{100000: "sdz"}, want: "sdz"},
		{name: "unique 64-bit LUN", lun: 17, devices: map[uint64]string{1<<32 + 17: "sdz"}, want: "sdz"},
		{name: "aliased LUNs", lun: 17, devices: map[uint64]string{17: "sdz", 1<<32 + 17: "sdy"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for lun, device := range test.devices {
				path := filepath.Join(root, "class", "scsi_device",
					fmt.Sprintf("%d:%d:%d:%d", host, channel, target, lun), "device", "block", device)
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			collector, _ := newRecordingIOHealthCollector(t, root)
			submitter := &recordingEvidenceSubmitter{accept: true}
			collector.handleKernelEvent(abi.IOHealthEvent{
				Type: ioHealthEventSCSIDispatchError, Status: scsiMLQueueTargetBusy,
				Host: host, Channel: channel, Target: target, Lun: test.lun,
			}, submitter, nil)
			if len(submitter.requests) != 1 {
				t.Fatalf("SCSI requests = %+v", submitter.requests)
			}
			request := submitter.requests[0]
			if request.Target != test.want || request.Trigger.Status != "target_busy" {
				t.Fatalf("SCSI request = %+v", request)
			}
			if test.want == "" {
				if request.Trigger.Device != "unknown" || request.Reason != iohealth.CollectionReasonTargetUnresolved {
					t.Fatalf("ambiguous SCSI identity was attributed: %+v", request)
				}
			} else if request.Protocol != iohealth.EvidenceProtocolSCSI || request.Trigger.Device != test.want {
				t.Fatalf("resolved SCSI request = %+v", request)
			}
		})
	}
}

func TestIOHealthRoutesMDTransitionsAndFaultEvidence(t *testing.T) {
	root := t.TempDir()
	writeIOHealthBlockDevice(t, root, "sdb", 8, 16)
	blockPath := filepath.Join(root, "class", "block", "sdb")
	identity := iohealth.BlockDeviceIdentity(blockPath)
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
			Array:          "md0",
			Member:         "sdb",
			MemberIdentity: identity,
			Field:          iohealth.MDFieldMemberState,
			OldState:       "in_sync",
			NewState:       state,
			ObservedAt:     observedAt,
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
	// A queued event belongs to the observed member, not a later same-name disk.
	if err := os.Rename(blockPath, blockPath+"-retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(blockPath, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, observed := range []string{"", identity} {
		collector.handleMDChange(iohealth.MDChange{
			Array: "md0", Member: "sdb", MemberIdentity: observed,
			Field: iohealth.MDFieldMemberState, OldState: "in_sync",
			NewState: iohealth.MDMemberStateRemoved, ObservedAt: observedAt,
		}, submitter)
	}
	if len(submitter.requests) != 4 || len(recorder.events) != 5 {
		t.Fatalf("unconfirmed member records=%+v requests=%+v", recorder.events, submitter.requests)
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
		target.protocol != iohealth.EvidenceProtocolSCSI ||
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
	if err := os.MkdirAll(
		filepath.Join(root, "class", "nvme", "nvme1"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("nvme0c1n1")
	if target.target != "nvme1" ||
		target.protocol != iohealth.EvidenceProtocolNVMe ||
		target.reason != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverUsesNVMeControllerGenerationIdentity(t *testing.T) {
	root := t.TempDir()
	controllerOne := filepath.Join(root, "devices", "pci0000:00", "nvme", "nvme0")
	controllerTwo := filepath.Join(root, "devices", "pci0000:01", "nvme", "nvme0")
	for _, controller := range []string{controllerOne, controllerTwo} {
		if err := os.MkdirAll(controller, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	classNVMe := filepath.Join(root, "class", "nvme")
	if err := os.MkdirAll(classNVMe, 0o755); err != nil {
		t.Fatal(err)
	}
	controllerLink := filepath.Join(classNVMe, "nvme0")
	if err := os.Symlink(controllerOne, controllerLink); err != nil {
		t.Fatal(err)
	}

	classBlock := filepath.Join(root, "class", "block")
	for _, namespace := range []string{"nvme0n1", "nvme0n2"} {
		namespacePath := filepath.Join(controllerOne, namespace)
		if err := os.MkdirAll(namespacePath, 0o755); err != nil {
			t.Fatal(err)
		}
		classNamespace := filepath.Join(classBlock, namespace)
		if err := os.MkdirAll(filepath.Join(classNamespace, "slaves"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			namespacePath,
			filepath.Join(classNamespace, "device"),
		); err != nil {
			t.Fatal(err)
		}
	}

	resolver := newIOHealthResolver(root)
	first := resolver.resolveBlockName("nvme0n1")
	second := resolver.resolveBlockName("nvme0n2")
	if first.reason != "" || first.identity == "" ||
		first.target != "nvme0" || first.identity != second.identity {
		t.Fatalf("controller identities = %+v, %+v", first, second)
	}
	request := iohealth.EvidenceRequest{
		Trigger:  types.IOHealthEvent{Device: "nvme0n1"},
		Target:   first.target,
		Identity: first.identity,
	}
	if !resolver.evidenceTargetCurrent(request) {
		t.Fatal("initial NVMe controller identity was not current")
	}
	if err := os.Remove(controllerLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(controllerTwo, controllerLink); err != nil {
		t.Fatal(err)
	}
	if resolver.evidenceTargetCurrent(request) {
		t.Fatal("replacement NVMe controller inherited namespace identity")
	}
}

func TestIOHealthResolverUsesSingleNVMeSubsystemController(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("controllers=%d", count), func(t *testing.T) {
			root := t.TempDir()
			subsystem := filepath.Join(root, "devices", "virtual", "nvme-subsystem", "nvme-subsys7")
			namespace := filepath.Join(subsystem, "nvme7n3")
			require.NoError(t, os.MkdirAll(namespace, 0o755))
			require.NoError(t, os.Symlink(subsystem, filepath.Join(namespace, "device")))
			classBlock := filepath.Join(root, "class", "block")
			classNVMe := filepath.Join(root, "class", "nvme")
			require.NoError(t, os.MkdirAll(classBlock, 0o755))
			require.NoError(t, os.MkdirAll(classNVMe, 0o755))
			require.NoError(t, os.Symlink(namespace, filepath.Join(classBlock, "nvme7n3")))
			partition := filepath.Join(namespace, "nvme7n3p1")
			require.NoError(t, os.MkdirAll(partition, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(partition, "partition"), []byte("1\n"), 0o600))
			require.NoError(t, os.Symlink(partition, filepath.Join(classBlock, "nvme7n3p1")))
			for index := range count {
				name := fmt.Sprintf("nvme%d", index+2)
				controller := filepath.Join(root, "devices", "virtual", "nvme-fabrics", "ctl", name)
				require.NoError(t, os.MkdirAll(controller, 0o755))
				require.NoError(t, os.Symlink(controller, filepath.Join(subsystem, name)))
				require.NoError(t, os.Symlink(controller, filepath.Join(classNVMe, name)))
			}
			resolver := newIOHealthResolver(root)
			for _, device := range []string{"nvme7n3", "nvme7n3p1"} {
				target := resolver.resolveBlockName(device)
				if count != 1 {
					require.Equal(t, iohealth.CollectionReasonTargetUnsupported, target.reason)
					require.Empty(t, target.target)
					continue
				}
				require.Empty(t, target.reason)
				require.Equal(t, "nvme2", target.target)
				require.Equal(t, iohealth.EvidenceProtocolNVMe, target.protocol)
				require.NotEmpty(t, target.identity)
			}
		})
	}
}

func TestIOHealthResolverTreatsMissingDeviceAsUnresolved(t *testing.T) {
	target := newIOHealthResolver(t.TempDir()).resolveBlockName("gone")
	if target.reason != iohealth.CollectionReasonTargetUnresolved {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverRejectsReusedEvidenceTarget(t *testing.T) {
	root := t.TempDir()
	devicePath := filepath.Join(root, "class", "block", "sda")
	if err := os.MkdirAll(filepath.Join(devicePath, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devicePath, "dev"), []byte("8:0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diskseqPath := filepath.Join(devicePath, "diskseq")
	if err := os.WriteFile(diskseqPath, []byte("41\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolver := newIOHealthResolver(root)
	target := resolver.resolveBlockName("sda")
	request := iohealth.EvidenceRequest{
		Trigger:  types.IOHealthEvent{Device: "sda"},
		Target:   target.target,
		Identity: target.identity,
	}
	if target.identity == "" || !resolver.evidenceTargetCurrent(request) {
		t.Fatalf("initial evidence target = %+v", target)
	}
	if err := os.WriteFile(diskseqPath, []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resolver.evidenceTargetCurrent(request) {
		t.Fatal("reused device identity remained valid")
	}
}

// Collector lifecycle and persistence boundaries.

type fakeIOHealthMDWatcher struct {
	startErr error
	changes  chan iohealth.MDChange
	stop     chan error
	ctx      context.Context
}

type fakeIOHealthPerfReader struct {
	ctx      context.Context
	events   <-chan abi.IOHealthEvent
	readInto func(context.Context, any) error
}

func (r *fakeIOHealthPerfReader) ReadInto(value any) error {
	if r.readInto != nil {
		return r.readInto(r.ctx, value)
	}
	select {
	case event := <-r.events:
		*value.(*abi.IOHealthEvent) = event
		return nil
	case <-r.ctx.Done():
		return types.ErrExitByCancelCtx
	}
}

func (r *fakeIOHealthPerfReader) ReadBatch(func() any) (bpf.PerfEventBatch, error) {
	return bpf.PerfEventBatch{}, errors.New("unexpected batch read")
}

func (r *fakeIOHealthPerfReader) Close() error {
	return nil
}

type fakeIOHealthRuntimeBPF struct {
	bpf.BPF
	attachErrors       map[string]error
	attachFunc         func(string) error
	closeFunc          func() error
	events             <-chan abi.IOHealthEvent
	readInto           func(context.Context, any) error
	freezeItems        []bpf.MapItem
	topologyGeneration uint64
	topologyError      error
}

func (b *fakeIOHealthRuntimeBPF) MapIDByName(name string) uint32 {
	if name == "io_health_topology" {
		return 1
	}
	if name == "io_health_freeze_counts" {
		return 2
	}
	return 0
}

func (b *fakeIOHealthRuntimeBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	return binary.LittleEndian.AppendUint64(nil, b.topologyGeneration), b.topologyError
}

func TestIOHealthBufferedEventEvidence(t *testing.T) {
	for _, test := range []struct {
		name            string
		generation      uint64
		eventGeneration uint64
		mapError        error
		wantEvidence    bool
	}{
		{name: "same topology", wantEvidence: true},
		{name: "disk replaced before consumption", generation: 1},
		{name: "new disk event", generation: 1, eventGeneration: 1, wantEvidence: true},
		{name: "topology cannot be read", mapError: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dev := writeIOHealthBlockDevice(t, root, "sda", 8, 0)
			collector, recorder := newRecordingIOHealthCollector(t, root)
			worker := &recordingEvidenceSubmitter{accept: true}
			reads := 0
			object := &fakeIOHealthRuntimeBPF{
				topologyGeneration: test.generation,
				topologyError:      test.mapError,
				readInto: func(_ context.Context, value any) error {
					reads++
					if reads != 1 {
						return errors.New("event stream drained")
					}
					if test.generation != 0 {
						block := filepath.Join(root, "class", "block", "sda")
						if err := os.Rename(block, block+".old"); err != nil {
							t.Fatal(err)
						}
						if err := os.MkdirAll(filepath.Join(block, "slaves"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					*value.(*abi.IOHealthEvent) = abi.IOHealthEvent{
						Type: ioHealthEventBlockError, Dev: dev, Status: -int32(unix.EIO),
						TopologyGeneration: test.eventGeneration,
					}
					return nil
				},
			}
			_, _, err := collector.runBPFSession(t.Context(), worker,
				func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
					return object, ioHealthTestHookOptions(t), nil
				})
			if err == nil || !strings.Contains(err.Error(), "event stream drained") {
				t.Fatalf("session result = %v, want drained stream", err)
			}
			if test.wantEvidence {
				if len(worker.requests) != 1 || len(recorder.events) != 0 {
					t.Fatalf("current event: evidence=%d saved=%d", len(worker.requests), len(recorder.events))
				}
			} else if len(worker.requests) != 0 || len(recorder.events) != 1 {
				t.Fatalf("untrusted event: evidence=%d saved=%d", len(worker.requests), len(recorder.events))
			} else if recorder.events[0].event.Device != "unknown" {
				t.Fatalf("untrusted event inherited device %q", recorder.events[0].event.Device)
			}
		})
	}
}

func (b *fakeIOHealthRuntimeBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name != "io_health_freeze_counts" {
		return nil, fmt.Errorf("unexpected IO health counter map %q", name)
	}
	return slices.Clone(b.freezeItems), nil
}

func (b *fakeIOHealthRuntimeBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	if mapID != 2 {
		return fmt.Errorf("unexpected IO health counter map ID %d", mapID)
	}
	for _, key := range keys {
		b.freezeItems = slices.DeleteFunc(b.freezeItems, func(item bpf.MapItem) bool {
			return bytes.Equal(item.Key, key)
		})
	}
	return nil
}

func TestIOHealthFreezeCounter(t *testing.T) {
	root := t.TempDir()
	writeIOHealthBlockDevice(t, root, "sda", 8, 0)
	collector := newIOHealthCollector(root, "")
	object := &fakeIOHealthRuntimeBPF{}
	collector.freezeObject = object
	key := make([]byte, 8)
	binary.LittleEndian.PutUint64(key, uint64(8)<<32)
	for _, total := range []uint64{0, 7, 7, 9, 0, 1} {
		value := make([]byte, 8)
		binary.LittleEndian.PutUint64(value, total)
		object.freezeItems = []bpf.MapItem{{Key: key, Value: value}}
		metrics, err := collector.Update()
		if err != nil || len(metrics) != 1 {
			t.Fatalf("freeze collection = (%v, %v), want one counter", metrics, err)
		}
		want := metric.NewCounterData("blkdisk_freeze", float64(total),
			"the disk freeze event count", map[string]string{"disk": "sda"})
		if !reflect.DeepEqual(metrics[0], want) {
			t.Fatalf("freeze counter = %+v, want %+v", metrics[0], want)
		}
	}
	writeIOHealthBlockDevice(t, root, "sdb", 8, 16)
	survivor := bpf.MapItem{
		Key:   binary.LittleEndian.AppendUint64(nil, uint64(8)<<32|16),
		Value: binary.LittleEndian.AppendUint64(nil, 11),
	}
	object.freezeItems = append(object.freezeItems, survivor)
	require.NoError(t, os.Remove(filepath.Join(root, "dev", "block", "8:0")))
	for range 2 {
		metrics, err := collector.Update()
		require.NoError(t, err)
		require.Len(t, metrics, 1)
		require.Equal(t, "sdb", metrics[0].Labels()["disk"])
		require.Equal(t, float64(11), metrics[0].Value)
		require.Equal(t, []bpf.MapItem{survivor}, object.freezeItems)
		// A callback that outlives del_gendisk may leave another stale row.
		object.freezeItems = append(object.freezeItems, bpf.MapItem{
			Key: key, Value: binary.LittleEndian.AppendUint64(nil, 1),
		})
	}
}

func (b *fakeIOHealthRuntimeBPF) AttachWithOptions(options []bpf.AttachOption) error {
	for _, option := range options {
		if b.attachFunc != nil {
			if err := b.attachFunc(option.ProgramName); err != nil {
				return err
			}
		} else if err := b.attachErrors[option.ProgramName]; err != nil {
			return err
		}
	}
	return nil
}

func (b *fakeIOHealthRuntimeBPF) Detach() error {
	return nil
}

func (b *fakeIOHealthRuntimeBPF) EventPipeByName(
	ctx context.Context,
	_ string,
	_ uint32,
) (bpf.PerfEventReader, error) {
	return &fakeIOHealthPerfReader{ctx: ctx, events: b.events, readInto: b.readInto}, nil
}

func (b *fakeIOHealthRuntimeBPF) Close() error {
	if b.closeFunc != nil {
		return b.closeFunc()
	}
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
	collector.saveEvent = func(
		_ time.Time,
		event types.IOHealthEvent,
	) error {
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
	collector.saveEvent = func(
		_ time.Time,
		event types.IOHealthEvent,
	) error {
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
	collector.saveEvent = func(
		_ time.Time,
		event types.IOHealthEvent,
	) error {
		saved <- event
		return nil
	}
	bpfAttempts := make(chan int, 2)
	releaseFirstLoad := make(chan struct{})
	attempt := 0
	loadBPF := func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
		attempt++
		bpfAttempts <- attempt
		if attempt == 1 {
			<-releaseFirstLoad
			return nil, nil, errors.New("BPF unavailable")
		}
		return &fakeIOHealthRuntimeBPF{}, ioHealthTestHookOptions(t), nil
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
	loadBPF := func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
		close(loadStarted)
		<-releaseLoad
		return nil, nil, errors.New("BPF unavailable")
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

func TestIOHealthBPFSessionContinuesAfterPerfSampleLoss(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}
	reads := 0
	object := &fakeIOHealthRuntimeBPF{readInto: func(ctx context.Context, value any) error {
		reads++
		switch reads {
		case 1:
			return &bpf.PerfEventSamplesLostError{Count: 3}
		case 2:
			*value.(*abi.IOHealthEvent) = abi.IOHealthEvent{
				Type:       ioHealthEventNVMeReset,
				Controller: ioHealthControllerBytes("nvme7"),
			}
			return nil
		default:
			<-ctx.Done()
			return types.ErrExitByCancelCtx
		}
	}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, retryable, err := collector.runBPFSession(
			ctx,
			nil,
			func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
				return object, ioHealthTestHookOptions(t), nil
			},
		)
		if retryable {
			t.Error("sample loss made the BPF session retryable")
		}
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("BPF session did not stop during cleanup")
		}
	})
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeNVMeReset || event.Device != "nvme7" {
			t.Fatalf("event after sample loss = %+v", event)
		}
	case err := <-done:
		t.Fatalf("BPF session stopped before the event after sample loss: %v", err)
	case <-time.After(time.Second):
		t.Fatal("sample loss prevented the next health event")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BPF session cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BPF session did not stop after cancellation")
	}
	metrics, err := collector.Update()
	if err != nil || len(metrics) != 1 || metrics[0].Value != 1 {
		t.Fatalf("metrics after sample loss = (%+v, %v), want one counted event", metrics, err)
	}
}

func TestIOHealthBPFSessionAttachFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "missing", err: unix.ENOENT},
		{name: "permission", err: unix.EPERM},
		{name: "transient", err: unix.EIO, retryable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			attempts := make(map[string]int)
			object := &fakeIOHealthRuntimeBPF{
				attachFunc: func(program string) error {
					attempts[program]++
					if program == "kprobe_nvme_timeout" {
						return test.err
					}
					return nil
				},
				readInto: func(context.Context, any) error {
					cancel()
					return types.ErrExitByCancelCtx
				},
			}
			attached, retryable, err := collector.runBPFSession(ctx,
				&recordingEvidenceSubmitter{},
				func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
					return object, ioHealthTestHookOptions(t), nil
				},
			)
			require.ErrorIs(t, err, test.err)
			require.Zero(t, attached)
			require.Equal(t, test.retryable, retryable)
			if got := attempts["kprobe_nvme_timeout"]; got != 1 {
				t.Fatalf("timeout hook attempts = %d, want 1", got)
			}
			if attempts["trace_block_rq_error"] != 1 {
				t.Fatalf("whole-session attempts = %v", attempts)
			}
		})
	}
}

func TestIOHealthBPFRecoversAfterSessionFailure(t *testing.T) {
	for _, test := range []struct {
		name          string
		missingSource bool
		attachFailure bool
		readErr       error
		readFailures  int
	}{
		{name: "read failure"},
		{name: "optional source absent", missingSource: true},
		{name: "attach failure", attachFailure: true},
		{name: "read ENOENT is not an unavailable hook", readErr: unix.ENOENT},
		{name: "read EINVAL is not an unavailable hook", readErr: unix.EINVAL},
		{name: "independent reader recoveries", readFailures: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
			watcher := newFakeIOHealthMDWatcher()
			mdStarts := 0
			collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
				mdStarts++
				return watcher
			}
			saved := make(chan types.IOHealthEvent, 2)
			collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
				saved <- event
				return nil
			}

			readErr := test.readErr
			if readErr == nil {
				readErr = unix.EIO
			}
			failures := max(test.readFailures, 1)
			closed := 0
			first := &fakeIOHealthRuntimeBPF{
				readInto: func(context.Context, any) error { return readErr },
				closeFunc: func() error {
					closed++
					return nil
				},
			}
			if test.attachFailure {
				first.attachErrors = map[string]error{"kprobe_nvme_timeout": unix.EIO}
			}
			options := ioHealthTestHookOptions(t)
			if test.missingSource {
				options = slices.DeleteFunc(options, func(option bpf.AttachOption) bool {
					return option.ProgramName == "kprobe_nvme_timeout"
				})
			}
			events := make(chan abi.IOHealthEvent, 1)
			attempts := make(chan int, failures+1)
			loads := 0
			loadBPF := func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
				loads++
				attempts <- loads
				if closed != loads-1 {
					t.Error("previous BPF object did not close before recovery")
				}
				if loads == 1 {
					return first, options, nil
				}
				object := &fakeIOHealthRuntimeBPF{
					events: events,
					closeFunc: func() error {
						closed++
						return nil
					},
				}
				if loads <= failures {
					object.readInto = func(context.Context, any) error { return readErr }
				}
				return object, options, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- collector.start(ctx, loadBPF, time.Millisecond) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("collector stop error = %v", err)
					}
				case <-time.After(time.Second):
					t.Error("collector did not stop after cancellation")
				}
			})

			for attempt := 1; attempt <= failures+1; attempt++ {
				waitIOHealthAttempt(t, attempts, attempt)
			}
			watcher.changes <- iohealth.MDChange{
				Array: "md0", Field: iohealth.MDFieldSyncAction,
				OldState: "idle", NewState: "recover", ObservedAt: time.Unix(1, 0),
			}
			select {
			case event := <-saved:
				if event.Type != ioHealthTypeMDSyncAction {
					t.Fatalf("event after BPF recovery = %+v", event)
				}
			case <-time.After(time.Second):
				t.Fatal("MD monitoring stopped during BPF recovery")
			}
			events <- abi.IOHealthEvent{Type: ioHealthEventNVMeReset}
			select {
			case event := <-saved:
				if event.Type != ioHealthTypeNVMeReset {
					t.Fatalf("event after BPF recovery = %+v", event)
				}
			case <-time.After(time.Second):
				t.Fatal("kernel events did not resume after cleanup recovered")
			}
			if mdStarts != 1 {
				t.Fatalf("MD watcher starts = %d, want 1", mdStarts)
			}
		})
	}
}

func TestIOHealthBPFSessionPreservesCloseFailure(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	readErr := errors.New("event read failed")
	closeErr := errors.New("close link failed")
	// defaultBPF marks itself closed even when a resource close reports an error.
	object := &fakeIOHealthRuntimeBPF{
		readInto:  func(context.Context, any) error { return readErr },
		closeFunc: func() error { return closeErr },
	}
	_, retryable, err := collector.runBPFSession(t.Context(), nil,
		func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
			return object, ioHealthTestHookOptions(t), nil
		})
	if retryable || !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
		t.Fatalf("session result = (%t, %v), want both errors without retry", retryable, err)
	}
}

func TestIOHealthCancellationPreservesCloseFailure(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		return newFakeIOHealthMDWatcher()
	}
	closeErr := errors.New("close link failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	object := &fakeIOHealthRuntimeBPF{
		readInto: func(context.Context, any) error {
			cancel()
			return types.ErrExitByCancelCtx
		},
		closeFunc: func() error {
			return closeErr
		},
	}
	err := collector.start(ctx,
		func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
			return object, ioHealthTestHookOptions(t), nil
		}, time.Hour)
	if !errors.Is(err, closeErr) {
		t.Fatalf("collector stop error = %v, want close error", err)
	}
}

func TestIOHealthStartWaitsForSaveBeforeExit(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	watcher := newFakeIOHealthMDWatcher()
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher { return watcher }
	saveStarted := make(chan struct{})
	releaseSave := make(chan struct{})
	var releaseOnce sync.Once
	collector.saveEvent = func(_ time.Time, _ types.IOHealthEvent) error {
		close(saveStarted)
		<-releaseSave
		return nil
	}
	events := make(chan abi.IOHealthEvent, 1)
	events <- abi.IOHealthEvent{
		Type:       ioHealthEventNVMeReset,
		Controller: ioHealthControllerBytes("nvme7"),
	}
	object := &fakeIOHealthRuntimeBPF{events: events}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- collector.start(ctx, func(string, map[string]any) (bpf.BPF, []bpf.AttachOption, error) {
			return object, ioHealthTestHookOptions(t), nil
		}, time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(releaseSave) })
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("collector did not finish after releasing save")
		}
	})
	select {
	case <-saveStarted:
	case <-time.After(time.Second):
		t.Fatal("health event did not reach persistence")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("collector exited while save was active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(releaseSave) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("collector exit error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not exit after save completed")
	}
}

func TestIOHealthSaveErrorsDoNotStopEventProcessing(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	saves := 0
	collector.saveEvent = func(time.Time, types.IOHealthEvent) error {
		saves++
		return errors.New("storage unavailable")
	}
	for range 2 {
		collector.handleKernelEvent(abi.IOHealthEvent{
			Type:       ioHealthEventNVMeReset,
			Controller: ioHealthControllerBytes("nvme7"),
		}, nil, nil)
	}
	if saves != 2 {
		t.Fatalf("save attempts = %d, want 2", saves)
	}
	if got := collector.counters[ioHealthCounterKey{
		kind: ioHealthCounterNVMeReset, device: "nvme7",
	}]; got != 2 {
		t.Fatalf("reset events = %d, want 2", got)
	}
	if got := collector.persistenceFailures; got != 2 {
		t.Fatalf("save failures = %d, want 2", got)
	}
	metrics, err := collector.Update()
	if err != nil || len(metrics) != 2 {
		t.Fatalf("event/error metrics = (%+v, %v), want two counters", metrics, err)
	}
	for _, sample := range metrics {
		if sample.Value != 2 {
			t.Fatalf("event/error counter = %v, want 2", sample.Value)
		}
	}
}

// Startup capability selection and whole-session attachment.

func ioHealthTestSymbols() map[string]symbol.KsymbolRange {
	names := []string{
		"del_gendisk", "device_add", "blk_register_queue",
		"cdev_device_add", "cdev_device_del", "nvme_sysfs_show_state",
		"nvme_change_ctrl_state", "nvme_reset_ctrl", "nvme_timeout",
		"blk_mq_freeze_queue", "blk_mq_freeze_queue_nomemsave",
		"__blk_freeze_queue_start", "blk_freeze_queue_start",
	}
	addresses := make(map[string]symbol.KsymbolRange, len(names))
	for _, name := range names {
		addresses[name] = symbol.KsymbolRange{Start: 1}
	}
	return addresses
}

func ioHealthTestHookOptions(t *testing.T) []bpf.AttachOption {
	t.Helper()
	options, err := selectIOHealthHooks(ioHealthTestSymbols(), func(string, string) (bool, error) {
		return true, nil
	})
	require.NoError(t, err)
	return options
}

func TestIOHealthHookSelection(t *testing.T) {
	for _, test := range []struct {
		name          string
		missing       []string
		noTracepoints []string
		traceError    error
		wantErr       error
		wantAbsent    []string
		wantPresent   map[string]string
	}{
		{
			name: "all sources",
			wantPresent: map[string]string{
				"trace_block_rq_error":         "block/block_rq_error",
				"io_health_freeze_queue":       "__blk_freeze_queue_start",
				"kprobe_nvme_sysfs_show_state": "nvme_sysfs_show_state",
				"kretprobe_nvme_reset":         "nvme_reset_ctrl",
				"kprobe_nvme_reset":            "nvme_reset_ctrl",
			},
		},
		{
			name:    "NVMe not compiled or loaded",
			missing: []string{"nvme_timeout", "nvme_reset_ctrl", "nvme_change_ctrl_state", "nvme_sysfs_show_state"},
			wantAbsent: []string{
				"kprobe_nvme_timeout", "kretprobe_nvme_reset", "kprobe_nvme_reset",
				"kprobe_nvme_change_state", "kretprobe_nvme_change_state",
				"kprobe_nvme_sysfs_show_state", "kprobe_nvme_cdev_add", "kprobe_nvme_cdev_del",
			},
			wantPresent: map[string]string{"trace_scsi_timeout": "scsi/scsi_dispatch_cmd_timeout"},
		},
		{
			name:        "NVMe fabrics without PCI",
			missing:     []string{"nvme_timeout"},
			wantAbsent:  []string{"kprobe_nvme_timeout"},
			wantPresent: map[string]string{"kprobe_nvme_reset": "nvme_reset_ctrl"},
		},
		{
			name:          "SCSI not compiled or loaded",
			noTracepoints: []string{"scsi/scsi_dispatch_cmd_timeout", "scsi/scsi_dispatch_cmd_error"},
			wantAbsent:    []string{"trace_scsi_timeout", "trace_scsi_dispatch_error"},
			wantPresent:   map[string]string{"kprobe_nvme_timeout": "nvme_timeout"},
		},
		{
			name:          "legacy block error source",
			noTracepoints: []string{"block/block_rq_error"},
			wantAbsent:    []string{"trace_block_rq_error"},
			wantPresent:   map[string]string{"trace_block_rq_complete_error": "block_rq_complete"},
		},
		{
			name:          "tracefs not mounted",
			noTracepoints: []string{"block/block_rq_error", "scsi/scsi_dispatch_cmd_timeout", "scsi/scsi_dispatch_cmd_error"},
			wantAbsent:    []string{"trace_block_rq_error", "trace_scsi_timeout", "trace_scsi_dispatch_error"},
			wantPresent: map[string]string{
				"trace_block_rq_complete_error": "block_rq_complete",
				"io_health_freeze_queue":        "__blk_freeze_queue_start",
			},
		},
		{
			name:        "legacy freeze implementation",
			missing:     []string{"__blk_freeze_queue_start", "blk_mq_freeze_queue_nomemsave"},
			wantPresent: map[string]string{"io_health_freeze_queue": "blk_freeze_queue_start"},
		},
		{
			name:        "freeze wrapper inlined",
			missing:     []string{"blk_mq_freeze_queue"},
			wantPresent: map[string]string{"io_health_freeze_queue": "__blk_freeze_queue_start"},
		},
		{
			name:       "freeze unavailable",
			missing:    []string{"__blk_freeze_queue_start", "blk_freeze_queue_start"},
			wantAbsent: []string{"io_health_freeze_queue"},
		},
		{
			name:    "missing topology observer",
			missing: []string{"device_add"},
			wantErr: types.ErrNotSupported,
		},
		{
			name:    "NVMe mapping without cleanup",
			missing: []string{"cdev_device_del"},
			wantErr: types.ErrNotSupported,
		},
		{
			name:       "tracefs access denied",
			traceError: unix.EACCES,
			wantErr:    unix.EACCES,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			addresses := ioHealthTestSymbols()
			for _, name := range test.missing {
				delete(addresses, name)
			}
			options, err := selectIOHealthHooks(addresses, func(group, event string) (bool, error) {
				return !slices.Contains(test.noTracepoints, group+"/"+event), test.traceError
			})
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Empty(t, options)
				return
			}
			require.NoError(t, err)
			selected := make(map[string]string, len(options))
			for _, option := range options {
				selected[option.ProgramName] = option.Symbol
			}
			for _, program := range test.wantAbsent {
				require.NotContains(t, selected, program)
			}
			for program, symbol := range test.wantPresent {
				require.Equal(t, symbol, selected[program], program)
			}
		})
	}
}

type fakeIOHealthAttachBPF struct {
	bpf.BPF
	attachErrors map[string]error
	programs     []string
}

func (f *fakeIOHealthAttachBPF) AttachWithOptions(options []bpf.AttachOption) error {
	for _, option := range options {
		f.programs = append(f.programs, option.ProgramName)
		if err := f.attachErrors[option.ProgramName]; err != nil {
			return err
		}
	}
	return nil
}

func TestAttachIOHealthHooks(t *testing.T) {
	options := ioHealthTestHookOptions(t)
	wantPrograms := []string{
		"io_health_freeze_disk_removed",
		"io_health_disk_publishing",
		"io_health_freeze_disk_added",
		"kretprobe_nvme_cdev_del",
		"kprobe_nvme_cdev_del",
		"kretprobe_nvme_cdev_add",
		"kprobe_nvme_cdev_add",
		"kprobe_nvme_sysfs_show_state",
		"trace_block_rq_error",
		"kretprobe_nvme_change_state",
		"kprobe_nvme_change_state",
		"kprobe_nvme_timeout",
		"kretprobe_nvme_reset",
		"kprobe_nvme_reset",
		"trace_scsi_timeout",
		"trace_scsi_dispatch_error",
		"io_health_freeze_queue",
	}
	require.Len(t, options, len(wantPrograms))
	for _, test := range []struct {
		name      string
		attachErr error
		primeErr  error
	}{
		{name: "ready"},
		{name: "selected entry hook rejected", attachErr: unix.EPERM},
		{name: "selected hook missing", attachErr: unix.ENOENT},
		{name: "controller bootstrap failed", primeErr: errors.New("read controller state")},
		{name: "controller bootstrap ENOENT", primeErr: unix.ENOENT},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &fakeIOHealthAttachBPF{attachErrors: map[string]error{
				"kprobe_nvme_change_state": test.attachErr,
			}}
			primed := false
			err := attachIOHealthHooks(
				object,
				options,
				func() error {
					require.Equal(t, wantPrograms, object.programs)
					primed = true
					return test.primeErr
				},
			)
			switch {
			case test.attachErr != nil:
				require.ErrorIs(t, err, test.attachErr)
				if ioControlHookUnavailable(test.attachErr) {
					require.ErrorIs(t, err, types.ErrNotSupported)
				}
				require.False(t, primed)
			case test.primeErr != nil:
				require.ErrorIs(t, err, test.primeErr)
				require.NotErrorIs(t, err, types.ErrNotSupported)
				require.True(t, primed)
			default:
				require.NoError(t, err)
				require.True(t, primed)
			}
		})
	}
}
