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
ROOT="$(cd "${BASEDIR}/.." && pwd)"

# Path to huatuo-bamai binary under test.
BIN="${ROOT}/_output/bin/huatuo-bamai"
PID="/var/run/huatuo-bamai.pid"

# Temporary directory for logs and runtime artifacts.
TMPDIR="$(mktemp -d /tmp/huatuo-integration-test.XXXXXX)"

# Test fixtures and expected outputs.
FIXTURES="${ROOT}/integration/fixtures"
EXPECTED_DIR="${ROOT}/integration/fixtures/expected_metrics"

# Start the huatuo-bamai service used for integration testing.
test_setup() {
  # Verify that required binaries and test data exist before running tests.
  [[ -x "${BIN}" ]] || fatal "binary not found: ${BIN}"
  [[ -d "${EXPECTED_DIR}" ]] || fatal "expected_metrics directory not found"

  log_info "starting huatuo-bamai (mock fixture fs)"

  generate_bamai_config

  log_info "launching huatuo-bamai..."

  "${BIN}" \
    --config-dir "${TMPDIR}" \
    --config bamai.conf \
    --region "huatuo-test" \
    --sysfs "${FIXTURES}/sys" \
    --procfs "${FIXTURES}/proc" \
    --disable-kubelet \
    --disable-storage \
    > "${TMPDIR}/huatuo.log" 2>&1 &

  local pid
  pid="$(cat "${PID}")"
  
  log_info "huatuo-bamai started, pid=${pid}"
}

# Entry point that orchestrates the full integration metrics test flow.
test_metrics(){
  wait_for_metrics_ready
  fetch_prometheus_metrics
  assert_all_expected_metrics
}

# Wait until the Prometheus metrics endpoint becomes available.
wait_for_metrics_ready() {
  for _ in {1..20}; do
    if curl -sf "http://127.0.0.1:19704/metrics" >/dev/null; then
      return 0
    fi
    sleep 0.5
  done

  fatal "metrics endpoint not ready"
}

# Fetch Prometheus metrics from the running service.
fetch_prometheus_metrics() {
  curl -s "http://127.0.0.1:19704/metrics" > "${TMPDIR}/metrics.txt"
}

# Verify that all metrics defined in the expected file exist.
assert_metrics_from_file() {
  local expected_file="$1"

  missing_metrics=$(
    grep -v '^[[:space:]]*\(#\|$\)' "${expected_file}" \
      | grep -Fv -f "${TMPDIR}/metrics.txt" || true
  )

  if [[ -z "${missing_metrics}" ]]; then
    log_info "all metrics found in $(basename "${expected_file}")"
    return 0
  fi

  fatal $'missing metrics:\n'"${missing_metrics}"
}

# Verify all expected metric files and dump metrics on success.
assert_all_expected_metrics() {
  for f in "${EXPECTED_DIR}"/*.txt; do
    prefix="$(basename "$f" .txt)"

    # 1. Assert that metrics defined in the expected file are present.
    assert_metrics_from_file "${f}" || return 1

    # 2. Dump collected Prometheus metrics for this prefix after a successful assertion.
    log_info "Metrics for prefix: huatuo_bamai_${prefix}"
    grep "^huatuo_bamai_${prefix}" "${TMPDIR}/metrics.txt" || log_info "(no metrics found)"
  done
}

# Stop and clean up the huatuo-bamai service.
test_teardown() { kill -9 "$(cat "${PID}")" 2>/dev/null || true; }

# Generate bamai config used by integration tests.
generate_bamai_config() {
  log_info "generating bamai config"

  # Base config (without blacklist)
  cat > "${TMPDIR}/bamai.conf" <<'EOF'
# the blacklist for tracing and metrics
BlackList = ["softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq"]
EOF
}

# Print informational messages for integration tests.
log_info() {
  echo "[INTEGRATION TEST] $*"
}

# Print an error message and terminate the test immediately.
fatal() {
  echo "[INTEGRATION TEST][FAIL] $*" >&2
  return 1
}

