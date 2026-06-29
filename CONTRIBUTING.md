---
title: Contribute
type: docs
weight: 10
---

# Contributing to HUATUO / HUATUO 贡献指南

Thank you for your interest in contributing to HUATUO! This guide will help you get started.
感谢你对 HUATUO 的关注！本指南将帮助你快速上手。

---

## Ways to Contribute / 贡献方式

你可以通过多种方式参与 HUATUO：

- **Code / 代码** — Fix bugs, add features, improve performance / 修复 Bug、添加功能、优化性能
- **Documentation / 文档** — Improve docs, translate content, write tutorials / 完善文档、翻译内容、编写教程
- **Testing / 测试** — Write unit tests, integration tests, report bugs / 编写单元测试、集成测试、报告 Bug
- **eBPF** — Add new kernel probes, improve kernel compatibility / 添加新的内核探针、改进内核兼容性
- **Review / 审查** — Review pull requests from other contributors / 审查其他贡献者的 Pull Request

---

## Development Environment / 开发环境

### Prerequisites / 前置条件

| Tool / 工具 | Requirement / 要求 | Note / 说明 |
|------|----------|------|
| **Go** | 1.24+ | The project is written in Go / 项目主体使用 Go 编写 |
| **Linux** | Kernel 4.18+ | eBPF programs require a Linux kernel / eBPF 程序需要 Linux 内核 |
| **Clang/LLVM** | Any recent version | Required for compiling eBPF C programs / 编译 eBPF C 程序所需 |
| **Kernel headers** | linux-headers | Required for BPF compilation / BPF 编译所需 |
| **Docker** | (optional / 可选) | For containerized development / 容器化开发环境 |
| **Git** | Any recent version | For version control / 版本管理 |

### Clone the Repository / 克隆仓库

```bash
# Fork the repo on GitHub, then: / 先在 GitHub 上 Fork，然后：
git clone https://github.com/YOUR_USERNAME/huatuo.git
cd huatuo
git remote add upstream https://github.com/ccfos/huatuo.git
``

---

## Build and Test / 构建与测试

### Build / 构建

```bash
make all          # Build everything (BPF + Go) / 全部构建
make bpf-build    # Build only BPF programs / 只构建 BPF 程序
make build        # Build only Go binaries / 只构建 Go 二进制
make docker-build # Build Docker image / 构建 Docker 镜像
``

### Test / 测试

```bash
make test  # Run all tests / 运行全部测试
make unit  # Run unit tests only / 只运行单元测试
make check # Run linting and formatting checks / 运行代码检查
``

> **Note / 注意**: make test requires /etc/kubernetes/pki for E2E tests. If you don't have a K8s cluster, use make unit instead.
>
> make test 需要 /etc/kubernetes/pki 来运行 E2E 测试。如果你没有 K8s 集群，请使用 make unit。

---

## Contribution Workflow / 贡献流程

### 1. Find or Create an Issue / 找到或创建 Issue

- Check the [open issues](https://github.com/ccfos/huatuo/issues) for bugs and features
  在 [open issues](https://github.com/ccfos/huatuo/issues) 中查找 Bug 和功能需求
- If you find an unassigned issue, comment to ask for assignment
  若找到未分配的 Issue，留言请求认领
- If you have a new idea, [create an issue](https://github.com/ccfos/huatuo/issues/new/choose) first
  如果有新想法，先[创建 Issue](https://github.com/ccfos/huatuo/issues/new/choose) 讨论

### 2. Create a Branch / 创建分支

```bash
git checkout -b fix/short-description
# or: git checkout -b feat/short-description
# or: git checkout -b docs/short-description
``

Branch name prefixes / 分支命名前缀：

| Prefix / 前缀 | Purpose / 用途 |
|---------|--------|
| ix/ | Bug fixes / Bug 修复 |
| eat/ | New features / 新功能 |
| docs/ | Documentation / 文档 |
| efactor/ | Code restructuring / 代码重构 |
| 	est/ | Adding tests / 添加测试 |

### 3. Make Your Changes / 编写代码

- Keep changes focused on a single issue / 一次只解决一个问题
- Add or update tests to cover your changes / 编写或更新测试
- Run make check to ensure code style compliance / 运行 make check 检查代码风格
- Run make unit to verify tests pass / 运行 make unit 验证测试

### 4. Commit Your Changes / 提交代码

Use [conventional commits](https://www.conventionalcommits.org/):
使用 [Conventional Commits](https://www.conventionalcommits.org/) 规范：

```bash
git commit -s -m "fix(scope): brief description / 简短描述

Detailed explanation if needed. / 详细说明（可选）

Closes #issue-number

Signed-off-by: Your Name <your.email@example.com>"
``

The -s flag adds the required DCO Signed-off-by line.
-s 参数会自动添加 DCO 要求的 Signed-off-by。

### 5. Push and Create a Pull Request / 推送并发起 PR

```bash
git push origin your-branch-name
``

Then go to [ccfos/huatuo](https://github.com/ccfos/huatuo) to create a Pull Request.
然后到 [ccfos/huatuo](https://github.com/ccfos/huatuo) 创建 Pull Request。

### 6. Code Review / 代码审查

- A maintainer will review your PR / 维护者会审查你的 PR
- Address review comments by pushing new commits / 根据审查意见修改并推送新 commit
- Once approved, the maintainer will merge your PR / 通过后维护者会合入你的 PR

---

## Commit Messages / 提交信息规范

HUATUO follows [Conventional Commits](https://www.conventionalcommits.org/):
HUATUO 遵循 [Conventional Commits](https://www.conventionalcommits.org/) 规范：

``
<type>(<scope>): <description>

[optional body / 可选正文]

[optional footer / 可选脚注]
``

### Types / 类型

| Type / 类型 | Description / 说明 |
|------|-------------|
| ix | A bug fix / Bug 修复 |
| eat | A new feature / 新功能 |
| docs | Documentation changes / 文档变更 |
| 	est | Adding or updating tests / 测试相关 |
| efactor | Code restructuring without behavior change / 不改变行为的代码重构 |
| chore | Build process, dependencies, etc. / 构建、依赖等 |
| perf | Performance improvements / 性能优化 |

### Examples / 示例

``
fix(pod): preserve response body read errors in httpDoRequest
feat(bpf): add probe for kernel scheduling latency
docs(contributing): add development setup guide
test(request): verify response body is readable after doRequest
``

---

## Code Style / 代码风格

| Language / 语言 | Tool / 工具 |
|----------|--------|
| **Go** | gofumpt + goimports |
| **C (eBPF)** | clang-format (config in .clang-format) |
| **Shell** | shfmt |
| **YAML/JSON** | 2-space indent / 2 空格缩进 |

Run make check before every commit to ensure compliance.
每次提交前运行 make check 确保代码符合规范。

---

## DCO Sign-off / DCO 签名

All contributions must include a **Developer Certificate of Origin (DCO)** sign-off.
所有贡献必须包含 **开发者原产地证书 (DCO)** 签名。

Every commit must end with / 每次提交必须以以下内容结尾：

``
Signed-off-by: Your Name <your.email@example.com>
``

Use git commit -s to add this automatically.
使用 git commit -s 自动添加。

The sign-off certifies that you wrote the code or have the right to contribute it under the project's license (Apache 2.0).
签名表明你是代码的作者，或有权在此项目的 Apache 2.0 许可证下贡献此代码。

---

## Issue and PR Labels / Issue 和 PR 标签

| Label / 标签 | Description / 说明 |
|-------|-------------|
| ug | Confirmed bug / 已确认的 Bug |
| enhancement | Feature request or improvement / 功能建议或改进 |
| good-first-issue | Suitable for new contributors / 适合新贡献者 |
| help-wanted | Extra attention needed / 需要更多关注 |
| documentation | Documentation-related / 文档相关 |

---

## Community / 社区

- **GitHub Issues** — Report bugs and request features / 报告 Bug 和提功能需求
- **GitHub Discussions** — Ask questions and share ideas / 提问和分享想法
- **WeChat / 微信** — Scan the QR code in the [README](https://github.com/ccfos/huatuo) to join the group / 扫描 [README](https://github.com/ccfos/huatuo) 中的二维码加入微信群

---

**Thank you for contributing to HUATUO! / 感谢你为 HUATUO 做出贡献！**
