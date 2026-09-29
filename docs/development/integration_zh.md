---
title: 集成测试
type: docs
description:
author: HUATUO Team
date: 2026-03-04
weight: 5
---

`integration/run.sh` 运行仓库的 `integration/test_*.sh` 测试集，其中既有基于固定数据的指标校验和 API 测试，也有实际加载 BPF、访问进程运行时、cgroup 和网络设备的测试。整套测试并非与宿主机内核、硬件无关。

### 前提条件与构建

在 Linux 环境的仓库根目录执行。运行器要求 **root（EUID 为 0）**，并使用 `unshare --uts --mount`、挂载和 BPF 加载。受限容器中的 root 仍可能缺少 `CAP_SYS_ADMIN` 或内核要求的 BPF/perf 权限。各测试还会检查内核 tracepoint、PMU 访问、语言运行时和辅助命令等前提。

直接调用运行器前，先构建二进制、BPF 对象和配置文件：

```bash
make build
sudo bash integration/run.sh
```

也可以在具备构建工具链的 root 环境运行 `make integration`。该目标先构建，再调用同一个运行器；非 root 执行时可能完成构建后跳过整套测试。

### 选择测试与重复执行

不带参数时运行全部 `integration/test_*.sh`。第一个参数为单个测试的文件名（不能是路径），第二个参数为正整数重复次数，默认为 1：

```bash
sudo bash integration/run.sh test_metrics.sh
sudo bash integration/run.sh test_metrics_exclude_filter.sh 10
```

### 如何判断结果

- 非 root 执行时，运行器输出 `[INTEGRATION][SKIP] ... requires root`，随后以 **0 退出，但不会运行任何测试**。
- 单个测试也可能因缺少前提而输出跳过原因并以 0 退出；运行器随后仍可能为该脚本打印 `passed`。应检查测试输出中的 `SKIP`，不能仅凭零退出码或最终成功提示认定全部测试已执行。
- 测试失败时停止后续执行，运行器会停止相关服务、打印诊断文本，并保留失败的临时工作目录；失败日志中包含目录路径。
- 成功测试的工作目录会被删除。指标固定数据测试会输出已检查的指标前缀及对应指标行。

### 指标固定数据测试

`test_metrics.sh` 使用真实的 `huatuo-bamai` 二进制和模拟的 `/proc`、`/sys` 数据。它生成临时配置、启动服务、等待 `/metrics`，然后对照预期指标文件校验响应。这减少了相应指标输入对宿主环境的依赖，但不会免除运行器的命名空间和权限要求，也不代表其他测试没有环境前提。

扩展该测试时：

1. 在 `integration/fixtures/` 下新增或更新数据，保持内核文件系统的目录结构。
2. 在 `integration/fixtures/expected_metrics/*.txt` 中添加预期指标行。非空、非注释行必须出现在 `/metrics` 输出中；新增的 `.txt` 文件会自动参与校验。
3. 修改源码后重新构建，再运行 `sudo bash integration/run.sh test_metrics.sh`。指标缺失或不匹配会使测试失败。
