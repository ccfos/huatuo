---
title: BPF ABI 指南
type: docs
author: HUATUO Team
date: 2026-03-04
weight: 6
---

BPF 侧 C 结构体是 perf event ABI 的唯一来源。Go 类型由 BPF 对象中的 BTF
自动生成，禁止手工维护同构结构体。

## 约定

一个 ABI 域对应一个 C 头文件和一个 Go 生成文件：

| 项目 | 约定 |
| --- | --- |
| 域名 | `<domain>` |
| C 头文件 | `bpf/include/abi/<domain>_types.h` |
| C 结构体前缀 | `<domain>_` |
| Go 生成文件 | `internal/bpf/abi/<domain>_types_generated.go` |

`<domain>` 必须以小写字母开头，只能包含小写字母、数字和下划线。避免使用
前缀重叠的域，例如 `net` 和 `net_rx`。

## 实现

以下示例创建 `sample` 域。

### 1. 定义 ABI 头文件

新建 `bpf/include/abi/sample_types.h`：

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

要求：

- 结构体名称必须使用 `<domain>_` 前缀。
- 每个需要生成 Go 类型的结构体都必须调用
  `BPF_ABI_EXPORT(<type>)`，包括嵌套结构体。
- 头文件只定义 ABI 类型和相关常量，不放置 BPF 程序、map 或业务逻辑。
- 数组长度必须是编译期常量，且在所有 BPF 编译单元中保持一致。

### 2. 在 BPF 程序中使用

ABI 头文件依赖 `u8`、`u16` 等基础类型，应在 `vmlinux.h` 和项目基础头文件
之后包含：

```c
#include "vmlinux.h"

#include "bpf_common.h"
#include "abi/sample_types.h"
```

发送 perf event 时直接使用 ABI 结构体：

```c
struct sample_event event = {};

event.timestamp = bpf_ktime_get_ns();
event.pid = bpf_get_current_pid_tgid() >> 32;
event.kind = kind;

bpf_perf_event_output(ctx, &events, COMPAT_BPF_F_CURRENT_CPU,
		      &event, sizeof(event));
```

必须零初始化事件，避免将未初始化的 padding 写入 perf buffer。传给
`bpf_perf_event_output` 的结构体必须定义在 ABI 头文件中。

### 3. 生成并使用 Go 类型

在项目根目录运行：

```bash
make gen-build
```

生成结果位于：

```text
internal/bpf/abi/sample_types_generated.go
```

Go 代码直接引用生成类型：

```go
import "huatuo-bamai/internal/bpf/abi"

var event abi.SampleEvent
if err := reader.ReadInto(&event); err != nil {
	return fmt.Errorf("read sample event: %w", err)
}
```

生成文件包含结构体、`SampleEventSize` 大小常量，以及基于
`unsafe.Sizeof` 和 `unsafe.Offsetof` 的布局断言。生成文件只读，禁止手工
修改。

## 类型和布局

| 类型 | 支持情况 |
| --- | --- |
| 1、2、4、8 字节定宽整数 | 支持 |
| 非零长度的定长数组 | 支持 |
| 满足相同约束的嵌套结构体 | 支持 |
| 目标类型受支持的 `typedef` | 支持 |
| 指针、`union`、`enum`、浮点类型 | 不支持 |
| 位域、`_Bool` | 不支持 |
| 零长度数组、柔性数组 | 不支持 |
| 递归、字段重叠或非字节对齐结构体 | 不支持 |

布局要求：

- 使用 `u8`、`s16`、`u32`、`s64` 等定宽整数。
- 按对齐要求排列字段，必要时使用 `u8 pad[N]` 明确 padding。
- 不使用 `long`、`unsigned long` 等随平台变化的类型。
- 不使用 `__attribute__((packed))` 绕过布局校验。
- 同名结构体出现在多个 BPF 对象中时，字段、偏移和大小必须完全一致。

C 名称按下划线转换为 Go 导出名称，例如 `sample_event` 转换为
`SampleEvent`，`pid_tgid` 转换为 `PIDTGID`。避免使用会映射为相同 Go 名称
的 C 名称，例如 `sample_id` 和 `sample_i_d`。

## 验证

新增或修改 ABI 后执行：

```bash
make gen-build
go test ./build/bpfabi-tool
make check
```

新增 perf event 还应增加解码测试，至少覆盖：

- 整数边界值和本机字节序。
- 嵌套结构体和数组首尾元素。
- padding 前后的字段。
- 样本大小与生成的 `<GoType>Size` 常量。

## 常见错误

| 错误 | 检查项 |
| --- | --- |
| `has no "<domain>_" btf anchors` | 头文件是否被 BPF 源文件包含；类型是否调用 `BPF_ABI_EXPORT` |
| `without matching abi header` | 头文件名与结构体前缀是否一致；域前缀是否重叠 |
| `differs between objects` | 条件编译、数组长度宏和依赖头文件是否导致布局变化 |
| `go offset is ... btf offset is ...` | 调整字段顺序或增加显式 padding，不要使用 `packed` |
| `go type name ... collides` | 重命名映射为相同 Go 名称的 C 类型或字段 |
