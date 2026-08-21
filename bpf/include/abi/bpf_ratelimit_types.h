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

#ifndef __BPF_ABI_BPF_RATELIMIT_H__
#define __BPF_ABI_BPF_RATELIMIT_H__

#include "bpf_abi.h"

struct bpf_ratelimit_event {
	u64 interval_ns;
	u64 window_start_ns;
	u64 burst;
	u64 max_burst;
	u64 events_in_window;
	u64 missed_in_window;
	u64 total_events;
	u64 total_missed;
	u64 total_elapsed_ns;
};

BPF_ABI_EXPORT(bpf_ratelimit_event);

#endif /* __BPF_ABI_BPF_RATELIMIT_H__ */
