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

/*
 * Fixed CO-RE input for BenchmarkLoadBPFFirst. A plain ELF with no kernel-field
 * relocation would not exercise kernel BTF loading.
 *
 * Rebuild with build/clang.sh, -I bpf/include, and -C '-target bpfel' or
 * -C '-target bpfeb'; write test_btf_load-el.elf or test_btf_load-eb.elf.
 * The checked-in fixtures use Clang 18.1.8 and the x86 vmlinux.h selection.
 */
#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "GPL";

SEC("raw_tracepoint/sys_enter")
int minimal_core(void *ctx)
{
	struct task_struct *task = (void *)bpf_get_current_task();

	return BPF_CORE_READ(task, pid);
}
