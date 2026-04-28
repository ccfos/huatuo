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

set -euo pipefail

: "${ROOT_DIR:="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"}"
source ${ROOT_DIR}/integration/env.sh
source ${ROOT_DIR}/integration/lib.sh
source ${ROOT_DIR}/e2e/lib.sh

# Reconstruct array from environment variable (arrays cannot be exported directly)
if [[ -z "${HUATUO_BAMAI_ARGS_E2E:-}" ]]; then
	eval "HUATUO_BAMAI_ARGS_E2E=(${HUATUO_BAMAI_E2E_ARGS_STR})"
fi

AUTOTRACING_HUATUO_LOCAL_DIR="/tmp/huatuo-e2e-record"
AUTOTRACING_WAIT_TIMEOUT=120
AUTOTRACING_WAIT_INTERVAL=3
AUTOTRACING_IO_TESTFILE="/tmp/huatuo-e2e-iotest"

# -------------------------------- helpers ------------------------------------

write_autotracing_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai-autotracing.conf" <<'EOF'
BlackList = ["softlockup", "ethtool", "netdev_hw", "metax_gpu"]

[Log]
    Level = "Info"

[APIServer]
    TCPAddr = ":19704"

[Storage.LocalFile]
    Path = "/tmp/huatuo-e2e-record"
    RotationSize = 100
    MaxRotation = 5

[AutoTracing]
    IssuesList = []

    [AutoTracing.CPUSys]
        SysThreshold = 10
        DeltaSysThreshold = 5
        Interval = 3
        RunTracingToolTimeout = 10

    [AutoTracing.CPUIdle]
        UserThreshold = 10
        SysThreshold = 10
        UsageThreshold = 20
        DeltaUserThreshold = 5
        DeltaSysThreshold = 5
        DeltaUsageThreshold = 10
        Interval = 3
        IntervalTracing = 0
        RunTracingToolTimeout = 10

    [AutoTracing.IOTracing]
        UtilThreshold = 1
        AwaitThreshold = 1
        RbpsThreshold = 0
        WbpsThreshold = 0

    [AutoTracing.Dload]
        ThresholdLoad = 1
        Interval = 3
        IntervalTracing = 0
        EnableDebug = true

    [AutoTracing.MemoryBurst]
        DeltaMemoryBurst = 10
        DeltaAnonThreshold = 1
        Interval = 1
        IntervalTracing = 0
        SlidingWindowLength = 3
        DumpProcessMaxNum = 5

[Pod]
    KubeletReadOnlyPort = 10255
    KubeletAuthorizedPort = 10250
    KubeletClientCertPath = "/etc/kubernetes/pki/apiserver-kubelet-client.crt,/etc/kubernetes/pki/apiserver-kubelet-client.key"
EOF
}

check_huatuo_local_exists() {
	local tracer_name=$1
	[[ -s "${AUTOTRACING_HUATUO_LOCAL_DIR}/${tracer_name}" ]]
}

validate_huatuo_local_common() {
	local tracer_name=$1
	local record_file="${AUTOTRACING_HUATUO_LOCAL_DIR}/${tracer_name}"

	if [[ ! -s "${record_file}" ]]; then
		log_error "${tracer_name} record is empty"
		return 1
	fi

	if ! grep -q '"tracer_name"' "${record_file}"; then
		log_error "${tracer_name} record missing tracer_name field"
		cat "${record_file}"
		return 1
	fi

	return 0
}

validate_huatuo_local_flame() {
	local tracer_name=$1
	local record_file="${AUTOTRACING_HUATUO_LOCAL_DIR}/${tracer_name}"

	validate_huatuo_local_common "${tracer_name}" || return 1

	local profile_type profile
	profile_type=$(jq -r '.tracer_data.flamedata.profile_type // empty' "${record_file}" 2>/dev/null | head -1)
	profile=$(jq -r '.tracer_data.flamedata.profile // empty' "${record_file}" 2>/dev/null | head -1)

	if [[ -z "${profile_type}" ]]; then
		log_error "${tracer_name} record: tracer_data.flamedata.profile_type is missing or empty"
		jq '.tracer_data.flamedata' "${record_file}" 2>/dev/null || cat "${record_file}"
		return 1
	fi

	if [[ -z "${profile}" ]]; then
		log_error "${tracer_name} record: tracer_data.flamedata.profile is missing or empty"
		jq '.tracer_data.flamedata' "${record_file}" 2>/dev/null || cat "${record_file}"
		return 1
	fi

	log_info "${tracer_name} record validated: profile_type=${profile_type}"
}

validate_huatuo_local_iotracing() {
	local record_file="${AUTOTRACING_HUATUO_LOCAL_DIR}/iotracing"

	validate_huatuo_local_common "iotracing" || return 1

	if ! jq -e '.tracer_data.reason_snapshot' "${record_file}" >/dev/null 2>&1; then
		log_error "iotracing record: tracer_data.reason_snapshot is missing"
		jq '.tracer_data' "${record_file}" 2>/dev/null || cat "${record_file}"
		return 1
	fi

	log_info "iotracing record validated"
}

kill_stress_pids() {
	local pids="$1"
	if [[ -n "${pids}" ]]; then
		kill ${pids} 2>/dev/null || true
		wait ${pids} 2>/dev/null || true
	fi
}

# -------------------------------- cpusys -------------------------------------

test_autotracing_cpusys() {
	log_info "========== Phase 1: autotracing cpusys =========="

	log_info "generating CPU stress load..."
	local stress_pids=""
	if command -v stress &>/dev/null; then
		stress --cpu 2 --io 2 --timeout 90 &
		stress_pids="$!"
	else
		for _i in 1 2; do
			dd if=/dev/zero of=/dev/null bs=1M count=999999 &>/dev/null &
			stress_pids="${stress_pids} $!"
		done
	fi

	if ! wait_until "${AUTOTRACING_WAIT_TIMEOUT}" "${AUTOTRACING_WAIT_INTERVAL}" \
		"cpusys record file generated" check_huatuo_local_exists "cpusys"; then
		log_error "cpusys record not generated within timeout"
		kill_stress_pids "${stress_pids}"
		return 1
	fi

	kill_stress_pids "${stress_pids}"
	validate_huatuo_local_flame "cpusys"

	log_info "========== Phase 1: autotracing cpusys passed =========="
}

# -------------------------------- cpuidle ------------------------------------

CPUIDLE_TEST_POD_NAME="cpuidle-e2e-test"

delete_cpuidle_test_pod() {
	local wait_flag="${1:---wait=false}"
	kubectl delete pod "${CPUIDLE_TEST_POD_NAME}" -n "${BUSINESS_POD_NS}" \
		--ignore-not-found "${wait_flag}" 2>/dev/null || true
}

create_cpuidle_test_pod() {
	delete_cpuidle_test_pod --wait=true

	kubectl run "${CPUIDLE_TEST_POD_NAME}" \
		-n "${BUSINESS_POD_NS}" \
		--image="${BUSINESS_POD_IMAGE}" \
		--restart=Always \
		--overrides='{"spec":{"containers":[{"name":"cpuidle-e2e-test","image":"'"${BUSINESS_POD_IMAGE}"'","command":["sh","-c","sleep infinity"],"resources":{"requests":{"cpu":"100m"},"limits":{"cpu":"1"}}}]}}'

	kubectl wait --for=condition=Ready \
		pod/"${CPUIDLE_TEST_POD_NAME}" \
		-n "${BUSINESS_POD_NS}" \
		--timeout=60s
	log_info "${CPUIDLE_TEST_POD_NAME} created with cpu limit=1"
}

test_autotracing_cpuidle() {
	log_info "========== Phase 2: autotracing cpuidle =========="

	log_info "generating CPU load in cpuidle test pod..."
	kubectl exec "${CPUIDLE_TEST_POD_NAME}" -n "${BUSINESS_POD_NS}" -- \
		sh -c 'while true; do :; done' &
	local exec_pid=$!

	if ! wait_until "${AUTOTRACING_WAIT_TIMEOUT}" "${AUTOTRACING_WAIT_INTERVAL}" \
		"cpuidle record file generated" check_huatuo_local_exists "cpuidle"; then
		log_error "cpuidle record not generated within timeout"
		kill ${exec_pid} 2>/dev/null || true
		return 1
	fi

	kill ${exec_pid} 2>/dev/null || true
	validate_huatuo_local_flame "cpuidle"

	log_info "========== Phase 2: autotracing cpuidle passed =========="
}

# -------------------------------- dload ---------------------------------------

validate_huatuo_local_dload() {
	local record_file="${AUTOTRACING_HUATUO_LOCAL_DIR}/dload"

	validate_huatuo_local_common "dload" || return 1

	if ! jq -e '.tracer_data.stack' "${record_file}" >/dev/null 2>&1; then
		log_error "dload record: tracer_data.stack is missing"
		jq '.tracer_data' "${record_file}" 2>/dev/null || cat "${record_file}"
		return 1
	fi

	log_info "dload record validated"
}

test_autotracing_dload() {
	log_info "========== Phase 3: autotracing dload =========="

	if ! [[ -d /sys/fs/cgroup/cpu ]]; then
		log_info "cgroup v2 detected, skipping dload test (netlink CGROUPSTATS_CMD_GET requires cgroup v1)"
		log_info "========== Phase 3: autotracing dload skipped (cgroup v2) =========="
		return 0
	fi

	if ! wait_until "${AUTOTRACING_WAIT_TIMEOUT}" "${AUTOTRACING_WAIT_INTERVAL}" \
		"dload record file generated" check_huatuo_local_exists "dload"; then
		log_error "dload record not generated within timeout"
		return 1
	fi

	validate_huatuo_local_dload

	log_info "========== Phase 3: autotracing dload passed =========="
}

# -------------------------------- iotracing ----------------------------------

test_autotracing_iotracing() {
	log_info "========== Phase 4: autotracing iotracing =========="

	log_info "generating IO load..."
	(while true; do dd if=/dev/zero of="${AUTOTRACING_IO_TESTFILE}" bs=4k count=50000 oflag=direct conv=notrunc 2>/dev/null; done) &
	local dd_pid=$!

	if ! wait_until "${AUTOTRACING_WAIT_TIMEOUT}" "${AUTOTRACING_WAIT_INTERVAL}" \
		"iotracing record file generated" check_huatuo_local_exists "iotracing"; then
		log_error "iotracing record not generated within timeout"
		kill ${dd_pid} 2>/dev/null || true
		rm -f "${AUTOTRACING_IO_TESTFILE}"
		return 1
	fi

	kill ${dd_pid} 2>/dev/null || true
	rm -f "${AUTOTRACING_IO_TESTFILE}"
	validate_huatuo_local_iotracing

	log_info "========== Phase 4: autotracing iotracing passed =========="
}

# -------------------------------- memburst ------------------------------------

validate_huatuo_local_memburst() {
	local record_file="${AUTOTRACING_HUATUO_LOCAL_DIR}/memburst"

	validate_huatuo_local_common "memburst" || return 1

	if ! jq -e '.tracer_data.top_memory_usage' "${record_file}" >/dev/null 2>&1; then
		log_error "memburst record: tracer_data.top_memory_usage is missing"
		jq '.tracer_data' "${record_file}" 2>/dev/null || cat "${record_file}"
		return 1
	fi

	log_info "memburst record validated"
}

test_autotracing_memburst() {
	log_info "========== Phase 5: autotracing memburst =========="

	log_info "generating memory pressure..."
	(python3 -c "
import time
blocks = []
for i in range(500):
    blocks.append(bytearray(1024*1024))
    time.sleep(0.05)
time.sleep(300)
" &>/dev/null) &
	local mem_pid=$!

	if ! wait_until "${AUTOTRACING_WAIT_TIMEOUT}" "${AUTOTRACING_WAIT_INTERVAL}" \
		"memburst record file generated" check_huatuo_local_exists "memburst"; then
		log_error "memburst record not generated within timeout"
		kill ${mem_pid} 2>/dev/null || true
		return 1
	fi

	kill ${mem_pid} 2>/dev/null || true
	validate_huatuo_local_memburst

	log_info "========== Phase 5: autotracing memburst passed =========="
}

# -------------------------------- main ---------------------------------------

autotracing_cleanup() {
	kill_stress_pids "$(jobs -p)" 2>/dev/null || true
	rm -f "${AUTOTRACING_IO_TESTFILE}"
	rm -rf "${AUTOTRACING_HUATUO_LOCAL_DIR}"
	delete_cpuidle_test_pod
}

test_autotracing() {
	log_info "========== test autotracing e2e =========="

	rm -rf "${AUTOTRACING_HUATUO_LOCAL_DIR}"

	huatuo_bamai_stop

	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-before-autotracing.log" 2>/dev/null || true

	create_cpuidle_test_pod

	write_autotracing_config
	huatuo_bamai_start \
		"--config-dir" "${HUATUO_BAMAI_TEST_TMPDIR}" \
		"--config" "huatuo-bamai-autotracing.conf" \
		"--region" "e2e"
	log_info "huatuo-bamai started with autotracing config"

	local failed=0

	test_autotracing_cpusys || failed=1

	if [[ ${failed} -eq 0 ]]; then
		test_autotracing_cpuidle || failed=1
	fi

	if [[ ${failed} -eq 0 ]]; then
		test_autotracing_dload || failed=1
	fi

	if [[ ${failed} -eq 0 ]]; then
		test_autotracing_iotracing || failed=1
	fi

	if [[ ${failed} -eq 0 ]]; then
		test_autotracing_memburst || failed=1
	fi

	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-autotracing.log" 2>/dev/null || true

	autotracing_cleanup

	if [[ ${failed} -ne 0 ]]; then
		fatal "❌ autotracing e2e test failed"
	fi

	log_info "========== test autotracing e2e passed =========="
}

test_autotracing
