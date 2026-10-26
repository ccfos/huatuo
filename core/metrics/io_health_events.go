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

// Kernel and MD health observations retain source-specific identity before
// entering bounded evidence collection and persistence.
package collector

import (
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/iohealth"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/io_health.c -o $BPF_DIR/io_health.o

const (
	ioHealthEventBlockError        = uint8(abi.HealthEventBlockError)
	ioHealthEventSCSITimeout       = uint8(abi.HealthEventScsiTimeout)
	ioHealthEventSCSIDispatchError = uint8(abi.HealthEventScsiDispatchError)
	ioHealthEventNVMeTimeout       = uint8(abi.HealthEventNvmeTimeout)
	ioHealthEventNVMeReset         = uint8(abi.HealthEventNvmeReset)
	ioHealthEventNVMeStateChange   = uint8(abi.HealthEventNvmeStateChange)
)

const ioHealthNVMeControllerNameLength = len(abi.IOHealthEvent{}.Controller)

const (
	ioHealthTypeBlockError        = "block_error"
	ioHealthTypeNVMeTimeout       = "nvme_timeout"
	ioHealthTypeNVMeReset         = "nvme_reset"
	ioHealthTypeNVMeStateChange   = "nvme_state_change"
	ioHealthTypeSCSITimeout       = "scsi_timeout"
	ioHealthTypeSCSIDispatchError = "scsi_dispatch_error"
	ioHealthTypeMDSyncAction      = "md_sync_action"
	ioHealthTypeMDDegraded        = "md_degraded"
	ioHealthTypeMDMemberState     = "md_member_state"
)

// These values mirror the stable REQ_OP definitions decoded from cmd_flags.
const (
	reqOpRead uint8 = iota
	reqOpWrite
	reqOpFlush
	reqOpDiscard
)

// These values mirror the SCSI_MLQUEUE_* dispatch return values.
const (
	scsiMLQueueHostBusy   int32 = 0x1055
	scsiMLQueueDeviceBusy int32 = 0x1056
	scsiMLQueueEHRetry    int32 = 0x1057
	scsiMLQueueTargetBusy int32 = 0x1058
)

type ioHealthHook struct {
	program string
	symbol  string
}

var ioHealthBlockErrorHook = ioHealthHook{
	program: "trace_block_rq_error",
	symbol:  "block/block_rq_error",
}

var ioHealthBlockCompleteHook = ioHealthHook{
	program: "trace_block_rq_complete_error",
	symbol:  "block_rq_complete",
}

var ioHealthNVMeMappingHook = ioHealthHook{
	program: "kprobe_nvme_sysfs_show_state",
	symbol:  "nvme_sysfs_show_state",
}

var ioHealthNVMeAddHooks = []ioHealthHook{
	{
		program: "kretprobe_nvme_cdev_add",
		symbol:  "cdev_device_add",
	},
	{
		program: "kprobe_nvme_cdev_add",
		symbol:  "cdev_device_add",
	},
}

var ioHealthNVMeDeleteHooks = []ioHealthHook{
	{
		program: "kretprobe_nvme_cdev_del",
		symbol:  "cdev_device_del",
	},
	{
		program: "kprobe_nvme_cdev_del",
		symbol:  "cdev_device_del",
	},
}

var ioHealthNVMeStateHooks = []ioHealthHook{
	{
		program: "kretprobe_nvme_change_state",
		symbol:  "nvme_change_ctrl_state",
	},
	{
		program: "kprobe_nvme_change_state",
		symbol:  "nvme_change_ctrl_state",
	},
}

var ioHealthHooks = []ioHealthHook{
	{
		program: "kprobe_nvme_timeout",
		symbol:  "nvme_timeout",
	},
	{
		program: "kretprobe_nvme_reset",
		symbol:  "nvme_reset_ctrl",
	},
	{
		program: "kprobe_nvme_reset",
		symbol:  "nvme_reset_ctrl",
	},
	{
		program: "trace_scsi_timeout",
		symbol:  "scsi/scsi_dispatch_cmd_timeout",
	},
	{
		program: "trace_scsi_dispatch_error",
		symbol:  "scsi/scsi_dispatch_cmd_error",
	},
}

var ioHealthTopologyHooks = []ioHealthHook{
	{
		program: "io_health_freeze_disk_removed",
		symbol:  "del_gendisk",
	},
	{
		program: "io_health_disk_publishing",
		symbol:  "device_add",
	},
	{
		program: "io_health_freeze_disk_added",
		symbol:  "blk_register_queue",
	},
}

var ioHealthFreezeHooks = []ioHealthHook{
	// Observe the implementation that increments freeze depth. Wrappers may
	// be inlined at call sites even when their symbols remain in kallsyms.
	{
		program: "io_health_freeze_queue",
		symbol:  "__blk_freeze_queue_start",
	},
	{
		program: "io_health_freeze_queue",
		symbol:  "blk_freeze_queue_start",
	},
}

func loadIOHealthHookOptions() ([]bpf.AttachOption, error) {
	names := []string{}
	for _, group := range [][]ioHealthHook{
		ioHealthTopologyHooks, ioHealthNVMeAddHooks, ioHealthNVMeDeleteHooks,
		ioHealthNVMeStateHooks, ioHealthHooks, ioHealthFreezeHooks,
		{ioHealthNVMeMappingHook},
	} {
		for _, hook := range group {
			if !strings.Contains(hook.symbol, "/") {
				names = append(names, hook.symbol)
			}
		}
	}
	addresses, err := symbol.KsymbolSearchAddr(names, symbol.KsymbolLookupDefault)
	if err != nil {
		return nil, fmt.Errorf("read io_health kernel symbols: %w", err)
	}
	options, err := selectIOHealthHooks(addresses, bpf.TracepointAvailable)
	if err != nil {
		return nil, fmt.Errorf("%w: select io_health hooks: %w", types.ErrTracingStopped, err)
	}
	return options, nil
}

// Select before attaching: absent drivers omit only their event sources.
// A selected hook which cannot attach rejects the whole kernel session.
func selectIOHealthHooks(
	addresses map[string]symbol.KsymbolRange,
	hasTracepoint func(string, string) (bool, error),
) ([]bpf.AttachOption, error) {
	options := make([]bpf.AttachOption, 0, 17)
	add := func(hooks ...ioHealthHook) error {
		for _, hook := range hooks {
			if addresses[hook.symbol].Start == 0 {
				return fmt.Errorf("%w: io_health kernel symbol %s is unavailable", types.ErrNotSupported, hook.symbol)
			}
			options = append(options, bpf.AttachOption{
				ProgramName: hook.program,
				Symbol:      hook.symbol,
			})
		}
		return nil
	}
	// Identity observers precede every event source and binding writer.
	if err := add(ioHealthTopologyHooks...); err != nil {
		return nil, err
	}
	if addresses["nvme_timeout"].Start != 0 || addresses["nvme_reset_ctrl"].Start != 0 ||
		addresses["nvme_change_ctrl_state"].Start != 0 {
		if err := add(ioHealthNVMeDeleteHooks...); err != nil {
			return nil, err
		}
		if err := add(ioHealthNVMeAddHooks...); err != nil {
			return nil, err
		}
		if err := add(ioHealthNVMeMappingHook); err != nil {
			return nil, err
		}
	}

	blockHook := ioHealthBlockCompleteHook
	available, err := hasTracepoint("block", "block_rq_error")
	if err != nil {
		return nil, fmt.Errorf("check block_rq_error tracepoint: %w", err)
	}
	if available {
		blockHook = ioHealthBlockErrorHook
	}
	options = append(options, bpf.AttachOption{
		ProgramName: blockHook.program,
		Symbol:      blockHook.symbol,
	})
	if addresses["nvme_change_ctrl_state"].Start != 0 {
		if err := add(ioHealthNVMeStateHooks...); err != nil {
			return nil, err
		}
	}
	for _, hook := range ioHealthHooks {
		group, name, isTracepoint := strings.Cut(hook.symbol, "/")
		if !isTracepoint {
			if addresses[hook.symbol].Start != 0 {
				if err := add(hook); err != nil {
					return nil, err
				}
			}
			continue
		}
		available, err := hasTracepoint(group, name)
		if err != nil {
			return nil, fmt.Errorf("check %s tracepoint: %w", hook.symbol, err)
		}
		if available {
			options = append(options, bpf.AttachOption{
				ProgramName: hook.program,
				Symbol:      hook.symbol,
			})
		}
	}
	for _, hook := range ioHealthFreezeHooks {
		if addresses[hook.symbol].Start != 0 {
			if err := add(hook); err != nil {
				return nil, err
			}
			break
		}
	}
	return options, nil
}

func attachIOHealthHooks(
	object bpf.BPF,
	options []bpf.AttachOption,
	primeNVMeControllers func() error,
) error {
	if err := object.AttachWithOptions(options); err != nil {
		if ioControlHookUnavailable(err) {
			return fmt.Errorf("%w: attach io_health hooks: %w", types.ErrNotSupported, err)
		}
		return fmt.Errorf("attach io_health hooks: %w", err)
	}
	for _, option := range options {
		if option.ProgramName == ioHealthNVMeMappingHook.program && primeNVMeControllers != nil {
			if err := primeNVMeControllers(); err != nil {
				return fmt.Errorf("map NVMe controllers: %w", err)
			}
			break
		}
	}
	return nil
}

func loadIOHealthKernelEnums() (map[uint32]string, uint32) {
	// Older request flags are macros, with no enum in BTF.
	quietMask := uint32(1 << 11)
	spec, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, quietMask
	}
	quietMask = ioHealthRequestQuietMask(spec)
	states := ioHealthNVMeStates(spec)
	if len(states) == 0 {
		if module, err := btf.LoadKernelModuleSpec("nvme_core"); err == nil {
			states = ioHealthNVMeStates(module)
		}
	}
	return states, quietMask
}

func ioHealthBlockErrorLayout(spec *btf.Spec) (stride, count uint32, err error) {
	// BLK_STS_* are macros, not a stable ABI. Some kernels omit BTF VARs
	// but retain blk_errors' anonymous array type. Match its complete shape;
	// BPF also validates the table prefix before reading an error entry.
	types := spec.Iterate()
	for types.Next() {
		array, ok := types.Type.(*btf.Array)
		if !ok || array.Nelems < 4 || array.Nelems > 256 {
			continue
		}
		entry, ok := btf.UnderlyingType(array.Type).(*btf.Struct)
		if !ok || entry.Name != "" || (len(entry.Members) != 2 && len(entry.Members) != 3) {
			continue
		}
		names := []string{"errno", "name"}
		if len(entry.Members) == 3 {
			names = []string{"errno", "tag", "name"}
		}
		if entry.Size != uint32(8*len(names)) {
			continue
		}
		matches := true
		for i, member := range entry.Members {
			if member.Name != names[i] || member.Offset != btf.Bits(i*64) || member.BitfieldSize != 0 {
				matches = false
				break
			}
			if i == 0 {
				integer, ok := btf.UnderlyingType(member.Type).(*btf.Int)
				matches = ok && integer.Size == 4 && integer.Encoding == btf.Signed
			} else {
				pointer, ok := btf.UnderlyingType(member.Type).(*btf.Pointer)
				if !ok {
					matches = false
					break
				}
				char, ok := btf.UnderlyingType(pointer.Target).(*btf.Int)
				matches = ok && char.Size == 1 && char.Name == "char"
			}
			if !matches {
				break
			}
		}
		if !matches {
			continue
		}
		if count != 0 && (stride != entry.Size || count != array.Nelems) {
			return 0, 0, fmt.Errorf("ambiguous blk_errors array layout in kernel BTF")
		}
		stride, count = entry.Size, array.Nelems
	}
	if count == 0 {
		return 0, 0, fmt.Errorf("blk_errors array layout not found in kernel BTF")
	}
	return stride, count, nil
}

func ioHealthRequestQuietMask(spec *btf.Spec) uint32 {
	var flags *btf.Enum
	if err := spec.TypeByName("rqf_flags", &flags); err != nil {
		return 1 << 11
	}
	for _, flag := range flags.Values {
		if flag.Name == "__RQF_QUIET" && flag.Value < 32 {
			return uint32(1) << flag.Value
		}
	}
	return 0
}

func ioHealthNVMeStates(spec *btf.Spec) map[uint32]string {
	var state *btf.Enum
	if err := spec.TypeByName("nvme_ctrl_state", &state); err != nil {
		return nil
	}
	names := make(map[uint32]string, len(state.Values))
	for _, value := range state.Values {
		if strings.HasPrefix(value.Name, "NVME_CTRL_") {
			names[uint32(value.Value)] = strings.ToLower(
				strings.TrimPrefix(value.Name, "NVME_CTRL_"))
		}
	}
	return names
}

type ioHealthEvidenceSubmitter interface {
	Submit(request iohealth.EvidenceRequest) bool
}

func (c *ioHealthCollector) handleKernelEvent(
	raw abi.IOHealthEvent,
	worker ioHealthEvidenceSubmitter,
	current func() bool,
) {
	triggeredAt := c.now()
	// Validate after resolution: a buffered event must not inherit the new
	// disk's name or evidence. Worker identity checks cover later replacement.
	resolve := func(target ioHealthResolvedTarget) ioHealthResolvedTarget {
		if current != nil && !current() {
			worker = nil
			return ioHealthResolvedTarget{eventDevice: "unknown"}
		}
		return target
	}

	// Block errors, NVMe timeouts, and SCSI anomalies may collect evidence
	// after resolving one target. NVMe resets only update their counter and
	// persist the event: the controller may be unavailable during reset, and a
	// later nvme-cli query cannot recover its pre-reset state.
	switch raw.Type {
	case ioHealthEventBlockError:
		target := resolve(c.resolver.resolveBlockDevice(raw.Dev))
		status := ioHealthBlockStatus(raw.Status)
		event := types.IOHealthEvent{
			Type:      ioHealthTypeBlockError,
			Device:    target.eventDevice,
			Operation: ioHealthOperation(raw),
			Status:    status,
			Sector:    uint64Pointer(raw.Sector),
		}
		c.incrementCounter(ioHealthCounterKey{
			kind:      ioHealthCounterBlockError,
			device:    event.Device,
			operation: event.Operation,
			status:    event.Status,
		})
		c.collectEvidenceOrPersist(worker, triggeredAt, event, target)

	case ioHealthEventNVMeTimeout:
		event := types.IOHealthEvent{
			Type:   ioHealthTypeNVMeTimeout,
			Device: "unknown",
		}
		var target ioHealthResolvedTarget
		if raw.Dev != 0 {
			target = resolve(c.resolver.resolveBlockDevice(raw.Dev))
			event.Device = target.eventDevice
		}
		c.incrementCounter(ioHealthCounterKey{
			kind:   ioHealthCounterNVMeTimeout,
			device: event.Device,
		})
		if raw.Dev == 0 {
			c.persistEvent(triggeredAt, event)
			return
		}
		c.collectEvidenceOrPersist(worker, triggeredAt, event, target)

	case ioHealthEventNVMeReset:
		event := types.IOHealthEvent{
			Type:   ioHealthTypeNVMeReset,
			Device: ioHealthControllerName(raw.Controller),
		}
		c.incrementCounter(ioHealthCounterKey{
			kind:   ioHealthCounterNVMeReset,
			device: event.Device,
		})
		c.persistEvent(triggeredAt, event)

	case ioHealthEventNVMeStateChange:
		newStateRaw := raw.NewStateRaw
		c.persistEvent(triggeredAt, types.IOHealthEvent{
			Type:        ioHealthTypeNVMeStateChange,
			Device:      ioHealthControllerName(raw.Controller),
			NewState:    ioHealthNVMeStateName(c.nvmeStates, newStateRaw),
			NewStateRaw: &newStateRaw,
		})

	case ioHealthEventSCSITimeout:
		target := resolve(c.resolver.resolveSCSI(
			raw.Host,
			raw.Channel,
			raw.Target,
			raw.Lun,
		))
		event := types.IOHealthEvent{
			Type:   ioHealthTypeSCSITimeout,
			Device: target.eventDevice,
		}
		c.incrementCounter(ioHealthCounterKey{
			kind:   ioHealthCounterSCSITimeout,
			device: event.Device,
		})
		c.collectEvidenceOrPersist(worker, triggeredAt, event, target)

	case ioHealthEventSCSIDispatchError:
		target := resolve(c.resolver.resolveSCSI(
			raw.Host,
			raw.Channel,
			raw.Target,
			raw.Lun,
		))
		event := types.IOHealthEvent{
			Type:   ioHealthTypeSCSIDispatchError,
			Device: target.eventDevice,
			Status: ioHealthSCSIDispatchStatus(raw.Status),
		}
		c.incrementCounter(ioHealthCounterKey{
			kind:   ioHealthCounterSCSIDispatchError,
			device: event.Device,
			status: event.Status,
		})
		c.collectEvidenceOrPersist(worker, triggeredAt, event, target)
	}
}

//nolint:gocritic // Handle the notification as an immutable value snapshot.
func (c *ioHealthCollector) handleMDChange(
	change iohealth.MDChange,
	worker ioHealthEvidenceSubmitter,
) {
	event := types.IOHealthEvent{
		Array:    change.Array,
		Member:   change.Member,
		OldState: change.OldState,
		NewState: change.NewState,
	}
	switch change.Field {
	case iohealth.MDFieldSyncAction:
		event.Type = ioHealthTypeMDSyncAction
	case iohealth.MDFieldDegraded:
		event.Type = ioHealthTypeMDDegraded
	case iohealth.MDFieldMemberState:
		event.Type = ioHealthTypeMDMemberState
	default:
		return
	}

	if change.Field != iohealth.MDFieldMemberState ||
		!ioHealthMDMemberFault(change.NewState) {
		c.persistEvent(change.ObservedAt, event)
		return
	}
	target := c.resolver.resolveBlockName(change.Member)
	event.Device = target.eventDevice
	if change.MemberIdentity == "" || c.resolver.blockIdentity(change.Member) != change.MemberIdentity {
		c.persistEvent(change.ObservedAt, event)
		return
	}
	c.collectEvidenceOrPersist(worker, change.ObservedAt, event, target)
}

func ioHealthMDMemberFault(state string) bool {
	for _, value := range strings.Split(state, ",") {
		switch value {
		case "faulty", "blocked", "write_error", iohealth.MDMemberStateRemoved:
			return true
		}
	}
	return false
}

//nolint:gocritic // Copy the event into the queued evidence request.
func (c *ioHealthCollector) collectEvidenceOrPersist(
	worker ioHealthEvidenceSubmitter,
	triggeredAt time.Time,
	event types.IOHealthEvent,
	target ioHealthResolvedTarget,
) {
	if worker == nil {
		c.persistEvent(triggeredAt, event)
		return
	}
	if !worker.Submit(iohealth.EvidenceRequest{
		Trigger:     event,
		Target:      target.target,
		Identity:    target.identity,
		Protocol:    target.protocol,
		TriggeredAt: triggeredAt,
		Reason:      target.reason,
	}) {
		c.persistEvent(triggeredAt, event)
	}
}

func ioHealthControllerName(raw [ioHealthNVMeControllerNameLength]uint8) string {
	length := len(raw)
	for index, value := range raw {
		if value == 0 {
			length = index
			break
		}
	}
	name := string(raw[:length])
	if !ioHealthNVMeControllerPattern.MatchString(name) {
		return "unknown"
	}
	return name
}

func ioHealthNVMeStateName(states map[uint32]string, raw uint32) string {
	if name, ok := states[raw]; ok {
		return name
	}
	return "unknown"
}

func ioHealthOperation(raw abi.IOHealthEvent) string {
	if raw.Rwbs[0] != 0 {
		operation := raw.Rwbs[0]
		// REQ_PREFLUSH precedes the actual operation, for example "FW".
		if operation == 'F' {
			switch raw.Rwbs[1] {
			case 'R', 'W', 'D', 'F':
				operation = raw.Rwbs[1]
			}
		}
		switch operation {
		case 'R':
			raw.Operation = reqOpRead
		case 'W':
			raw.Operation = reqOpWrite
		case 'F':
			raw.Operation = reqOpFlush
		case 'D':
			raw.Operation = reqOpDiscard
		default:
			return "unknown"
		}
	}
	switch raw.Operation {
	case reqOpRead:
		return "read"
	case reqOpWrite:
		return "write"
	case reqOpFlush:
		return "flush"
	case reqOpDiscard:
		return "discard"
	default:
		return "unknown"
	}
}

func ioHealthBlockStatus(status int32) string {
	switch status {
	case -int32(unix.EOPNOTSUPP):
		return "not_supported"
	case -int32(unix.ETIMEDOUT):
		return "timeout"
	case -int32(unix.ENOSPC):
		return "no_space"
	case -int32(unix.ENOLINK),
		-int32(unix.EREMOTEIO),
		-int32(unix.EBADE):
		return "transport"
	case -int32(unix.ENODATA):
		return "medium_error"
	case -int32(unix.EILSEQ):
		return "protection"
	case -int32(unix.ENOMEM),
		-int32(unix.EBUSY),
		-int32(unix.EAGAIN),
		-int32(unix.EREMCHG),
		-int32(unix.ETOOMANYREFS),
		-int32(unix.EOVERFLOW):
		return "resource"
	case -int32(unix.ENODEV):
		return "offline"
	default:
		// Positive values are raw blk_status_t values which could not be
		// converted with the running kernel's table. Do not guess their ABI.
		if status >= 0 {
			return "unknown"
		}
		return "io_error"
	}
}

func ioHealthSCSIDispatchStatus(status int32) string {
	switch status {
	case scsiMLQueueHostBusy:
		return "host_busy"
	case scsiMLQueueDeviceBusy:
		return "device_busy"
	case scsiMLQueueEHRetry:
		return "eh_retry"
	case scsiMLQueueTargetBusy:
		return "target_busy"
	default:
		return "unknown"
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}
