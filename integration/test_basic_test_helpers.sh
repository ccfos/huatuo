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

assert_native_pid_prerequisite() (
	source "${ROOT_DIR}/integration/lib_profiler.sh"
	local container_status namespace status expected output
	output="${HUATUO_BAMAI_TEST_TMPDIR}/native-pid-prerequisite.log"
	for container_status in 0 1; do
		is_container() { return "${container_status}"; }
		for namespace in host isolated unavailable; do
			stat() {
				case ${namespace} in
				host) printf '4026531836\n' ;;
				isolated) printf '4026533000\n' ;;
				unavailable) return 1 ;;
				esac
			}
			status=0
			(
				require_native_pid_profiling
				printf 'collection reached\n'
			) > "${output}" 2>&1 || status=$?
			expected=77
			[[ ${namespace} != host ]] || expected=0
			assert_eq "${status}" "${expected}" "container=${container_status} namespace=${namespace}: native PID prerequisite"
			if [[ ${expected} == 0 ]]; then
				grep -Fq 'collection reached' "${output}" || fatal "host PID namespace was skipped"
			else
				if [[ ${namespace} == isolated ]]; then
					grep -Fq 'requires the host PID namespace' "${output}" || fatal "PID namespace skip reason is missing"
				else
					grep -Fq 'cannot inspect PID namespace' "${output}" || fatal "PID namespace inspection failure reason is missing"
				fi
				! grep -Fq 'collection reached' "${output}" || fatal "native PID collection ran after skip"
			fi
		done
	done
)

assert_profiler_bpf_prerequisite() (
	source "${ROOT_DIR}/integration/lib_profiler.sh"
	local mode fixture_caps hard_limit expected reason status output
	output="${HUATUO_BAMAI_TEST_TMPDIR}/profiler-bpf-prerequisite.log"
	for mode in admin modern missing_bpf missing_perfmon missing_memlock resource; do
		fixture_caps=$(((1 << 39) | (1 << 38)))
		hard_limit=unlimited expected=0 reason=''
		case ${mode} in
		admin) fixture_caps=$((1 << 21)) ;;
		missing_bpf) fixture_caps=$((1 << 38)) expected=77 reason=CAP_BPF ;;
		missing_perfmon) fixture_caps=$((1 << 39)) expected=77 reason=CAP_PERFMON ;;
		missing_memlock) hard_limit=64 expected=77 reason=CAP_SYS_RESOURCE ;;
		resource) hard_limit=64 fixture_caps=$((fixture_caps | (1 << 24))) ;;
		esac
		awk() { printf '%x\n' "${fixture_caps}"; }
		ulimit() { printf '%s\n' "${hard_limit}"; }
		status=0
		(
			require_profiler_bpf_sampling
			printf 'collection reached\n'
		) > "${output}" 2>&1 || status=$?
		assert_eq "${status}" "${expected}" "${mode}: profiler BPF prerequisite exit status"
		if [[ ${expected} == 0 ]]; then
			grep -Fq 'collection reached' "${output}" || fatal "${mode}: capable profiler was skipped"
		else
			grep -Fq "${reason}" "${output}" || fatal "${mode}: missing capability diagnostic"
			! grep -Fq 'collection reached' "${output}" || fatal "${mode}: profiler ran after skip"
		fi
	done
)

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
	# Only exercise this fallback when the guest has the toml module.
	if python3 -c 'import toml' > /dev/null 2>&1; then (
		# Older guests use the toml package when neither stdlib nor tomli is available.
		python3() {
			if [[ $1 == -c ]]; then
				command python3 -c "import sys; sys.modules['tomllib']=None; sys.modules['tomli']=None; ${2}"
			else
				{
					printf 'import sys\nsys.modules["tomllib"]=None\nsys.modules["tomli"]=None\n'
					cat
				} | command python3 "$@"
			fi
		}
		assert_toml_values "${helpers_config}" "${helpers_expected}"
	); fi
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

assert_helper_config_fragments() (
	source "${ROOT_DIR}/integration/config.sh"
	local config="${HUATUO_BAMAI_TEST_TMPDIR}/fragments.conf" all selected expected
	write_config_blacklist_except > "${config}"
	all=$(read_toml_json "${config}" | jq -c '.BlackList | sort')
	write_config_blacklist_except cpu_stat xfs cpu_stat > "${config}"
	selected=$(read_toml_json "${config}" | jq -c '.BlackList | sort')
	expected=$(jq -cn --argjson all "${all}" '$all - ["cpu_stat", "xfs"]')
	assert_eq "${selected}" "${expected}" "collector exceptions preserve unrelated exclusions"
	assert_eq "$(jq -c 'unique' <<< "${all}")" "${all}" "collector blacklist has no duplicates"

	{
		write_config_bamai_http "127.0.0.1:12345"
		write_config_bamai_auth "test-token"
		write_config_bamai_kubelet 10251 "/tmp/client cert" "/tmp/client key"
		write_config_storage_local "/tmp/test events"
	} > "${config}"
	assert_toml_values "${config}" '{
		"HTTPServer.ListenAddress":"127.0.0.1:12345",
		"HTTPServer.Auth.BearerToken":"test-token",
		"Pod.KubeletReadOnlyPort":0,
		"Pod.KubeletAuthorizedPort":10251,
		"Pod.KubeletClientCertPath":"/tmp/client cert,/tmp/client key",
		"Storage.LocalFile.Path":"/tmp/test events"
	}'

	local CPU_STAT_PORT=12345 CPU_STAT_KUBELET_PORT=10251
	local KUBELET_CERT="/tmp/client cert" KUBELET_KEY="/tmp/client key" cpu_stat_disabled
	for cpu_stat_disabled in false true; do
		write_config_bamai_cpu_stat > "${config}"
		selected=$(read_toml_json "${config}" | jq -c '.BlackList | sort')
		expected=${all}
		if [[ ${cpu_stat_disabled} == false ]]; then
			expected=$(jq -cn --argjson all "${all}" '$all - ["cpu_stat"]')
		fi
		assert_eq "${selected}" "${expected}" "cpu_stat disabled=${cpu_stat_disabled}"
		assert_toml_values "${config}" '{
			"HTTPServer.ListenAddress":"127.0.0.1:12345",
			"HTTPServer.Auth.BearerToken":"integration-node-token",
			"Pod.KubeletAuthorizedPort":10251,
			"Pod.KubeletClientCertPath":"/tmp/client cert,/tmp/client key"
		}'
	done
)

assert_helper_bamai_ports() (
	source "${ROOT_DIR}/integration/config.sh"
	unset HUATUO_BAMAI_DEFAULT_ADDR INTEGRATION_PORT_DIR INTEGRATION_PORT_BASE
	local first next address expected
	huatuo_bamai_start() { :; }
	HUATUO_BAMAI_ADDR=""
	write_config_bamai_runtime_limits 0.75 1.25 512 > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	integration_huatuo_bamai_start :
	assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" '{"Runtime.StartupCPULimitCores":0.75,"Runtime.CPULimitCores":1.25,"Runtime.MemoryLimitMiB":512}'
	integration_huatuo_bamai_start
	first=${HUATUO_BAMAI_ADDR}
	assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "{\"HTTPServer.ListenAddress\":\"${first#http://}\"}"
	integration_huatuo_bamai_start
	assert_eq "${HUATUO_BAMAI_ADDR}" "${first}" "restart preserves the execution address"
	next=$(allocate_available_port)
	[[ http://127.0.0.1:${next} != "${first}" ]] || fatal "unbound service ports were reused"
	for address in "127.0.0.1:${next}" "0.0.0.0:${next}" ":${next}" "[::]:${next}" "[::1]:${next}"; do
		{
			write_config_bamai_http "${address}"
			printf '\n[HTTPServer.Auth]\nBearerToken = "integration-node-token"\n'
		} > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		integration_huatuo_bamai_start :
		case ${address} in
		\[*) expected="http://[::1]:${next}" ;;
		*) expected="http://127.0.0.1:${next}" ;;
		esac
		assert_eq "${HUATUO_BAMAI_ADDR}" "${expected}" "explicit listener client address"
		assert_eq "${HUATUO_BAMAI_METRICS_API}" "${expected}/metrics" "explicit listener metrics URL"
		assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "{\"HTTPServer.ListenAddress\":\"${address}\",\"HTTPServer.Auth.BearerToken\":\"integration-node-token\"}"
	done
	integration_huatuo_bamai_start write_config_bamai_http
	assert_eq "${HUATUO_BAMAI_ADDR}" "${first}" "implicit HTTP writer restores the execution address"
	assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "{\"HTTPServer.ListenAddress\":\"${first#http://}\"}"
	local header
	for header in '[HTTPServer] # keep authentication' '["HTTPServer"]' "['HTTPServer']" 'HTTPServer = {Auth = {BearerToken = "token"}}'; do
		printf '%s\n' "${header}" > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		if [[ $header != HTTPServer* ]]; then
			printf '[HTTPServer.Auth]\nBearerToken="token"\n' >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		fi
		integration_huatuo_bamai_start :
		assert_toml_values "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "{\"HTTPServer.ListenAddress\":\"${first#http://}\",\"HTTPServer.Auth.BearerToken\":\"token\"}"
	done
	local status=0
	write_config_bamai_http "" > "${HUATUO_BAMAI_TEST_TMPDIR}/empty-address.out" 2>&1 || status=$?
	assert_eq "$status" 1 "explicit empty address is rejected"
	# An unreplaceable destination forces failure after serialization, before rename.
	printf '[HTTPServer]\n' > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "${HUATUO_BAMAI_TEST_TMPDIR}/original.conf"
	status=0
	(
		python3() {
			if [[ $1 == - && $# == 3 ]]; then
				# Preserve parsing and writing; fail only the atomic replacement syscall.
				{
					printf 'import os\ndef denied(*args): raise PermissionError("fixture replace denied")\nos.replace = denied\n'
					cat
				} | command python3 "$@"
			else command python3 "$@"; fi
		}
		integration_huatuo_bamai_start :
	) > "${HUATUO_BAMAI_TEST_TMPDIR}/replace-failure.log" 2>&1 || status=$?
	assert_eq "${status}" 1 "configuration replacement failure"
	grep -q 'PermissionError: fixture replace denied' "${HUATUO_BAMAI_TEST_TMPDIR}/replace-failure.log" \
		|| fatal "replacement failure was not exercised; see ${HUATUO_BAMAI_TEST_TMPDIR}/replace-failure.log"
	cmp "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" "${HUATUO_BAMAI_TEST_TMPDIR}/original.conf" || fatal "failed replacement changed the original configuration"
	[[ -z $(find "${HUATUO_BAMAI_TEST_TMPDIR}" -maxdepth 1 -name '.bamai.*' -print -quit) ]] || fatal "failed replacement leaked temporary configuration"
	for address in ':0' '127.0.0.1:65536'; do
		write_config_bamai_http "${address}" > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
		status=0
		(integration_huatuo_bamai_start :) > "${HUATUO_BAMAI_TEST_TMPDIR}/invalid-listener.log" 2>&1 || status=$?
		assert_eq "${status}" 1 "invalid listener port"
		grep -q 'requires a port between 1 and 65535' "${HUATUO_BAMAI_TEST_TMPDIR}/invalid-listener.log" || fatal "missing invalid listener diagnosis"
	done
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
	ss() {
		printf 'LISTEN 0 128 127.0.0.1:20000 0.0.0.0:*\n'
		# Exceed pipe capacity after the match to expose early-reader SIGPIPE failures.
		awk 'BEGIN { for (i = 0; i < 20000; i++) print "ESTAB 0 0 127.0.0.1:19999 0.0.0.0:*" }'
	}
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
	assert_eq "$(allocate_available_port udp)" 20013 "UDP shares the allocation counter"
	printf '21000\n' > "${work}/ports/next"
	assert_eq "$(allocate_available_port)" 20014 "wrap skips occupied and reserved ports"
	seq 20000 20999 > "${work}/ports/allocated"
	if allocate_available_port; then fatal "allocation ignored execution reservations"; fi
	# Reset reservations while retaining the cursor across the slot boundary.
	ss() { :; }
	printf '20999\n' > "${work}/ports/next"
	prepare_execution_ports
	assert_eq "$(allocate_available_port)" 20999 "new execution clears reservations and retains cursor"
	prepare_execution_ports
	assert_eq "$(allocate_available_port)" 20000 "next execution wraps the cursor"
	assert_eq "$(allocate_available_port udp)" 20001 "wrapped execution reserves distinct ports"
	ss() { return 1; }
	if allocate_available_port; then fatal "allocation ignored failed socket query"; fi
)

assert_cpu_reference_startup() (
	source "${ROOT_DIR}/integration/lib_cpu_reference.sh"
	local work="${HUATUO_BAMAI_TEST_TMPDIR}/cpu-reference-startup" mode status expected clock=0 calls=0
	mkdir -p "${work}"
	command() {
		[[ $* != '-v pidstat' ]] || return 1
		builtin command "$@"
	}
	pidstat() { fatal "cAdvisor startup unexpectedly invoked pidstat"; }
	allocate_available_port() { echo 20000; }
	docker() {
		case $1 in
		inspect) [[ ${mode} == exited ]] && echo false || echo true ;;
		logs) echo 'cAdvisor fixture log' ;;
		esac
		return 0
	}
	curl() {
		calls=$((calls + 1))
		printf '%s\n' "${calls}" > "${work}/${mode}.calls"
		[[ ${mode} == immediate || (${mode} == delayed && ${calls} -gt 1) ]] && return 0
		echo 'cAdvisor fixture health check failed' >&2
		return 22
	}
	date() { printf '%s\n' "${clock}"; }
	sleep() { clock=$((clock + 15)); }
	for mode in immediate delayed timeout exited; do
		status=0 expected=0 clock=0 calls=0
		printf '0\n' > "${work}/${mode}.calls"
		(cpu_reference_start "${work}/${mode}") > "${work}/${mode}.log" 2>&1 || status=$?
		[[ ${mode} != timeout && ${mode} != exited ]] || expected=1
		assert_eq "${status}" "${expected}" "${mode}: cAdvisor readiness without pidstat"
		case ${mode} in
		immediate) expected=1 ;;
		delayed | timeout) expected=2 ;;
		exited) expected=0 ;;
		esac
		assert_eq "$(< "${work}/${mode}.calls")" "${expected}" "${mode}: health check attempts"
	done
	grep -Fq 'cAdvisor fixture log' "${work}/exited/reference/cadvisor.log" \
		|| fatal "cAdvisor startup exit omitted container logs"
	grep -Fq 'cAdvisor fixture log' "${work}/timeout/reference/cadvisor.log" \
		|| fatal "cAdvisor readiness timeout omitted container logs"
	grep -Fq 'health check failed' "${work}/timeout/reference/ready.err" \
		|| fatal "cAdvisor readiness timeout omitted the latest health error"
	status=0
	(cpu_reference_prepare_pidstat) > "${work}/missing-pidstat.log" 2>&1 || status=$?
	assert_eq "${status}" 77 "worker CPU sampling requires pidstat"
	grep -Fq 'command is not installed: pidstat' "${work}/missing-pidstat.log" \
		|| fatal "missing pidstat skip omitted the dependency name"
)

assert_cpu_reference_window() (
	source "${ROOT_DIR}/integration/lib_cpu_reference.sh"
	local work="${HUATUO_BAMAI_TEST_TMPDIR}/cpu-reference-window" mode calls status expected end clock=0
	mkdir -p "${work}"
	CPU_REFERENCE_URL=http://fixture
	end=$(jq -n '"2026-01-01T00:00:10Z" | fromdateiso8601')
	jq -n --argjson window_end "${end}" '
	 {"/fixture": [range(0; 10) | {timestamp: ($window_end-10+. | todateiso8601),
	  cpu_inst: {usage: {user: 200000000, system: 300000000, total: 500000000}}}]}
	' > "${work}/fixture-stale.json"
	jq '."/fixture" += [(."/fixture"[-1] | .timestamp = "2026-01-01T00:00:10.125Z")]' \
		"${work}/fixture-stale.json" > "${work}/fixture-fresh.json"
	cpu_reference_name() { echo /fixture; }
	curl() {
		calls=$((calls + 1))
		if [[ ${mode} == fresh || (${mode} == delayed && ${calls} -gt 1) ]]; then
			cp "${work}/fixture-fresh.json" "${@: -1}"
		else
			cp "${work}/fixture-stale.json" "${@: -1}"
		fi
	}
	# Advance the shared polling clock without waiting 30 seconds in this fixture.
	date() { printf '%s\n' "${clock}"; }
	sleep() { clock=$((clock + 15)); }
	for mode in default fresh delayed timeout; do
		calls=0 status=0 expected=0
		if [[ ${mode} == default ]]; then
			cpu_reference_stats /fixture "${work}/${mode}.json" \
				> "${work}/${mode}.stdout" 2> "${work}/${mode}.log"
		else
			cpu_reference_stats /fixture "${work}/${mode}.json" "${end}" \
				> "${work}/${mode}.stdout" 2> "${work}/${mode}.log" || status=$?
		fi
		[[ ${mode} != timeout ]] || expected=1
		assert_eq "${status}" "${expected}" "${mode}: cAdvisor window coverage"
		[[ ! -s ${work}/${mode}.stdout ]] || fatal "${mode}: cAdvisor polling logs polluted stdout"
		grep -Fq 'wait attempt' "${work}/${mode}.log" || fatal "${mode}: cAdvisor polling diagnostics missing from stderr"
		case ${mode} in
		default | fresh) assert_eq "${calls}" 1 "${mode}: no unnecessary retry" ;;
		delayed | timeout) assert_eq "${calls}" 2 "${mode}: bounded sample retry" ;;
		esac
	done
	jq -e "${CPU_REFERENCE_JQ}""average($((end - 10)); ${end}; 1) | .Total == 50" \
		"${work}/delayed.json" > /dev/null
	grep -Fq 'precedes window end' "${work}/timeout.json.err" \
		|| fatal "cAdvisor timeout omitted the latest sample timestamp"
	grep -Fq "window end ${end}" "${work}/timeout.log" \
		|| fatal "cAdvisor timeout omitted the required window endpoint"
)

cpu_image_status=0
bash -eu -s > "${HUATUO_BAMAI_TEST_TMPDIR}/cpu-image-failure.log" 2>&1 << 'SH' || cpu_image_status=$?
source "$ROOT_DIR/integration/lib.sh"
source "$ROOT_DIR/e2e/lib_cpu_load.sh"
crictl() { return 1; }
timeout() { return 1; }
cpu_load_prepare_image
SH
assert_eq "${cpu_image_status}" 1 "CPU image pull failure belongs to the CPU case"
grep -Fq 'cannot pull CPU workload image' "${HUATUO_BAMAI_TEST_TMPDIR}/cpu-image-failure.log" \
	|| fatal "CPU image pull failure lacks registry diagnostics"

assert_native_pid_prerequisite
assert_profiler_bpf_prerequisite
assert_helper_log_levels
assert_helper_toml_values
assert_helper_http_defaults
assert_helper_config_fragments
assert_helper_bamai_ports
assert_helper_metric_diagnostics
stop_and_wait_by_pid ""
assert_process_shutdown graceful_failure 7
assert_process_shutdown forced 137
assert_process_shutdown custom_signal 0 USR2
assert_helper_port_allocation
assert_cpu_reference_startup
assert_cpu_reference_window
log_info "framework logging, assertions, HTTP diagnostics, service addresses, process shutdown and port allocation passed"
