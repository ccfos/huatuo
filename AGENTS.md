# HUATUO

## Communication

- Dry, concise. No flattery, no forced memes, no preamble/postamble.
- Comments explain **why**, not **what**.
- Error messages: actionable and specific. No vague "something went wrong".

## Constraints

- **Generated files are read-only.** Never hand-edit `mock_*_test.go` or `*.capnp.go` — run `make gen-build`.
- **License header required.** Every `.go` file carries the Apache 2.0 header (canonical form: `internal/storage/store.go`). New files: current year. Modified files: append current year.
- **One logical change per commit.** Keep patches focused and reviewable.
- **Commit messages must use (`feat`, `fix`, `refactor`) in `type(scope): summary` format; include a body explaining why; no `Co-authored-by:` trailers.**
- **Commit body lines must not exceed 80 characters.**
  Wrap longer text onto additional lines.

## Guidelines

- Think before coding.
- Add unit tests. Add integration tests when the component has external dependencies.
- Update docs when changing storage schemas, config options, features, or APIs.
- Surgical changes: touch what the task needs, match local style, don't refactor unrelated code.
- Define verifiable success criteria for non-trivial work; iterate until checks pass or blockers are explicit.
- Performance-sensitive paths (BPF event processing, metric loops) require benchmark justification.
- Comments, logs, and error messages: concise and focused — highlight what matters.

## Commands

```
make all                    # BPF compile + Go build + sync artifacts
make check                  # goimports + gofumpt + golangci-lint + git diff --exit-code
make import-fmt             # Format Go (goimports/gofumpt) and Shell (shfmt)
make golangci-lint          # Static analysis (requires gen-build first)
make unit                   # Unit tests with coverage
make integration            # Integration tests (requires full build)
make e2e                    # End-to-end tests (requires full build)
make gen-build              # Generate mocks (mockery) and Cap'n Proto files
make bpf-build              # Compile all BPF C sources in parallel
make vendor                 # go mod tidy + verify + vendor
make clean                  # Remove _output/, .o, mock, capnp generated files
```

Run `make check` before every commit.

## Repository Map

- `cmd/` — Binary entry points
- `core/` — Business logic (collectors, handlers, autotracing)
- `internal/` — Infrastructure (storage, BPF loader, config, logging, pod/cgroup)
- `pkg/` — Public reusable types
- `apis/` — Versioned API types
- `bpf/` — BPF C sources, headers, compiled objects
- `build/` — Build scripts and helpers
- `docs/` — User documentation
- `integration/`, `e2e/` — Test suites
- `vendor/` — Vendored Go dependencies

## Review Focus

1. **Correctness** — data races, leaks, error handling, BPF verifier compatibility.
2. **Reuse** — check if existing code already covers the need.
3. **Performance** — no allocations in hot loops; preallocate slices; pointer semantics for large structs (>80B); no range-value copies of large elements.
4. **Architecture** — respect layering; no cross-layer pollution; no new patterns without justification.
