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

/* Block device health perf events share this layout with generated Go types. */
#ifndef __BPF_ABI_BLK_DEVICE_HEALTH_H__
#define __BPF_ABI_BLK_DEVICE_HEALTH_H__

#include "bpf_abi.h"

#define BLK_DEVICE_HEALTH_CONTROLLER_NAME_LEN 16

enum blk_device_health_event_type {
	BLK_DEVICE_HEALTH_EVENT_BLOCK_ERROR = 1,
	BLK_DEVICE_HEALTH_EVENT_SCSI_TIMEOUT,
	BLK_DEVICE_HEALTH_EVENT_SCSI_DISPATCH_ERROR,
	BLK_DEVICE_HEALTH_EVENT_NVME_TIMEOUT,
	BLK_DEVICE_HEALTH_EVENT_NVME_RESET,
	BLK_DEVICE_HEALTH_EVENT_NVME_STATE_CHANGE,
};

struct blk_device_health_event {
	u64 sector;
	u32 dev;
	s32 status;
	u32 host;
	u32 channel;
	u32 target;
	u32 lun;
	u8 controller[BLK_DEVICE_HEALTH_CONTROLLER_NAME_LEN];
	u32 new_state_raw;
	u8 type;
	u8 operation;
	u8 rwbs[2];
	u64 topology_generation;
};

BPF_ABI_EXPORT(blk_device_health_event);
BPF_ABI_EXPORT_ENUM(blk_device_health_event_type);

#endif /* __BPF_ABI_BLK_DEVICE_HEALTH_H__ */
