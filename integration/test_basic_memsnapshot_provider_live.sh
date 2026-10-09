#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Component integration for live providers, pressure and recovery.
# Java/Python/native dependencies are optional and report their own skipped cases.
# Container triggers and persistence remain in the separate E2E suite.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"
require_commands go jq
export MEMSNAP_ACCEPTANCE_ARTIFACTS="${HUATUO_BAMAI_TEST_TMPDIR}/memsnapshot"
export MEMSNAP_ACCEPTANCE_FIXTURE="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/go-snapshot"
mkdir -p "${MEMSNAP_ACCEPTANCE_ARTIFACTS}"
cd "${ROOT_DIR}"
go build -mod=vendor -o "${MEMSNAP_ACCEPTANCE_FIXTURE}" ./e2e/testdata/memory_threshold_snapshot_golang.go
export MEMSNAP_ACCEPTANCE_RUNTIME_FIXTURES="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/runtimes"
mkdir -p "${MEMSNAP_ACCEPTANCE_RUNTIME_FIXTURES}"
MEMSNAP_OPTIONAL_RUNTIMES=1 HUATUO_BAMAI_TEST_TMPDIR="${MEMSNAP_ACCEPTANCE_RUNTIME_FIXTURES}" snapshot_build_runtime_fixtures
# Public live acceptance uses the unmodified reader and provider implementation.
memsnapshot_test_status=0
# Keep diagnostics even on failure; pipefail preserves the go test exit status.
go test -mod=vendor -tags=integration -count=1 -timeout=10m -json \
	-run '^(TestSnapshotLive(Go|HotSpot|CPython|Java|CCpp)Process|TestMemsnapshotAcceptance.*)$' \
	./internal/memsnapshot/collector \
	| tee "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" || memsnapshot_test_status=$?
# Keep collector-package white-box assertions separate from public live tests.
go test -mod=vendor -tags=integration -count=1 -timeout=2m -json \
	-run '^TestCaptureSlotAcceptance$' ./internal/memsnapshot/collector \
	| tee -a "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" || memsnapshot_test_status=$?
# Only remote-read interruption needs syscall instrumentation. Component test
# files live in their own packages and are never injected through the overlay.
snapshot_build_read_barrier
memsnapshot_overlay="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/reader-fault-overlay.json"
jq -n --arg syscall "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" \
	--arg memsnapshot_read_source "${memsnapshot_read_source}" '{Replace:{($syscall):$memsnapshot_read_source}}' > "${memsnapshot_overlay}"
MEMSNAP_READ_BARRIER_ENABLED=1 go test -mod=vendor -overlay="${memsnapshot_overlay}" -tags=integration \
	-count=1 -timeout=3m -json -run '^TestMemsnapshotAcceptanceReadInterruption$' \
	./internal/memsnapshot/collector \
	| tee -a "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" || memsnapshot_test_status=$?
jq -r 'select(.Test != null and (.Action == "pass" or .Action == "fail" or .Action == "skip")) | "\(.Action): \(.Test)"' \
	"${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl"
jq -r '.Output // empty' "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" > "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/test.log"
assert_memsnapshot_logs "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/test.log"
[[ ${memsnapshot_test_status} -eq 0 ]] || exit "${memsnapshot_test_status}"
log_info "live memsnapshot tests passed; see per-runtime skips above"
