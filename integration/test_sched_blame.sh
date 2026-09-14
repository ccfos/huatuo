#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
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

# Exercise SchedBlame raw perf decoding, queueing, attribution, and reporting
# with deterministic records. Scheduler-hook behavior is outside this test.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

command -v go > /dev/null || skip "go command is not installed"

readonly SCHED_BLAME_TEST_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/sched-blame.log"
readonly SCHED_BLAME_GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly SCHED_BLAME_GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"

mkdir -p "${SCHED_BLAME_GO_CACHE_DIR}" "${SCHED_BLAME_GO_TMP_DIR}"

if ! GOCACHE="${SCHED_BLAME_GO_CACHE_DIR}" \
	GOTMPDIR="${SCHED_BLAME_GO_TMP_DIR}" \
	go test -mod=vendor -tags=integration ./core/autotracing \
	-run '^TestSchedBlamePipelineIntegration$' -count=1 \
	> "${SCHED_BLAME_TEST_LOG}" 2>&1; then
	sed -n '1,240p' "${SCHED_BLAME_TEST_LOG}" >&2
	fatal "sched-blame userspace pipeline integration test failed"
fi

log_info "sched-blame userspace pipeline verified"
