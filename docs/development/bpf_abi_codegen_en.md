---
title: BPF ABI Guide
type: docs
author: HUATUO Team
date: 2026-03-04
weight: 6
---

The C structures on the BPF side are the source of truth for the perf event
ABI. Go types are generated automatically from BTF in the BPF objects. Do not
maintain equivalent structures manually.

## Conventions

Each ABI domain maps to one C header and one generated Go file:

| Item | Convention |
| --- | --- |
| Domain name | `<domain>` |
| C header | `bpf/include/abi/<domain>_types.h` |
| C structure prefix | `<domain>_` |
| Generated Go file | `internal/bpf/abi/<domain>_types_generated.go` |

`<domain>` must start with a lowercase letter and contain only lowercase
letters, digits, and underscores. Avoid domains with overlapping prefixes,
such as `net` and `net_rx`.

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
	u64 timestamp;
	struct sample_detail detail;
	u32 pid;
	u8 kind;
	u8 pad[3];
};

BPF_ABI_EXPORT(sample_detail);
BPF_ABI_EXPORT(sample_event);

#endif /* __BPF_ABI_SAMPLE_H__ */
```

Requirements:

- Structure names must use the `<domain>_` prefix.
- Every structure that requires a generated Go type must call
  `BPF_ABI_EXPORT(<type>)`, including nested structures.
- The header must contain only ABI types and related constants. Do not add BPF
  programs, maps, or business logic.
- Array lengths must be compile-time constants and remain consistent across
  all BPF compilation units.

### 2. Use the Header in a BPF Program

The ABI header depends on base types such as `u8` and `u16`. Include it after
`vmlinux.h` and the project base headers:

```c
#include "vmlinux.h"

#include "bpf_common.h"
#include "abi/sample_types.h"
```

Use the ABI structure directly when emitting a perf event:

```c
struct sample_event event = {};

event.timestamp = bpf_ktime_get_ns();
event.pid = bpf_get_current_pid_tgid() >> 32;
event.kind = kind;

bpf_perf_event_output(ctx, &events, COMPAT_BPF_F_CURRENT_CPU,
		      &event, sizeof(event));
```

The event must be zero-initialized to prevent uninitialized padding from being
written to the perf buffer. Any structure passed to `bpf_perf_event_output`
must be defined in an ABI header.

### 3. Generate and Use the Go Type

Run the following command from the repository root:

```bash
make gen-build
```

The generated file is located at:

```text
internal/bpf/abi/sample_types_generated.go
```

Reference the generated type directly from Go code:

```go
import "huatuo-bamai/internal/bpf/abi"

var event abi.SampleEvent
if err := reader.ReadInto(&event); err != nil {
	return fmt.Errorf("read sample event: %w", err)
}
```

The generated file contains the structure, the `SampleEventSize` size
constant, and layout assertions based on `unsafe.Sizeof` and
`unsafe.Offsetof`. Generated files are read-only. Do not edit them manually.

## Types and Layout

| Type | Support |
| --- | --- |
| Fixed-width integers of 1, 2, 4, or 8 bytes | Supported |
| Fixed-length arrays with a nonzero length | Supported |
| Nested structures that meet the same constraints | Supported |
| `typedef` with a supported target type | Supported |
| Pointers, `union`, `enum`, and floating-point types | Unsupported |
| Bit fields and `_Bool` | Unsupported |
| Zero-length and flexible arrays | Unsupported |
| Recursive structures, overlapping fields, or non-byte-aligned structures | Unsupported |

Layout requirements:

- Use fixed-width integers such as `u8`, `s16`, `u32`, and `s64`.
- Order fields according to their alignment requirements. Use `u8 pad[N]` to
  make padding explicit when necessary.
- Do not use platform-dependent types such as `long` or `unsigned long`.
- Do not use `__attribute__((packed))` to bypass layout validation.
- If a structure with the same name appears in multiple BPF objects, its
  fields, offsets, and size must match exactly.

C names are converted to exported Go names by splitting on underscores. For
example, `sample_event` becomes `SampleEvent`, and `pid_tgid` becomes
`PIDTGID`. Avoid C names that map to the same Go name, such as `sample_id` and
`sample_i_d`.

## Verification

Run the following commands after adding or modifying an ABI:

```bash
make gen-build
go test ./build/bpfabi-tool
make check
```

Add a decoding test for each new perf event. At a minimum, cover:

- Integer boundary values and native byte order.
- Nested structures and the first and last array elements.
- Fields before and after padding.
- The sample size and the generated `<GoType>Size` constant.

## Common Errors

| Error | Check |
| --- | --- |
| `has no "<domain>_" btf anchors` | Confirm that a BPF source includes the header and that the type calls `BPF_ABI_EXPORT` |
| `without matching abi header` | Confirm that the header name matches the structure prefix and that domain prefixes do not overlap |
| `differs between objects` | Check whether conditional compilation, array length macros, or included headers change the layout |
| `go offset is ... btf offset is ...` | Reorder fields or add explicit padding; do not use `packed` |
| `go type name ... collides` | Rename C types or fields that map to the same Go name |
