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

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly MODULE_NAME=ht_tx_timeout_t
readonly NETDEV="httx$$"
readonly KERNEL_BUILD="/lib/modules/$(uname -r)/build"
readonly MODULE_BUILD="${HUATUO_BAMAI_TEST_TMPDIR}/tx-timeout-module"
readonly CONTROL="/sys/module/${MODULE_NAME}/parameters"
readonly EVENT_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/netdev_txqueue_timeout"
readonly SAMPLE_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/tx-timeout-event.json"
module_loaded=false

cleanup() {
	local status=$?
	trap - EXIT
	huatuo_bamai_stop || status=1
	if [[ ${module_loaded} == true ]]; then
		# Taking the device down cancels any pending watchdog before unload.
		ip link set dev "${NETDEV}" down || status=1
		rmmod "${MODULE_NAME}" || status=1
	fi
	exit "${status}"
}
trap cleanup EXIT

require_commands make gcc insmod rmmod ip tc jq curl
tracepoint_available net net_dev_xmit_timeout \
	|| skip "net/net_dev_xmit_timeout tracepoint is unavailable"
[[ -r "${KERNEL_BUILD}/Makefile" ]] \
	|| skip "install matching kernel headers: ${KERNEL_BUILD}/Makefile is missing"
[[ $(< /proc/sys/kernel/modules_disabled) == 0 ]] \
	|| skip "kernel module loading is disabled"
[[ $(< /proc/sys/kernel/panic_on_warn) == 0 ]] \
	|| skip "watchdog may WARN on older kernels; panic_on_warn must be 0"
[[ ! -d /sys/module/${MODULE_NAME} ]] \
	|| skip "${MODULE_NAME} is already loaded; refusing to modify an existing fixture"

mkdir -p "${MODULE_BUILD}"
cp "${ROOT_DIR}/integration/testdata/netdev_txqueue_timeout/"{Makefile,ht_tx_timeout_t.c} "${MODULE_BUILD}/"
make -C "${KERNEL_BUILD}" M="${MODULE_BUILD}" modules \
	> "${MODULE_BUILD}/build.log" 2>&1 \
	|| fatal "watchdog fixture build failed; see ${MODULE_BUILD}/build.log"

if ! insmod "${MODULE_BUILD}/${MODULE_NAME}.ko" ifname="${NETDEV}" \
	2> "${MODULE_BUILD}/insmod.err"; then
	if grep -qiE 'operation not permitted|permission denied|key was rejected|required key not available' \
		"${MODULE_BUILD}/insmod.err"; then
		skip "module loading denied by permissions or signature policy: $(cat "${MODULE_BUILD}/insmod.err")"
	fi
	fatal "watchdog fixture load failed: $(cat "${MODULE_BUILD}/insmod.err")"
fi
module_loaded=true
[[ -r "${CONTROL}/timeout_count" ]] \
	|| fatal "watchdog fixture module parameters are missing: ${CONTROL}"

# Pin the qdisc rather than depending on the host's default queueing policy.
tc qdisc replace dev "${NETDEV}" root pfifo limit 16

integration_huatuo_bamai_start write_netdev_txqueue_timeout_config \
	--region dev --disable-kubelet --log-debug

observer_attached() {
	# This config enables no other perf-event observer; HTTP readiness is not enough.
	grep -q 'attached BPF and created event pipe' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}
wait_until 15 0.2 observer_attached \
	|| fatal "netdev_txqueue_timeout did not attach its BPF event pipe"

timeout_fired() {
	[[ $(< "${CONTROL}/timeout_count") == 1 ]]
}

event_is_valid() {
	[[ -s "${EVENT_FILE}" ]] || return 1
	jq -s -e --arg dev "${NETDEV}" --arg driver "${MODULE_NAME}" '
		first(.[] | select(
			.tracer_name == "netdev_txqueue_timeout"
			and .tracer_type == "event"
			and (.tracer_id | type == "string" and length > 0)
			and .tracer_data.queue_index == 0
			and .tracer_data.device_name == $dev
			and .tracer_data.driver_name == $driver
			and (.observed_timestamp | type == "string" and length > 0)
		))
	' "${EVENT_FILE}" > "${SAMPLE_FILE}" 2> "${MODULE_BUILD}/event-parse.err"
}

[[ $(< "${CONTROL}/timeout_count") == 0 ]] \
	|| fatal "watchdog fired before the test trigger"
ip link set dev "${NETDEV}" up
wait_until 10 0.1 timeout_fired \
	|| fatal "test device ${NETDEV} did not reach its watchdog timeout"
wait_until 15 0.2 event_is_valid \
	|| fatal "watchdog fired but no matching netdev_txqueue_timeout event was persisted"

assert_huatuo_bamai_shutdown
timeout_fired || fatal "test device watchdog did not recover after a single timeout"
event_is_valid || fatal "persisted netdev_txqueue_timeout event is invalid after shutdown"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
log_info "valid netdev_txqueue_timeout event: $(jq -c . "${SAMPLE_FILE}")"
