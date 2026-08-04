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
	"errors"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/ioobserve/health"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"

	"golang.org/x/sys/unix"
)

const (
	ioHealthEventBlockError = iota + 1
	ioHealthEventSCSITimeout
	ioHealthEventSCSIDispatchError
	ioHealthEventNVMeTimeout
	ioHealthEventNVMeReset
	ioHealthEventNVMeStateChange
)

const ioHealthNVMeControllerNameLength = 16

const (
	ioHealthTypeBlockError        = "block_error"
	ioHealthTypeNVMeTimeout       = "nvme_timeout"
	ioHealthTypeNVMeReset         = "nvme_reset"
	ioHealthTypeNVMeStateChange   = "nvme_state_change"
	ioHealthTypeSCSITimeout       = "scsi_timeout"
	ioHealthTypeSCSIDispatchError = "scsi_dispatch_error"
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

// ioHealthPerfEvent mirrors struct health_event in bpf/io_health.c.
type ioHealthPerfEvent struct {
	Sector      uint64
	Dev         uint32
	Status      int32
	Host        uint32
	Channel     uint32
	Target      uint32
	LUN         uint32
	Controller  [ioHealthNVMeControllerNameLength]uint8
	NewStateRaw uint32
	Type        uint8
	Operation   uint8
	Pad         [2]uint8
}

type ioHealthNVMeStateLayout uint8

const (
	ioHealthNVMeStateUnknown ioHealthNVMeStateLayout = iota
	ioHealthNVMeStateLinux418
	ioHealthNVMeStateLinux510
)

// Linux 4.18 includes ADMIN_ONLY. Linux 5.10 and later include
// DELETING_NOIO instead, so their raw enum values require separate tables.
var ioHealthNVMeStatesLinux418 = [...]string{
	"new",
	"live",
	"admin_only",
	"resetting",
	"connecting",
	"deleting",
	"dead",
}

var ioHealthNVMeStatesLinux510 = [...]string{
	"new",
	"live",
	"resetting",
	"connecting",
	"deleting",
	"deleting_noio",
	"dead",
}

var ioHealthHostKernelRelease = detectIOHealthKernelRelease()

var ioHealthHostNVMeStateLayout = ioHealthNVMeStateLayoutForRelease(
	ioHealthHostKernelRelease,
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

var ioHealthNVMeDeleteHook = ioHealthHook{
	program: "kprobe_nvme_cdev_del",
	symbol:  "cdev_device_del",
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
	{program: "kprobe_nvme_timeout", symbol: "nvme_timeout"},
	{program: "kprobe_nvme_reset", symbol: "nvme_reset_ctrl"},
	{program: "trace_scsi_timeout", symbol: "scsi/scsi_dispatch_cmd_timeout"},
	{program: "trace_scsi_dispatch_error", symbol: "scsi/scsi_dispatch_cmd_error"},
}

func attachIOHealthHooks(
	object bpf.BPF,
	primeNVMeControllers func() error,
	legacyBlockCompleteSupported bool,
) (attached int) {
	for _, hook := range ioHealthNVMeAddHooks {
		if err := bpf.AttachIndependently(object, bpf.AttachOption{
			ProgramName: hook.program,
			Symbol:      hook.symbol,
		}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			break
		}
	}
	if err := bpf.AttachIndependently(object, bpf.AttachOption{
		ProgramName: ioHealthNVMeDeleteHook.program,
		Symbol:      ioHealthNVMeDeleteHook.symbol,
	}); err != nil {
		log.Warnf(
			"io_health: attach optional hook %s: %v",
			ioHealthNVMeDeleteHook.symbol,
			err,
		)
	}

	if err := bpf.AttachIndependently(object, bpf.AttachOption{
		ProgramName: ioHealthNVMeMappingHook.program,
		Symbol:      ioHealthNVMeMappingHook.symbol,
	}); err != nil {
		log.Warnf(
			"io_health: attach optional hook %s: %v",
			ioHealthNVMeMappingHook.symbol,
			err,
		)
	} else {
		if primeNVMeControllers != nil {
			if err := primeNVMeControllers(); err != nil {
				log.Warnf("io_health: map NVMe controllers: %v", err)
			}
		}
		if err := bpf.DetachProgram(object, ioHealthNVMeMappingHook.program); err != nil {
			log.Warnf(
				"io_health: detach bootstrap hook %s: %v",
				ioHealthNVMeMappingHook.symbol,
				err,
			)
			return 0
		}
	}

	err := bpf.AttachIndependently(object, bpf.AttachOption{
		ProgramName: ioHealthBlockErrorHook.program,
		Symbol:      ioHealthBlockErrorHook.symbol,
	})
	if err == nil {
		attached++
	} else {
		log.Warnf(
			"io_health: attach optional hook %s: %v",
			ioHealthBlockErrorHook.symbol,
			err,
		)
		if errors.Is(err, unix.ENOENT) && legacyBlockCompleteSupported {
			if err := bpf.AttachIndependently(object, bpf.AttachOption{
				ProgramName: ioHealthBlockCompleteHook.program,
				Symbol:      ioHealthBlockCompleteHook.symbol,
			}); err != nil {
				log.Warnf(
					"io_health: attach optional hook %s: %v",
					ioHealthBlockCompleteHook.symbol,
					err,
				)
			} else {
				attached++
			}
		}
	}

	stateHooksAttached := true
	for _, hook := range ioHealthNVMeStateHooks {
		if err := bpf.AttachIndependently(object, bpf.AttachOption{
			ProgramName: hook.program,
			Symbol:      hook.symbol,
		}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			stateHooksAttached = false
			break
		}
	}
	if stateHooksAttached {
		attached++
	}

	for _, hook := range ioHealthHooks {
		if err := bpf.AttachIndependently(object, bpf.AttachOption{
			ProgramName: hook.program,
			Symbol:      hook.symbol,
		}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			continue
		}
		attached++
	}

	return attached
}

func detectIOHealthKernelRelease() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return ""
	}

	release := make([]byte, 0, len(name.Release))
	for _, value := range name.Release {
		if value == 0 {
			break
		}
		release = append(release, byte(value))
	}
	return string(release)
}

func ioHealthKernelVersion(release string) (major, minor int, ok bool) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func ioHealthLegacyBlockCompleteSupported(release string) bool {
	major, minor, ok := ioHealthKernelVersion(release)
	if !ok {
		return false
	}
	return major < 5 || (major == 5 && minor <= 15)
}

type ioHealthEvidenceSubmitter interface {
	Submit(health.EvidenceRequest) bool
}

func (c *ioHealthCollector) handleKernelEvent(
	raw ioHealthPerfEvent,
	worker ioHealthEvidenceSubmitter,
) {
	triggeredAt := c.now()

	// Block errors, NVMe timeouts, and SCSI anomalies may collect evidence
	// after resolving one target. NVMe resets only update their counter and
	// persist the event: the controller may be unavailable during reset, and a
	// later nvme-cli query cannot recover its pre-reset state.
	switch raw.Type {
	case ioHealthEventBlockError:
		target := c.resolver.resolveBlockDevice(raw.Dev)
		event := types.IOHealthEvent{
			Type:      ioHealthTypeBlockError,
			Device:    target.eventDevice,
			Operation: ioHealthOperation(raw.Operation),
			Status:    ioHealthBlockStatus(raw.Status),
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
			target = c.resolver.resolveBlockDevice(raw.Dev)
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
			NewState:    ioHealthNVMeStateName(ioHealthHostNVMeStateLayout, newStateRaw),
			NewStateRaw: &newStateRaw,
		})

	case ioHealthEventSCSITimeout:
		target := c.resolver.resolveSCSI(
			raw.Host,
			raw.Channel,
			raw.Target,
			raw.LUN,
		)
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
		target := c.resolver.resolveSCSI(
			raw.Host,
			raw.Channel,
			raw.Target,
			raw.LUN,
		)
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

func (c *ioHealthCollector) collectEvidenceOrPersist(
	worker ioHealthEvidenceSubmitter,
	triggeredAt time.Time,
	event types.IOHealthEvent,
	target ioHealthResolvedTarget,
) {
	protocol := health.EvidenceProtocol("")
	switch target.protocol {
	case ioHealthProtocolNVMe:
		protocol = health.EvidenceProtocolNVMe
	case ioHealthProtocolSCSI:
		protocol = health.EvidenceProtocolSCSI
	}
	if worker != nil && worker.Submit(health.EvidenceRequest{
		Trigger:     event,
		Target:      target.target,
		Protocol:    protocol,
		TriggeredAt: triggeredAt,
		Reason:      target.reason,
	}) {
		return
	}
	c.persistEvent(triggeredAt, event)
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

func ioHealthNVMeStateLayoutForRelease(release string) ioHealthNVMeStateLayout {
	major, minor, ok := ioHealthKernelVersion(release)
	if !ok {
		return ioHealthNVMeStateUnknown
	}

	if major == 4 && minor == 18 {
		return ioHealthNVMeStateLinux418
	}
	if major > 5 || major == 5 && minor >= 10 {
		return ioHealthNVMeStateLinux510
	}
	return ioHealthNVMeStateUnknown
}

func ioHealthNVMeStateName(layout ioHealthNVMeStateLayout, raw uint32) string {
	switch layout {
	case ioHealthNVMeStateLinux418:
		if raw < uint32(len(ioHealthNVMeStatesLinux418)) {
			return ioHealthNVMeStatesLinux418[raw]
		}
	case ioHealthNVMeStateLinux510:
		if raw < uint32(len(ioHealthNVMeStatesLinux510)) {
			return ioHealthNVMeStatesLinux510[raw]
		}
	}
	return "unknown"
}

func ioHealthOperation(operation uint8) string {
	switch operation {
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
		if status == 0 {
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
