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

#ifndef __BPF_ABI_CGROUP_H__
#define __BPF_ABI_CGROUP_H__

#include "bpf_abi.h"

#define CGROUP_KNODE_NAME_MAXLEN 85
#define CGROUP_KNODE_NAME_MINLEN 64

struct cgroup_css_event {
	u64 cgroup;
	u64 ops_type;
	s32 cgroup_root;
	s32 cgroup_level;
	u64 css[CGROUP_SUBSYS_COUNT];
	u8 knode_name[CGROUP_KNODE_NAME_MAXLEN + 2];
};

BPF_ABI_EXPORT(cgroup_css_event);

#endif /* __BPF_ABI_CGROUP_H__ */
