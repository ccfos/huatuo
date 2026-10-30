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

# Check shared framework helpers independently of product workloads and suite scheduling.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands python3 jq curl ss flock
prepare_toml_parser_tool

assert_helper_log_levels() {
	local helpers_log_format helpers_log_level helpers_log_probe helpers_log_status helpers_log_expected helpers_log_line
	# The logger quotes text fields; exercise both text variants and JSON output.
	for helpers_log_format in text quoted json json-spaced; do
		for helpers_log_level in info error panic fatal; do
			helpers_log_probe="${HUATUO_BAMAI_TEST_TMPDIR}/log-${helpers_log_format}-${helpers_log_level}.log"
			case ${helpers_log_format} in
			text) printf 'level=%s msg=probe\n' "${helpers_log_level}" > "${helpers_log_probe}" ;;
			quoted) printf 'level="%s" msg="probe"\n' "${helpers_log_level}" > "${helpers_log_probe}" ;;
			json) printf '{"level":"%s","msg":"probe"}\n' "${helpers_log_level}" > "${helpers_log_probe}" ;;
			json-spaced) printf '{"level": "%s", "msg": "probe"}\n' "${helpers_log_level}" > "${helpers_log_probe}" ;;
			esac
			helpers_log_status=0
			(assert_log_has_no_failure "${helpers_log_probe}" fixture) \
				> "${helpers_log_probe}.assertion" 2>&1 || helpers_log_status=$?
			helpers_log_expected=1
			[[ ${helpers_log_level} != info ]] || helpers_log_expected=0
			assert_eq "${helpers_log_status}" "${helpers_log_expected}" "${helpers_log_format}/${helpers_log_level}" \
				|| fatal "log assertion misclassified ${helpers_log_probe}; see ${helpers_log_probe}.assertion"
		done
	done
	for helpers_log_line in \
		'level="info" msg="nonfatal condition recovered"' \
		'{"level":"info","msg":"fatal and error are field values"}'; do
		printf '%s\n' "${helpers_log_line}" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
		assert_huatuo_bamai_log_clean
	done
	for helpers_log_line in 'panic: runtime failure' '{"level":"error","msg":"probe"}'; do
		printf '%s\n' "${helpers_log_line}" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
		helpers_log_status=0
		assert_huatuo_bamai_log_clean > "${HUATUO_BAMAI_TEST_TMPDIR}/log-rejected.log" 2>&1 || helpers_log_status=$?
		assert_eq "${helpers_log_status}" 1 "failure log must be rejected"
	done
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/saved-huatuo.log"
	helpers_log_status=0
	assert_huatuo_bamai_log_clean > "${HUATUO_BAMAI_TEST_TMPDIR}/log-missing.log" 2>&1 || helpers_log_status=$?
	assert_eq "${helpers_log_status}" 1 "missing log must fail"
	log_info "text and JSON log levels checked"
}

assert_helper_toml_values() {
	local helpers_config="${HUATUO_BAMAI_TEST_TMPDIR}/persisted.conf" helpers_case helpers_status
	local helpers_expected='{"BlackList":["dropwatch","netdev_hw"],"Runtime.CPULimitCores":1.5,"Runtime.MemoryLimitMiB":1024}'
	cp "${ROOT_DIR}/integration/testdata/test_toml_values.toml" "${helpers_config}"
	assert_toml_values "${helpers_config}" "${helpers_expected}"
	for helpers_case in comments section type value syntax; do
		case ${helpers_case} in
		comments) sed 's/^/# /' "${ROOT_DIR}/integration/testdata/test_toml_values.toml" > "${helpers_config}" ;;
		section) sed 's/\[Runtime\]/[Other]/' "${ROOT_DIR}/integration/testdata/test_toml_values.toml" > "${helpers_config}" ;;
		type) sed 's/CPULimitCores = 1.5/CPULimitCores = "1.5"/' "${ROOT_DIR}/integration/testdata/test_toml_values.toml" > "${helpers_config}" ;;
		value) sed 's/MemoryLimitMiB = 1024/MemoryLimitMiB = 1/' "${ROOT_DIR}/integration/testdata/test_toml_values.toml" > "${helpers_config}" ;;
		syntax) printf '[Runtime\n' > "${helpers_config}" ;;
		esac
		helpers_status=0
		(assert_toml_values "${helpers_config}" "${helpers_expected}") \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/toml-${helpers_case}.log" 2>&1 || helpers_status=$?
		assert_eq "${helpers_status}" 1 "${helpers_case}: invalid persisted configuration must fail"
	done
}

assert_helper_http_defaults() (
	# Match the runner's parent-to-child Bash boundary, including a non-exportable array.
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	export CURL_TIMEOUT
	bash -euc '
		source "$ROOT_DIR/integration/lib.sh"
		assert_eq "${CURL_TIMEOUT[*]}" "--connect-timeout 2 --max-time 3" "child HTTP defaults"
	'
	CURL_TIMEOUT=(--max-time 0.2)
	source "${ROOT_DIR}/integration/lib.sh"
	assert_eq "${CURL_TIMEOUT[*]}" "--max-time 0.2" "caller HTTP timeout override"
)

assert_helper_bamai_ports() (
	source "${ROOT_DIR}/integration/config.sh"
	INTEGRATION_PORT_DIR=""
	allocate_available_port() { printf '23456\n'; }
	huatuo_bamai_start() {
		assert_eq "${HUATUO_BAMAI_ADDR}" "http://127.0.0.1:23456" "serial allocated address"
		assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" '{"HTTPServer.ListenAddress":"127.0.0.1:23456"}'
	}
	integration_huatuo_bamai_start
	INTEGRATION_PORT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/ports"
	HUATUO_BAMAI_ADDR=http://127.0.0.1:24567
	write_config_bamai_default > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	huatuo_bamai_configure_default_address
	assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" '{"HTTPServer.ListenAddress":"127.0.0.1:24567"}'
	{
		write_config_bamai_http "127.0.0.1:25678"
		printf '\n[HTTPServer.Auth]\nBearerToken = "integration-node-token"\n'
	} > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	huatuo_bamai_configure_default_address
	assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" '{"HTTPServer.ListenAddress":"127.0.0.1:25678"}'
	# A prepared configuration must survive the launcher's explicit no-writer mode.
	huatuo_bamai_start() {
		assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" '{"HTTPServer.ListenAddress":"127.0.0.1:25678","HTTPServer.Auth.BearerToken":"integration-node-token"}'
	}
	integration_huatuo_bamai_start :
)

assert_helper_metric_diagnostics() (
	local helpers_pid="" helpers_port helpers_status
	trap '[[ -z "${helpers_pid}" ]] || stop_and_wait_by_pid "${helpers_pid}" 1 || true' EXIT
	helpers_port=$(allocate_available_port) || fatal "cannot allocate metrics fixture port"
	python3 "${ROOT_DIR}/integration/testdata/test_http_metrics.py" "${helpers_port}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/metrics-server.log" 2>&1 &
	helpers_pid=$!
	wait_until 5 0.05 grep -q '^ready$' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics-server.log"
	export no_proxy=127.0.0.1 NO_PROXY=127.0.0.1
	HUATUO_BAMAI_ADDR="http://127.0.0.1:${helpers_port}"
	HUATUO_BAMAI_METRICS_API="http://127.0.0.1:${helpers_port}/ok"
	printf '%s\n' "${helpers_pid}" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	huatuo_bamai_ready || fatal "readiness rejected the process owning the metrics listener"
	printf '%s\n' "${BASHPID}" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	helpers_status=0
	(huatuo_bamai_ready) > "${HUATUO_BAMAI_TEST_TMPDIR}/foreign-listener.log" 2>&1 || helpers_status=$?
	assert_eq "${helpers_status}" 1 "another process serving metrics must not satisfy readiness"
	printf '%s\n' "${helpers_pid}" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	huatuo_bamai_collect_metrics
	assert_eq "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt")" 'test_counter 7' "metrics response"
	HUATUO_BAMAI_METRICS_API="http://127.0.0.1:${helpers_port}/error"
	helpers_status=0
	huatuo_bamai_collect_metrics > /dev/null 2> "${HUATUO_BAMAI_TEST_TMPDIR}/metrics-failure.log" || helpers_status=$?
	assert_eq "${helpers_status}" 1 "HTTP error must fail metrics collection"
	assert_eq "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.status")" 'curl exit=0 HTTP status=503' "metrics HTTP diagnostics"
	assert_eq "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.response")" '{"error":"metrics unavailable"}' "metrics error body"
	CURL_TIMEOUT=(--connect-timeout 0.1 --max-time 0.1)
	HUATUO_BAMAI_METRICS_API="http://127.0.0.1:${helpers_port}/stall"
	helpers_status=0
	huatuo_bamai_collect_metrics > /dev/null 2> "${HUATUO_BAMAI_TEST_TMPDIR}/metrics-timeout.log" 2>&1 || helpers_status=$?
	assert_eq "${helpers_status}" 1 "stalled request must fail metrics collection"
	assert_eq "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.status")" 'curl exit=28 HTTP status=000' "metrics timeout diagnostics"
	[[ -s "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.curl-error" ]] || fatal "curl timeout diagnostic is empty"
	stop_and_wait_by_pid "${helpers_pid}" 1 || true
	helpers_status=0
	(huatuo_bamai_ready) > "${HUATUO_BAMAI_TEST_TMPDIR}/exited-daemon.log" 2>&1 || helpers_status=$?
	assert_eq "${helpers_status}" 1 "exited daemon must fail readiness"
	grep -q 'exited during startup' "${HUATUO_BAMAI_TEST_TMPDIR}/exited-daemon.log" \
		|| fatal "readiness did not diagnose the daemon startup exit"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	helpers_pid=""
)

assert_process_shutdown() (
	local mode=$1 expected_status=$2 signal=${3:-TERM} pid="" status=0
	local ready="${HUATUO_BAMAI_TEST_TMPDIR}/${mode}.ready"
	trap 'stop_and_wait_by_pid "${pid}" 1 || true' EXIT
	python3 - "${ready}" "${mode}" << 'PY' &
import pathlib
import signal
import sys
import time

signal.signal(signal.SIGTERM, signal.SIG_IGN if sys.argv[2] == "forced" else lambda *_: sys.exit(7))
signal.signal(signal.SIGUSR2, lambda *_: sys.exit(0))
pathlib.Path(sys.argv[1]).touch()
while True:
    time.sleep(0.01)
PY
	pid=$!
	wait_until 5 0.05 test -f "${ready}" || fatal "${mode}: child did not become ready"
	stop_and_wait_by_pid "${pid}" 1 "${signal}" || status=$?
	assert_eq "${status}" "${expected_status}" "${mode}: child exit status"
	! kill -0 "${pid}" 2> /dev/null || fatal "${mode}: child survived shutdown"
	pid=""
)

assert_helper_port_allocation() (
	local work pid i file port
	local -a pids=()
	trap cleanup_running_jobs EXIT
	work="${HUATUO_BAMAI_TEST_TMPDIR}/worker-ports"
	mkdir -p "${work}"
	ss() { echo 'LISTEN 0 128 127.0.0.1:20000 0.0.0.0:*'; }
	export INTEGRATION_PORT_BASE=20000 INTEGRATION_PORT_DIR="${work}/ports"
	for ((i = 0; i < 12; i++)); do
		allocate_available_port > "${work}/port-${i}" &
		pids+=("$!")
	done
	for pid in "${pids[@]}"; do wait "${pid}"; done
	assert_eq "$(sort -u "${work}/"port-* | wc -l)" 12 "concurrent port uniqueness"
	for file in "${work}/"port-*; do
		port=$(cat "${file}")
		((port > 20000 && port < 21000)) || fatal "worker port outside its block: ${port}"
	done
	assert_eq "$(INTEGRATION_PORT_BASE=21000 INTEGRATION_PORT_DIR="${work}/other" allocate_available_port)" 21000 "separate worker block"
	ss() { return 1; }
	if allocate_available_port; then fatal "allocation ignored failed socket query"; fi
)

assert_helper_log_levels
assert_helper_toml_values
assert_helper_http_defaults
assert_helper_bamai_ports
assert_helper_metric_diagnostics
stop_and_wait_by_pid ""
assert_process_shutdown graceful_failure 7
assert_process_shutdown forced 137
assert_process_shutdown custom_signal 0 USR2
assert_helper_port_allocation
log_info "framework logging, assertions, HTTP diagnostics, service addresses, process shutdown and port allocation passed"
