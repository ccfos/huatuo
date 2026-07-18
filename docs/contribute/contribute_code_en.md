---
title: Code Contributions
type: docs
weight: 1
---

# Contributing to HUATUO

Thank you for your interest in contributing to HUATUO! This guide will help you get started.

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

| Tool | Requirement | Note |
|------|----------|------|
| **Go** | 1.24+ | The project is written in Go |
| **Linux** | Kernel 4.18+ | eBPF programs require a Linux kernel |
| **Clang/LLVM** | Any recent version | Required for compiling eBPF C programs |
| **Kernel headers** | linux-headers | Required for BPF compilation |
| **Docker** | (optional) | For containerized development |
| **Git** | Any recent version | For version control |

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
make all          # Build everything (BPF + Go)
make bpf-build    # Build only BPF programs
make build        # Build only Go binaries
make docker-build # Build Docker image
```

### Full Container Integration

Build the development image from the current source and start the collector,
API Server, Elasticsearch, Prometheus, and Grafana:

```bash
make compose-dev-up
```

Stop the environment and remove its containers, volumes, and local development
image:

```bash
make compose-dev-down
```

### Test

```bash
make test  # Run all tests
make unit  # Run unit tests only
make check # Run linting and formatting checks
```

> **Note**: `make test` requires `/etc/kubernetes/pki` for E2E tests. If you don't have a K8s cluster, use `make unit` instead.

---

## Contribution Workflow

### 1. Find or Create an Issue

- Check the [open issues](https://github.com/ccfos/huatuo/issues) for bugs and features
- If you find an unassigned issue, comment to ask for assignment
- If you have a new idea, [create an issue](https://github.com/ccfos/huatuo/issues/new/choose) first

### 2. Create a Branch

```bash
git checkout -b fix/short-description
# or: git checkout -b feat/short-description
# or: git checkout -b docs/short-description
```

Branch name prefixes:

| Prefix | Purpose |
|---------|--------|
| `fix/` | Bug fixes |
| `feat/` | New features |
| `docs/` | Documentation |
| `refactor/` | Code restructuring |
| `test/` | Adding tests |

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

The `-s` flag adds the required DCO `Signed-off-by` line.

### 5. Push and Create a Pull Request

```bash
git push origin your-branch-name
```

Then go to [ccfos/huatuo](https://github.com/ccfos/huatuo) and create a **draft** Pull Request. When ready for review, click **Ready for review**.

### 6. Code Review

- A maintainer will review your PR
- Address review comments by pushing new commits
- Once approved, the maintainer will merge your PR

---

## Commit Messages

HUATUO follows [Conventional Commits](https://www.conventionalcommits.org/):

```text
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

```text
fix(pod): preserve response body read errors in httpDoRequest
feat(bpf): add probe for kernel scheduling latency
docs(contributing): add development setup guide
test(request): verify response body is readable after doRequest
```

---

## Code Style

| Language | Tool |
|----------|--------|
| **Go** | `gofumpt` + `goimports` |
| **C (eBPF)** | `clang-format` (config in `.clang-format`) |
| **Shell** | `shfmt` |
| **YAML/JSON** | 2-space indent |

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

## Community

- **GitHub Issues** — Report bugs and request features
- **GitHub Discussions** — Ask questions and share ideas
- **WeChat** — Scan the QR code in the [README](https://github.com/ccfos/huatuo) to join the group

---

**Thank you for contributing to HUATUO!**
