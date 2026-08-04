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
	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/types"

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
	{program: "kprobe_nvme_timeout", symbol: "nvme_timeout"},
	{program: "kprobe_nvme_reset", symbol: "nvme_reset_ctrl"},
	{program: "trace_scsi_timeout", symbol: "scsi/scsi_dispatch_cmd_timeout"},
	{program: "trace_scsi_dispatch_error", symbol: "scsi/scsi_dispatch_cmd_error"},
}

func attachIOHealthHooks(
	object bpf.BPF,
	primeNVMeControllers func() error,
) (attached int) {
	// Binding writers require a working cleanup source, including bootstrap.
	cleanupAttached := true
	for index, hook := range ioHealthNVMeDeleteHooks {
		if err := object.AttachWithOptions([]bpf.AttachOption{{
			ProgramName:         hook.program,
			Symbol:              hook.symbol,
			KeepAttachedOnError: true,
		}}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			for _, previous := range ioHealthNVMeDeleteHooks[:index] {
				if err := object.Detach(previous.program); err != nil {
					log.Warnf("io_health: detach incomplete hook %s: %v", previous.symbol, err)
					return 0
				}
			}
			cleanupAttached = false
			break
		}
	}
	if cleanupAttached {
		for _, hook := range ioHealthNVMeAddHooks {
			if err := object.AttachWithOptions([]bpf.AttachOption{{
				ProgramName:         hook.program,
				Symbol:              hook.symbol,
				KeepAttachedOnError: true,
			}}); err != nil {
				log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
				break
			}
		}

		if err := object.AttachWithOptions([]bpf.AttachOption{{
			ProgramName:         ioHealthNVMeMappingHook.program,
			Symbol:              ioHealthNVMeMappingHook.symbol,
			KeepAttachedOnError: true,
		}}); err != nil {
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
			if err := object.Detach(ioHealthNVMeMappingHook.program); err != nil {
				log.Warnf(
					"io_health: detach bootstrap hook %s: %v",
					ioHealthNVMeMappingHook.symbol,
					err,
				)
				return 0
			}
		}
	}

	blockHook := ioHealthBlockErrorHook
	err := object.AttachWithOptions([]bpf.AttachOption{{
		ProgramName:         blockHook.program,
		Symbol:              blockHook.symbol,
		KeepAttachedOnError: true,
	}})
	if errors.Is(err, unix.ENOENT) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EINVAL) ||
		errors.Is(err, types.ErrNotSupported) {
		blockHook = ioHealthBlockCompleteHook
		err = object.AttachWithOptions([]bpf.AttachOption{{
			ProgramName:         blockHook.program,
			Symbol:              blockHook.symbol,
			KeepAttachedOnError: true,
		}})
	}
	if err == nil {
		attached++
	} else {
		log.Warnf(
			"io_health: attach optional hook %s: %v",
			blockHook.symbol,
			err,
		)
	}

	stateHooksAttached := true
	for _, hook := range ioHealthNVMeStateHooks {
		if err := object.AttachWithOptions([]bpf.AttachOption{{
			ProgramName:         hook.program,
			Symbol:              hook.symbol,
			KeepAttachedOnError: true,
		}}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			stateHooksAttached = false
			break
		}
	}
	if stateHooksAttached {
		attached++
	}

	for _, hook := range ioHealthHooks {
		if err := object.AttachWithOptions([]bpf.AttachOption{{
			ProgramName:         hook.program,
			Symbol:              hook.symbol,
			KeepAttachedOnError: true,
		}}); err != nil {
			log.Warnf("io_health: attach optional hook %s: %v", hook.symbol, err)
			continue
		}
		attached++
	}

	return attached
}
