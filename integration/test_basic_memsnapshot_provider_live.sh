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

# Exercise live providers, configuration, concurrency, pressure and recovery.
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
# Overlay only the test build: pause before the second remote read, after one
# successful read. The repository's generated syscall source stays untouched.
snapshot_build_read_barrier
export MEMSNAP_READ_BARRIER_ENABLED=1
overlay="${MEMSNAP_ACCEPTANCE_ARTIFACTS}/collector-overlay.json"
jq -n --arg virtual "${ROOT_DIR}/internal/memsnapshot/collector/acceptance_capture_slot_integration_test.go" \
	--arg source "${ROOT_DIR}/integration/testdata/test_basic_memsnapshot_capture_slot_linux_test.go" \
	--arg syscall "${ROOT_DIR}/vendor/golang.org/x/sys/unix/zsyscall_linux.go" --arg read_source "${read_source}" \
	'{Replace: {($virtual): $source, ($syscall): $read_source}}' > "${overlay}"
status=0
# Keep diagnostics even on failure; pipefail preserves the go test exit status.
go test -mod=vendor -overlay="${overlay}" -tags=integration -count=1 -timeout=10m -json \
	-run '^(TestSnapshotLive(Go|HotSpot|CPython|Java|CCpp)Process|TestMemsnapshotAcceptance.*)$' \
	./integration/testdata/test_basic_memsnapshot_provider_live_linux_test.go \
	| tee "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" || status=$?
# Keep collector-package white-box assertions separate from public live tests.
go test -mod=vendor -overlay="${overlay}" -tags=integration -count=1 -timeout=2m -json \
	-run '^TestCaptureSlotAcceptance$' ./internal/memsnapshot/collector \
	| tee -a "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" || status=$?
jq -r 'select(.Test != null and (.Action == "pass" or .Action == "fail" or .Action == "skip")) | "\(.Action): \(.Test)"' \
	"${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl"
jq -r '.Output // empty' "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/results.jsonl" > "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/test.log"
if grep -qiE 'level="?(warn|warning|error|panic|fatal)"?|"level":"(warn|warning|error|panic|fatal)"|panic:' "${MEMSNAP_ACCEPTANCE_ARTIFACTS}/test.log"; then
	fatal "unexpected warning/error: ${MEMSNAP_ACCEPTANCE_ARTIFACTS}/test.log"
fi
[[ ${status} -eq 0 ]] || exit "${status}"
log_info "live memsnapshot tests passed; see per-runtime skips above"
