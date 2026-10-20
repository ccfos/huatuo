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
- Update existing unit tests only; do not create new unit test files. Add integration tests for components with external dependencies.
- Update docs when changing storage schemas, config options, features, or APIs.
- Surgical changes: touch what the task needs, match local style, don't refactor unrelated code.
- Preserve existing function groups and order in shared shell helpers, including
  `integration/lib_*.sh` and `integration/config.sh`. Add related functions at the
  end of their group and independent helpers after the existing function groups;
  keep definitions before the top-level calls that use them.
- Name case-specific config writers after the test script's domain and purpose,
  e.g. `test_basic_elasticsearch_config.sh` uses `write_elasticsearch_config`.
  Writers shared across cases use their common responsibility rather than one
  caller's filename. Check naming, placement, and all references for every new
  or renamed helper before submitting.
- Define verifiable success criteria for non-trivial work; iterate until checks pass or blockers are explicit.
- Performance-sensitive paths (BPF event processing, metric loops) require benchmark justification.
- Comments, logs, and error messages: concise and focused — highlight what matters.

## Commands

```
make build                  # BPF compile + Go build + sync artifacts
make check                  # Generated files + format + lint + git diff check
make unit                   # Unit tests with coverage
make integration            # Integration tests (requires full build)
make e2e                    # End-to-end tests (requires full build)
make gen-build              # Generate BPF, OpenAPI, mocks, and Cap'n Proto files
make vendor                 # go mod tidy + verify + vendor
make clean                  # Remove _output/, .o, mock, capnp generated files
make docker-build           # Build the selected release image
make docker-clean           # Remove the selected release image
make compose-dev-up         # Build and start the development stack
make compose-dev-down       # Stop and remove the development stack
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
