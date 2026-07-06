---
title: 源码贡献
type: docs
weight: 1
---

# HUATUO 贡献指南

感谢你对 HUATUO 的关注！本指南将帮助你快速上手。

---

## 贡献方式

你可以通过多种方式参与 HUATUO：

- **代码** — 修复 Bug、添加功能、优化性能
- **文档** — 完善文档、翻译内容、编写教程
- **测试** — 编写单元测试、集成测试、报告 Bug
- **eBPF** — 添加新的内核探针、改进内核兼容性
- **审查** — 审查其他贡献者的 Pull Request

---

## 开发环境

### 前置条件

| 工具 | 要求 | 说明 |
|------|------|------|
| **Go** | 1.24+ | 项目主体使用 Go 编写 |
| **Linux** | 内核 4.18+ | eBPF 程序需要 Linux 内核 |
| **Clang/LLVM** | 任意较新版本 | 编译 eBPF C 程序所需 |
| **Kernel headers** | linux-headers | BPF 编译所需 |
| **Docker** | (可选) | 容器化开发环境 |
| **Git** | 任意较新版本 | 版本管理 |

### 克隆仓库

```bash
# 先在 GitHub 上 Fork 仓库，然后：
git clone https://github.com/YOUR_USERNAME/huatuo.git
cd huatuo
git remote add upstream https://github.com/ccfos/huatuo.git
```

---

## 构建与测试

### 构建

```bash
make all          # 全部构建（BPF + Go）
make bpf-build    # 只构建 BPF 程序
make build        # 只构建 Go 二进制文件
make docker-build # 构建 Docker 镜像
```

### 测试

```bash
make test  # 运行全部测试
make unit  # 只运行单元测试
make check # 运行代码风格和格式化检查
```

> **注意**：`make test` 需要 `/etc/kubernetes/pki` 来运行 E2E 测试。如果没有 K8s 集群，请使用 `make unit`。

---

## 贡献流程

### 1. 找到或创建 Issue

- 在 [open issues](https://github.com/ccfos/huatuo/issues) 中查找 Bug 和功能需求
- 若找到未分配的 Issue，留言请求认领
- 如果有新想法，先[创建 Issue](https://github.com/ccfos/huatuo/issues/new/choose) 讨论

### 2. 创建分支

```bash
git checkout -b fix/short-description
# 或: git checkout -b feat/short-description
# 或: git checkout -b docs/short-description
```

分支命名前缀：

| 前缀 | 用途 |
|------|------|
| `fix/` | Bug 修复 |
| `feat/` | 新功能 |
| `docs/` | 文档 |
| `refactor/` | 代码重构 |
| `test/` | 添加测试 |

### 3. 编写代码

- 一次只解决一个问题
- 编写或更新与变更对应的测试
- 运行 `make check` 检查代码风格
- 运行 `make unit` 验证测试通过

### 4. 提交代码

使用 [Conventional Commits](https://www.conventionalcommits.org/) 规范：

```bash
git commit -s -m "fix(scope): 简短描述

详细说明（可选）

Closes #issue-number

Signed-off-by: Your Name <your.email@example.com>"
```

`-s` 参数会自动添加 DCO 要求的 `Signed-off-by` 行。

### 5. 推送并发起 PR

```bash
git push origin your-branch-name
```

然后到 [ccfos/huatuo](https://github.com/ccfos/huatuo) 创建 **Draft** Pull Request。准备就绪后点击 **Ready for review** 请求审阅。

### 6. 代码审查

- 维护者会审查你的 PR
- 根据审查意见修改并推送新 commit
- 修改期间可将 PR 设为 Draft，完成后再次点击 **Ready for review**
- 通过后维护者会合入你的 PR

---

## 提交信息规范

HUATUO 遵循 [Conventional Commits](https://www.conventionalcommits.org/) 规范：

```text
<type>(<scope>): <description>

[可选正文]

[可选脚注]
```

### 类型

| 类型 | 说明 |
|------|------|
| `fix` | Bug 修复 |
| `feat` | 新功能 |
| `docs` | 文档变更 |
| `test` | 测试相关 |
| `refactor` | 不改变行为的代码重构 |
| `chore` | 构建、依赖等 |
| `perf` | 性能优化 |

### 示例

```text
fix(pod): preserve response body read errors in httpDoRequest
feat(bpf): add probe for kernel scheduling latency
docs(contributing): add development setup guide
test(request): verify response body is readable after doRequest
```

---

## 代码风格

| 语言 | 工具 |
|------|------|
| **Go** | `gofumpt` + `goimports` |
| **C (eBPF)** | `clang-format`（配置见 `.clang-format`） |
| **Shell** | `shfmt` |
| **YAML/JSON** | 2 空格缩进 |

每次提交前运行 `make check` 确保代码符合规范。

---

## DCO 签名

所有贡献必须包含 **开发者原产地证书 (DCO)** 签名。

每次提交必须以以下内容结尾：

```bash
Signed-off-by: Your Name <your.email@example.com>
```

使用 `git commit -s` 自动添加。

签名表明你是代码的作者，或有权在此项目的 Apache 2.0 许可证下贡献此代码。

---

## 社区

- **GitHub Issues** — 报告 Bug 和提功能需求
- **GitHub Discussions** — 提问和分享想法
- **微信** — 扫描 [README](https://github.com/ccfos/huatuo) 中的二维码加入微信群

---

**感谢你为 HUATUO 做出贡献！**
