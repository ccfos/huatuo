#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Root of the project repository.
ROOT=$(cd ${BASEDIR}/.. && pwd)

HUATUO_BAMAI_BIN="${ROOT}/_output/bin/huatuo-bamai"
HUATUO_BAMAI_PIDFILE="/var/run/huatuo-bamai.pid"
HUATUO_TEST_TMPDIR=$(mktemp -d /tmp/huatuo-integration-test.XXXXXX)
HUATUO_TEST_FIXTURES="${ROOT}/integration/fixtures"
HUATUO_TEST_EXPECTED="${ROOT}/integration/fixtures/expected_metrics"

# Start the huatuo-bamai service used for integration testing.
test_setup() {
	[[ -x ${HUATUO_BAMAI_BIN} ]] || fatal "binary not found: ${HUATUO_BAMAI_BIN}"
	[[ -d ${HUATUO_TEST_EXPECTED} ]] || fatal "expected metrics directory not found"

	log_info "starting huatuo-bamai (mock fixture fs)"

	bamai_config

	log_info "launching huatuo-bamai..."

	${HUATUO_BAMAI_BIN} \
		--config-dir ${HUATUO_TEST_TMPDIR} \
		--config bamai.conf \
		--region "dev" \
		--procfs-prefix ${HUATUO_TEST_FIXTURES} \
		--disable-kubelet \
		--disable-storage \
		>${HUATUO_TEST_TMPDIR}/huatuo.log 2>&1 &

	sleep 1s

	log_info "huatuo-bamai started, pid=$(cat ${HUATUO_BAMAI_PIDFILE})"
}

test_teardown() {
	local exit_code=$1

	kill -9 $(cat ${HUATUO_BAMAI_PIDFILE} 2>/dev/null) 2>/dev/null || true

	# Print details on failure
	if [ "${exit_code}" -ne 0 ]; then
		log_info "the exit code: $exit_code"
		log_info "
========== HUATUO INTEGRATION TEST FAILED ================

Summary:
  - One or more expected metrics are missing.

Temporary artifacts preserved at:
  ${HUATUO_TEST_TMPDIR}

Key files:
  - metrics.txt
  - huatuo.log
  - bamai.conf

=========================================================
"
	fi
}

test_metrics() {
	wait_and_fetch_metrics
	check_procfs_metrics
	# ...
}

wait_and_fetch_metrics() {
	for _ in {1..20}; do
		if curl -sf "localhost:19704/metrics" >"${HUATUO_TEST_TMPDIR}/metrics.txt"; then
			return 0
		fi
		sleep 0.5
	done

	fatal "metrics endpoint not ready"
}

# Verify all expected metric files and dump metrics on success.
check_procfs_metrics() {
	for f in "${HUATUO_TEST_EXPECTED}"/*.txt; do
		prefix="$(basename "$f" .txt)"

		check_metrics_from_file "${f}"

		log_info "metrics prefix: huatuo_bamai_${prefix}"
		grep "^huatuo_bamai_${prefix}" "${HUATUO_TEST_TMPDIR}/metrics.txt" || log_info "(no metrics found)"
	done
}

check_metrics_from_file() {
	local file="$1"

	missing_metrics=$(
		grep -v '^[[:space:]]*\(#\|$\)' "${file}" |
			grep -Fv -f "${HUATUO_TEST_TMPDIR}/metrics.txt" || true
	)

	if [[ -z "${missing_metrics}" ]]; then
		return
	fi

	log_info "the missing metrics:"
	log_info "${missing_metrics}"
	log_info "the metrics file ${HUATUO_TEST_TMPDIR}/metrics.txt:"
	log_info "$(cat ${HUATUO_TEST_TMPDIR}/metrics.txt)"
	exit 1
}

# Generate bamai config used by integration tests.
bamai_config() {
	cat >"${HUATUO_TEST_TMPDIR}/bamai.conf" <<'EOF'
# the blacklist for tracing and metrics
BlackList = ["softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq"]
EOF
}

log_info() {
	echo "[INTEGRATION TEST] $*"
}

fatal() {
	echo "[INTEGRATION TEST][FAIL] $*" >&2
	exit 1
}
