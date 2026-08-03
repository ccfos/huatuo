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

#ifndef __BPF_ABI_OOM_H__
#define __BPF_ABI_OOM_H__

#include "bpf_abi.h"

#define OOM_CMDLINE_MAX 512
#define OOM_ENVIRON_MAX 2048

#define OOM_CAPTURE_TRUNC (1U << 0)

struct oom_event {
	u8 trigger_comm[COMPAT_TASK_COMM_LEN];
	u8 victim_comm[COMPAT_TASK_COMM_LEN];
	u32 trigger_pid;
	u32 victim_pid;
	u64 trigger_memcg_css;
	u64 victim_memcg_css;
	u64 mem_limit_pages;
	u64 mem_usage_pages;
	u64 victim_rss_anon_pages;
	u64 victim_rss_file_pages;
	u64 victim_rss_shmem_pages;
	u64 victim_total_vm_pages;
	u64 timestamp;
};

BPF_ABI_EXPORT(oom_event);

struct oom_exit_event {
	u64 timestamp;
	u32 victim_tgid;
	u16 cmdline_len;
	u16 environ_len;
	u8 cmdline_flags;
	u8 environ_flags;
	u8 go_build_info;
	u8 pad[5];
	u8 victim_cmdline[OOM_CMDLINE_MAX];
	u8 victim_environ[OOM_ENVIRON_MAX];
};

BPF_ABI_EXPORT(oom_exit_event);

#endif /* __BPF_ABI_OOM_H__ */
