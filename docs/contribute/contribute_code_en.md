---
title: Code Contributions
type: docs
weight: 1
---

This guide covers the development environment, validation requirements, and contribution workflow. Keep each change focused on one problem so it is easy to review and revert.

## Ways to Contribute

You can contribute to HUATUO in several ways:

- **Code**: fix bugs, add features, or improve performance.
- **Documentation**: improve documentation, translate content, or write tutorials.
- **Testing**: write unit and integration tests or report bugs.
- **eBPF**: add kernel probes or improve kernel compatibility.
- **Review**: review other contributors' pull requests.

## Development Environment

### Prerequisites

| Tool | Requirement | Purpose |
|------|------|------|
| **Go** | As specified in `go.mod` (currently 1.24.3) | Main implementation language |
| **Linux** | Kernel 4.18+ | Required for eBPF programs |
| **Clang/LLVM** | A recent version | Compile eBPF C programs |
| **Kernel headers** | linux-headers | Required for BPF compilation |
| **Docker** | Optional | Containerized development environment |
| **Git** | A recent version | Version control |

### Clone the Repository

```bash
# Fork the repository on GitHub first, then:
git clone https://github.com/YOUR_USERNAME/huatuo.git
cd huatuo
git remote add upstream https://github.com/ccfos/huatuo.git
```

## Build and Test

### Build

```bash
make build        # Build BPF programs and Go binaries
make gen-build    # Regenerate BPF artifacts and generated Go code
make docker-build # Build the Docker image
```

### Run the Development Stack

Build a development image from the current source and start the collector, API Server, Elasticsearch, Prometheus, and Grafana:

```bash
make compose-dev-up
```

Stop the environment and remove its containers, volumes, and local development image:

```bash
make compose-dev-down
```

### Test

```bash
make test  # Run all tests
make unit  # Run unit tests only
make check # Run code style and formatting checks
```

> **Note:** `make test` requires `/etc/kubernetes/pki` for E2E tests. Without a Kubernetes cluster, use `make unit`.

To test or debug on other distributions without a local VM, see [GitHub Actions VM Debugging](/docs/development/debugging_en.md#vm-debugging).

## Contribution Workflow

### 1. Find or Create an Issue

- Browse [open issues](https://github.com/ccfos/huatuo/issues) for bugs and feature requests.
- To work on an unassigned issue, leave a comment asking to take it.
- For a new idea, [create an issue](https://github.com/ccfos/huatuo/issues/new/choose) to discuss it first.

### 2. Create a Branch

```bash
git checkout -b fix/short-description
# Or: git checkout -b feat/short-description
# Or: git checkout -b docs/short-description
```

Branch prefixes:

| Prefix | Purpose |
|------|------|
| `fix/` | Bug fixes |
| `feat/` | New features |
| `docs/` | Documentation |
| `refactor/` | Code refactoring |
| `test/` | Additional tests |

### 3. Write Code

- Address one problem at a time.
- Update existing unit tests; add integration tests for components with external dependencies.
- Update documentation when changing configuration, interfaces, storage structures, or features.
- Run `make check` to check code style.
- Run `make unit` to verify the tests pass.

### 4. Commit Changes

Use [Conventional Commits](https://www.conventionalcommits.org/):

```bash
git commit -s -m "fix(scope): short summary

Detailed explanation (optional)

Closes #issue-number

Signed-off-by: Your Name <your.email@example.com>"
```

The `-s` flag adds the `Signed-off-by` line required by the DCO.

### 5. Push and Open a PR

```bash
git push origin your-branch-name
```

Create a **Draft** pull request in [ccfos/huatuo](https://github.com/ccfos/huatuo). When ready, click **Ready for review**.

### 6. Code Review

- Maintainers review your PR.
- Address feedback and push new commits.
- You can mark the PR as Draft while revising it, then click **Ready for review** again.
- Maintainers merge the PR after approval.

## Commit Messages

Use `type(scope): summary` and follow the repository's `AGENTS.md`. The body must explain why the change is needed, with no line exceeding 80 characters. Each commit must contain one logical change and no `Co-authored-by:` trailer.

```text
<type>(<scope>): <description>

Explain why the change is needed (no more than 80 characters per line)

[Optional footer]
```

### Types

| Type | Description |
|------|------|
| `fix` | Bug fix |
| `feat` | New feature |
| `refactor` | Code restructuring without behavior changes |

### Examples

```text
fix(pod): preserve response body read errors in httpDoRequest
feat(bpf): add probe for kernel scheduling latency
fix(docs): clarify development setup requirements
fix(request): cover response body reads after doRequest
```

## Code Style

| Language | Tool |
|------|------|
| **Go** | `gofumpt` + `goimports` |
| **C (eBPF)** | `clang-format` (configured in `.clang-format`) |
| **Shell** | `shfmt` |
| **YAML/JSON** | 2-space indentation |

Run `make check` before every commit to verify compliance.

## DCO Sign-off

All contributions require a **Developer Certificate of Origin (DCO)** sign-off.

Each commit must end with:

```bash
Signed-off-by: Your Name <your.email@example.com>
```

Use `git commit -s` to add it automatically.

The sign-off confirms that you authored the code or have the right to contribute it under the project's Apache 2.0 license.

## Community

- **GitHub Issues**: report bugs and request features.
- **GitHub Discussions**: ask questions and share ideas.
- **WeChat**: scan the QR code in the [README](https://github.com/ccfos/huatuo) to join the group.
