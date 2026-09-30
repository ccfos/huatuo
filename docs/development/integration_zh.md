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

在 Linux 环境的仓库根目录执行。运行器要求 **root（EUID 为 0）**，并使用 `unshare --uts --mount` 和挂载；许多用例还会加载 BPF 程序。受限容器中的 root 仍可能缺少 `CAP_SYS_ADMIN` 或内核要求的 BPF/perf 权限。各测试还会检查内核 tracepoint、PMU 访问、语言运行时和辅助命令等前提。

直接调用运行器前，先构建二进制、BPF 对象和配置文件：

```bash
make build
sudo bash integration/run.sh
```

运行器只检查 `_output` 目录是否存在，不逐个检查二进制和 BPF 产物。目录存在不代表构建完整，缺失产物会在实际使用时导致失败。

也可以在具备构建工具链的 root 环境运行 `make integration`。该目标先构建，再调用同一个运行器；非 root 执行时可能完成构建后跳过整套测试。

### 选择测试与重复执行

不带参数时运行全部 `integration/test_*.sh`。第一个参数为单个测试的文件名（不能是路径），第二个参数为正整数重复次数，默认为 1：

```bash
sudo bash integration/run.sh test_metrics.sh
sudo bash integration/run.sh test_metrics_exclude_filter.sh 10
```

两套测试共用此运行器，默认运行 integration。`e2e/run.sh` 转发到 `--suite e2e`，`make e2e` 入口继续可用：

```bash
sudo bash integration/run.sh --suite e2e
sudo bash integration/run.sh --suite e2e test_metrics.sh 2
sudo bash e2e/run.sh test_metrics.sh 2
```

两套测试都在独立的 UTS、mount namespace 中运行，hostname 为 `huatuo-dev`，挂载传播设为 private。integration 由用例启动服务；e2e 在每例之前启动默认 bamai，结束时停止服务并检查日志。

### 如何判断结果

- 缺少 root 权限或所需的 namespace 命令（`unshare`、`mount`）时，运行器将所选执行全部计为 SKIP，随后以 **0 退出，但不会运行任何测试**。
- 单个测试使用 `skip` 输出原因并以 **77** 退出，仍会执行 `EXIT` 清理。运行器将 0 记为 PASS、77 记为 SKIP，其他状态记为 FAIL；清理失败也记为 FAIL。
- 只有成功和跳过时，运行器返回 0。应检查汇总和跳过原因，不能仅凭零退出码认定全部测试已执行。
- 测试失败时停止后续用例。运行器会停止相关服务、打印诊断文本、保留失败的临时工作目录，并输出截至当前的统计。
- integration 成功或跳过后删除工作目录，e2e 保留工作目录。指标固定数据测试会输出已检查的指标前缀及对应指标行。

每次执行都单独计数，包括重复运行，例如：

```text
summary: total=5 passed=3 skipped=2 failed=0
```

### 检查用例前提

使用同一个函数检查 PATH 中的命令或可执行文件路径，缺失时跳过：

```bash
require_commands jq curl ss
require_commands "${PROFILER_TOOL_DIR}/bin/asprof"
```

证书等输入文件使用 `require_readable` 检查，不可读时输出具体路径并跳过：

```bash
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
```

### 指标固定数据测试

`test_metrics.sh` 使用真实的 `huatuo-bamai` 二进制和模拟的 `/proc`、`/sys` 数据。它生成临时配置、启动服务、等待 `/metrics`，然后对照预期指标文件校验响应。这减少了相应指标输入对宿主环境的依赖，但不会免除运行器的命名空间和权限要求，也不代表其他测试没有环境前提。

扩展该测试时：

1. 在 `integration/fixtures/` 下新增或更新数据，保持内核文件系统的目录结构。
2. 在 `integration/fixtures/expected_metrics/*.txt` 中添加预期指标行。非空、非注释行必须出现在 `/metrics` 输出中；新增的 `.txt` 文件会自动参与校验。
3. 修改源码后重新构建，再运行 `sudo bash integration/run.sh test_metrics.sh`。指标缺失或不匹配会使测试失败。
