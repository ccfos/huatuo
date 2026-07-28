# BPF perf event ABI

BPF perf event 的 C 结构体是 ABI 的唯一来源。Go 侧结构体由 BPF 对象中的
BTF 自动生成，禁止手工维护同构结构体。

## 定义

每个 ABI 域在 `bpf/include/abi/<domain>_types.h` 中定义。结构体名称必须使用
`<domain>_` 前缀，并通过 `BPF_ABI_EXPORT` 显式导出：

```c
struct sample_event {
	u64 timestamp;
	u32 pid;
};

BPF_ABI_EXPORT(sample_event);
```

ABI 结构体只能包含定长整数、定长数组和满足相同约束的嵌套结构体。以下类型会
导致生成失败：

- 指针、union、位域和 `_Bool`
- 零长度或柔性数组
- 非字节对齐字段
- Go 与 C 无法得到相同偏移或大小的布局

所有传给 `bpf_perf_event_output` 的结构体都必须来自 ABI 头文件。不要依赖
`packed` 绕过布局校验；需要稳定布局时使用定宽字段和显式 padding。

## 生成

运行：

```bash
make gen-build
```

该目标先编译全部 BPF 对象，再扫描 BTF 中的显式 ABI anchor，最后生成
`internal/bpf/abi/*_types_generated.go`。随后继续生成 mock 和 Cap'n Proto
文件；`make build` 和 `make unit` 均包含此步骤。

生成器会：

- 合并不同 BPF 对象中的同名、同布局类型
- 拒绝同名但布局不同的类型
- 生成确定性排序的 Go 类型
- 生成 `unsafe.Sizeof` 和 `unsafe.Offsetof` 编译期断言
- 仅清理带生成标记的过期输出，不删除手写文件

生成文件只读并由 `.gitignore` 排除。新 checkout 或修改 C ABI 后必须运行
`make gen-build`，不要直接编辑生成文件。

## 验证

提交前至少运行：

```bash
make bpf-build
make gen-build
go test ./build/bpfabi-tool
make check
```

新增 perf event 时，还需要增加包含边界值的解码测试，至少覆盖嵌套结构体、
数组首尾字段和 padding 相邻字段。
