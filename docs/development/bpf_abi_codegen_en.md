---
title: BPF ABI Guide
type: docs
author: HUATUO Team
date: 2026-03-04
weight: 7
---

C structures on the BPF side are the single source of truth for the perf event ABI. Go types are generated from BTF in BPF objects; do not maintain equivalent structures manually.

## Conventions

Each ABI domain maps to one C header and one generated Go file:

| Item | Convention |
| --- | --- |
| Domain name | `<domain>` |
| C header | `bpf/include/abi/<domain>_types.h` |
| C structure prefix | `<domain>_` |
| Generated Go file | `internal/bpf/abi/<domain>_types_generated.go` |

`<domain>` must start with a lowercase letter and contain only lowercase letters, digits, and underscores. Avoid overlapping domain prefixes such as `net` and `net_rx`.

## Implementation

The following example creates the `sample` domain.

### 1. Define the ABI Header

Create `bpf/include/abi/sample_types.h`:

```c
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

#ifndef __BPF_ABI_SAMPLE_H__
#define __BPF_ABI_SAMPLE_H__

#include "bpf_abi.h"

#define SAMPLE_DATA_LEN 4

struct sample_detail {
	u32 code;
	u8 data[SAMPLE_DATA_LEN];
};

struct sample_event {
	u64 kernel_observed_ns;
	struct sample_detail detail;
	u32 tgid;
	u8 kind;
	u8 pad[3];
};

BPF_ABI_EXPORT(sample_detail);
BPF_ABI_EXPORT(sample_event);

#endif /* __BPF_ABI_SAMPLE_H__ */
```

Requirements:

- Structure names must use the `<domain>_` prefix.
- Every structure that needs a generated Go type must call `BPF_ABI_EXPORT(<type>)`, including nested structures.
- Headers define ABI types and related constants only, with no BPF programs, maps, or business logic.
- Array lengths must be compile-time constants and consistent across all BPF compilation units.

### 2. Use the Types in BPF

ABI headers depend on base types such as `u8` and `u16`. Include them after `vmlinux.h` and the project's base headers:

```c
#include "vmlinux.h"

#include "bpf_common.h"
#include "abi/sample_types.h"
```

Use the ABI structure directly when sending a perf event:

```c
struct sample_event event = {};

event.kernel_observed_ns = bpf_ktime_get_ns();
event.tgid = bpf_get_current_pid_tgid() >> 32;
event.kind = kind;

bpf_perf_event_output(ctx, &events, COMPAT_BPF_F_CURRENT_CPU,
		      &event, sizeof(event));
```

Zero-initialize events to avoid writing uninitialized padding to the perf buffer. Structures passed to `bpf_perf_event_output` must be defined in an ABI header.

### 3. Generate and Use Go Types

Run from the repository root:

```bash
make gen-build
```

The generated file is:

```text
internal/bpf/abi/sample_types_generated.go
```

Reference the generated type directly in Go:

```go
import "github.com/ccfos/huatuo/internal/bpf/abi"

var event abi.SampleEvent
if err := reader.ReadInto(&event); err != nil {
	return fmt.Errorf("read sample event: %w", err)
}
```

The generated file contains structures, the `SampleEventSize` constant, and layout assertions based on `unsafe.Sizeof` and `unsafe.Offsetof`. Generated files are read-only; do not edit them manually.

## Types and Layout

| Type | Support |
| --- | --- |
| Fixed-width integers of 1, 2, 4, or 8 bytes | Supported |
| Fixed-length arrays with nonzero length | Supported |
| Nested structures satisfying the same constraints | Supported |
| `typedef` with a supported target type | Supported |
| Pointers, `union`, `enum`, floating-point types | Unsupported |
| Bit fields, `_Bool` | Unsupported |
| Zero-length or flexible arrays | Unsupported |
| Recursive structures, overlapping fields, or non-byte-aligned structures | Unsupported |

Layout requirements:

- Use fixed-width integers such as `u8`, `s16`, `u32`, and `s64`.
- Order fields for alignment, adding explicit `u8 pad[N]` padding where needed.
- Do not use platform-dependent types such as `long` or `unsigned long`.
- Do not bypass layout validation with `__attribute__((packed))`.
- Structures with the same name in multiple BPF objects must have identical fields, offsets, and sizes.

C names are converted from underscore-separated names to exported Go names: `sample_event` becomes `SampleEvent`, and `kernel_observed_ns` becomes `KernelObservedNS`. Follow the [field conventions](/docs/development/event-field-naming_en.md) for semantics and naming across layers. Avoid C names that map to the same Go name, such as `sample_id` and `sample_i_d`.

## Validation

After adding or changing an ABI, run:

```bash
make gen-build
go test ./build/bpfabi-tool
make check
```

For new perf events, also add decoding coverage for at least:

- Integer boundary values and native byte order.
- Nested structures and the first and last array elements.
- Fields before and after padding.
- Sample size against the generated `<GoType>Size` constant.

## Common Errors

| Error | Check |
| --- | --- |
| `has no "<domain>_" btf anchors` | Is the header included by a BPF source file? Does the type call `BPF_ABI_EXPORT`? |
| `without matching abi header` | Do the header name and structure prefix match? Do domain prefixes overlap? |
| `differs between objects` | Do conditional compilation, array-length macros, or included headers change the layout? |
| `go offset is ... btf offset is ...` | Reorder fields or add explicit padding; do not use `packed` |
| `go type name ... collides` | Rename C types or fields that map to the same Go name |
