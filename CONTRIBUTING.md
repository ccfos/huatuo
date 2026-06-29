---
title: Contribute
type: docs
weight: 10
---

# Contributing to HUATUO

Thank you for your interest in contributing to HUATUO! This guide will help you get started.

## Table of Contents

- [Ways to Contribute](#ways-to-contribute)
- [Development Environment](#development-environment)
- [Build and Test](#build-and-test)
- [Contribution Workflow](#contribution-workflow)
- [Commit Messages](#commit-messages)
- [Code Style](#code-style)
- [DCO Sign-off](#dco-sign-off)
- [Issue and PR Labels](#issue-and-pr-labels)
- [Community](#community)

---

## Ways to Contribute

There are many ways to contribute to HUATUO:

- **Code** — Fix bugs, add features, improve performance
- **Documentation** — Improve docs, translate content, write tutorials
- **Testing** — Write unit tests, integration tests, report bugs
- **eBPF** — Add new kernel probes, improve kernel compatibility
- **Review** — Review pull requests from other contributors

---

## Development Environment

### Prerequisites

- **Go 1.24+** — The project requires Go 1.24 or later.
- **Linux** — eBPF programs require a Linux kernel. WSL2 or a Linux VM is fine for development.
- **Kernel headers** — Required for BPF compilation (`linux-headers` package).
- **Clang/LLVM** — Required for compiling eBPF C programs.
- **Docker** (optional) — For containerized development and testing.
- **Git** — For version control.

### Clone the Repository

```bash
# Fork the repository on GitHub, then:
git clone https://github.com/YOUR_USERNAME/huatuo.git
cd huatuo
git remote add upstream https://github.com/ccfos/huatuo.git
```

---

## Build and Test

### Build

```bash
# Build everything (BPF programs + Go binaries)
make all

# Build only BPF programs
make bpf-build

# Build only Go binaries
make build

# Build Docker image
make docker-build
```

### Test

```bash
# Run all tests
make test

# Run unit tests only
make unit

# Run linting and formatting checks
make check
```

> **Note**: `make test` requires `/etc/kubernetes/pki` for E2E tests. If you don't have a K8s cluster, use `make unit` instead.

---

## Contribution Workflow

### 1. Find or Create an Issue

- Check the [open issues](https://github.com/ccfos/huatuo/issues) for bugs and features
- If you find an unassigned issue you'd like to work on, comment to ask for assignment
- If you have a new idea, [create an issue](https://github.com/ccfos/huatuo/issues/new/choose) first to discuss it

### 2. Create a Branch

```bash
git checkout -b fix/short-description
# or: git checkout -b feat/short-description
# or: git checkout -b docs/short-description
```

Use a descriptive branch name with a prefix:
- `fix/` — Bug fixes
- `feat/` — New features
- `docs/` — Documentation changes
- `refactor/` — Code restructuring
- `test/` — Adding tests

### 3. Make Your Changes

- Keep changes focused on a single issue
- Add or update tests to cover your changes
- Run `make check` to ensure code style compliance
- Run `make unit` to verify tests pass

### 4. Commit Your Changes

Use [conventional commits](https://www.conventionalcommits.org/):

```bash
git commit -s -m "fix(scope): brief description

Detailed explanation if needed.

Closes #issue-number

Signed-off-by: Your Name <your.email@example.com>"
```

The `-s` flag adds the required `Signed-off-by` line for DCO compliance.

### 5. Push and Create a Pull Request

```bash
git push origin your-branch-name
```

Then go to [ccfos/huatoo](https://github.com/ccfos/huatuo) and create a Pull Request.

### 6. Code Review

- A maintainer will review your PR
- Address review comments by pushing new commits to your branch
- Once approved, the maintainer will merge your PR

---

## Commit Messages

HUATUO follows the [Conventional Commits](https://www.conventionalcommits.org/) specification:

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

### Types

| Type | Description |
|------|-------------|
| `fix` | A bug fix |
| `feat` | A new feature |
| `docs` | Documentation changes |
| `test` | Adding or updating tests |
| `refactor` | Code restructuring without behavior change |
| `chore` | Build process, dependencies, etc. |
| `perf` | Performance improvements |

### Examples

```
fix(pod): preserve response body read errors in httpDoRequest
feat(bpf): add probe for kernel scheduling latency
docs(contributing): add development setup guide
test(request): verify response body is readable after doRequest
```

---

## Code Style

- **Go** — Formatted with `gofumpt` and `goimports`. Run `make check` to verify.
- **C (eBPF)** — Formatted with `clang-format`. Configuration in `.clang-format`.
- **Shell** — Formatted with `shfmt`.
- **YAML/JSON** — Consistent indentation (2 spaces for YAML).

Run `make check` before every commit to ensure compliance.

---

## DCO Sign-off

All contributions must include a **Developer Certificate of Origin (DCO)** sign-off.

Every commit must end with:

```
Signed-off-by: Your Name <your.email@example.com>
```

Use `git commit -s` to add this automatically.

The sign-off certifies that you wrote the code or have the right to contribute it under the project's license (Apache 2.0).

---

## Issue and PR Labels

| Label | Description |
|-------|-------------|
| `bug` | Confirmed bug |
| `enhancement` | Feature request or improvement |
| `good-first-issue` | Suitable for new contributors |
| `help-wanted` | Extra attention needed |
| `documentation` | Documentation-related |

---

## Community

- **GitHub Issues** — Report bugs and request features
- **GitHub Discussions** — Ask questions and share ideas
- **WeChat** — Scan the QR code in the [README](https://github.com/ccfos/huatuo) to join the group

---

**Thank you for contributing to HUATUO!**
