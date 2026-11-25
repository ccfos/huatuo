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

# Compare real root qdisc statistics with public metrics in an isolated network.
# Cover device filters and nonzero traffic, backlog, drops and overlimits;
# driver-dependent requeues are checked only at zero.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_net.sh"
source "${ROOT_DIR}/integration/config.sh"

require_commands ip tc python3 jq curl ss awk grep

readonly QDISC_DEVICE=htqdisc0
readonly QDISC_PEER=htqdisc1
readonly QDISC_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/qdisc-metrics"
readonly QDISC_METRICS="${QDISC_RESPONSE}.txt"
readonly QDISC_STATS="${HUATUO_BAMAI_TEST_TMPDIR}/qdisc-stats.json"
qdisc_owned=false
qdisc_included='^htqdisc[01]$'
qdisc_excluded=''

cleanup() {
	local status=0
	huatuo_bamai_stop || return 1
	if [[ ${qdisc_owned} == true ]]; then
		ip link delete "${QDISC_DEVICE}" || status=1
	fi
	return "${status}"
}
trap 'qdisc_exit_status=$?
if ! cleanup && [[ ${qdisc_exit_status} == 0 || ${qdisc_exit_status} == 77 ]]; then
	qdisc_exit_status=1
fi
exit "${qdisc_exit_status}"' EXIT

qdisc_setup() {
	local output="${HUATUO_BAMAI_TEST_TMPDIR}/qdisc-setup.log"
	if LC_ALL=C "$@" > "${output}" 2>&1; then return 0; fi
	cat "${output}" >&2
	if grep -Eqi 'operation not permitted|permission denied|not supported|unknown qdisc|specified qdisc kind is unknown' "${output}"; then
		skip "qdisc setup requires CAP_NET_ADMIN and veth/netem/tbf/prio kernel support: $*"
	fi
	fatal "qdisc setup failed: $*; see ${output}"
}

qdisc_send_packets() {
	# Private EtherType frames avoid ARP, routing and background reply traffic.
	python3 - "${QDISC_DEVICE}" "${1:-10}" << 'PY'
import socket
import sys

with socket.socket(socket.AF_PACKET, socket.SOCK_RAW) as sock:
	sock.bind((sys.argv[1], 0))
	frame = bytes.fromhex("ffffffffffff02000000000188b5") + bytes(50)
	for _ in range(int(sys.argv[2])):
		if sock.send(frame) != len(frame):
			raise RuntimeError("short qdisc test frame send")
PY
}

qdisc_rate_limit_drained() {
	tc -j -s qdisc show dev "${QDISC_DEVICE}" > "${QDISC_STATS}" || return 1
	jq -e 'any(.[]; .root == true and .kind == "tbf" and
		.overlimits > 0 and .qlen == 0 and .backlog == 0)' "${QDISC_STATS}" > /dev/null
}

assert_qdisc_metrics() {
	local kind=$1 name value device actual_kind extra samples suffix expected type actual_type
	local -A values=() found=()
	tc -j -s qdisc show dev "${QDISC_DEVICE}" > "${QDISC_STATS}"
	# Require the reference fields: missing kernel statistics must not become zeros.
	samples=$(jq -er --arg kind "${kind}" '
		[.[] | select(.root == true and .kind == $kind)]
		| if length == 1 then .[0] else error("expected one root qdisc") end
		| {bytes_total: .bytes, packets_total: .packets, drops_total: .drops,
		   requeues_total: .requeues, overlimits_total: .overlimits,
		   current_queue_length: .qlen, backlog: .backlog}
		| to_entries[]
		| if (.value | type) == "number" then [.key, .value] | @tsv
		  else error("missing numeric qdisc statistic") end
	' "${QDISC_STATS}") || fatal "cannot read root ${kind} statistics: ${QDISC_STATS}"
	while IFS=$'\t' read -r suffix expected; do
		values[${suffix}]=${expected}
	done <<< "${samples}"
	assert_metrics_response "${QDISC_RESPONSE}"
	assert_netdev_collector_success "${QDISC_METRICS}" netdev_qdisc
	samples=$(awk '
		/^huatuo_bamai_netdev_qdisc_/ {
			name = $1; sub(/\{.*/, "", name)
			device = $1; sub(/.*[,{]device="/, "", device); sub(/".*/, "", device)
			kind = $1; sub(/.*[,{]kind="/, "", kind); sub(/".*/, "", kind)
			print name "|" $2 "|" device "|" kind
		}' "${QDISC_METRICS}") || fatal "cannot parse qdisc samples: ${QDISC_METRICS}"
	[[ -n ${samples} ]] || fatal "no qdisc metrics: ${QDISC_METRICS}"
	while IFS='|' read -r name value device actual_kind extra; do
		suffix=${name#huatuo_bamai_netdev_qdisc_}
		[[ ${device}/${actual_kind} == "${QDISC_DEVICE}/${kind}" && -n ${values[${suffix}]+present} ]] \
			|| fatal "unexpected qdisc sample ${name}/${device}/${actual_kind}; see ${QDISC_METRICS}"
		found[${suffix}]=$((${found[${suffix}]:-0} + 1))
		assert_netdev_counter_value "${value}" "qdisc ${suffix}" "${values[${suffix}]}"
	done <<< "${samples}"
	for suffix in "${!values[@]}"; do
		assert_eq "${found[${suffix}]:-0}" 1 "qdisc ${suffix} sample count" \
			|| fatal "expected exactly one ${suffix} sample; see ${QDISC_METRICS}"
		type=counter
		case ${suffix} in backlog | current_queue_length) type=gauge ;; esac
		actual_type=$(awk -v metric="huatuo_bamai_netdev_qdisc_${suffix}" \
			'$1 == "#" && $2 == "TYPE" && $3 == metric { print $4 }' "${QDISC_METRICS}")
		assert_eq "${actual_type}" "${type}" "qdisc ${suffix} type" \
			|| fatal "invalid metric type for ${suffix}; see ${QDISC_METRICS}"
	done
}

assert_qdisc_filtered() {
	assert_metrics_response "${QDISC_RESPONSE}"
	assert_netdev_collector_success "${QDISC_METRICS}" netdev_qdisc
	if grep -q '^huatuo_bamai_netdev_qdisc_' "${QDISC_METRICS}"; then
		fatal "filtered qdisc produced metrics; see ${QDISC_METRICS}"
	fi
}

qdisc_stop() {
	local phase=$1
	assert_huatuo_bamai_shutdown
	assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_qdisc
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/qdisc-${phase}.log"
	cp "${QDISC_METRICS}" "${HUATUO_BAMAI_TEST_TMPDIR}/qdisc-${phase}.txt"
	cp "${QDISC_STATS}" "${HUATUO_BAMAI_TEST_TMPDIR}/qdisc-${phase}.json"
}

[[ ! -e /sys/class/net/${QDISC_DEVICE} && ! -e /sys/class/net/${QDISC_PEER} ]] \
	|| fatal "qdisc test device already exists; run through integration/run.sh for network isolation"
qdisc_setup ip link add "${QDISC_DEVICE}" type veth peer name "${QDISC_PEER}"
qdisc_owned=true
for qdisc_device in "${QDISC_DEVICE}" "${QDISC_PEER}"; do
	qdisc_setup ip link set dev "${qdisc_device}" addrgenmode none
	qdisc_setup ip link set dev "${qdisc_device}" up
done
qdisc_setup tc qdisc add dev "${QDISC_DEVICE}" root handle 1: netem limit 100
tc -j qdisc show dev "${QDISC_PEER}" | jq -e 'any(.[]; .kind == "noqueue")' > /dev/null \
	|| fatal "qdisc peer must expose noqueue to exercise skipping"

integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
assert_qdisc_metrics netem
qdisc_send_packets
assert_qdisc_metrics netem
jq -e 'any(.[]; .root == true and .bytes > 0 and .packets >= 10)' "${QDISC_STATS}" > /dev/null \
	|| fatal "test traffic did not advance qdisc bytes/packets: ${QDISC_STATS}"
qdisc_stop transmitted

# Hold frames beyond the test duration so both gauge observations are stable.
qdisc_setup tc qdisc change dev "${QDISC_DEVICE}" root netem delay 60s
integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
qdisc_send_packets
assert_qdisc_metrics netem
jq -e 'any(.[]; .root == true and .backlog > 0 and .qlen == 10)' "${QDISC_STATS}" > /dev/null \
	|| fatal "netem did not retain test traffic: ${QDISC_STATS}"
qdisc_stop queued

qdisc_setup tc qdisc replace dev "${QDISC_DEVICE}" root handle 2: netem loss 100%
qdisc_send_packets
integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
assert_qdisc_metrics netem
jq -e 'any(.[]; .root == true and .drops >= 10)' "${QDISC_STATS}" > /dev/null \
	|| fatal "netem did not drop test traffic: ${QDISC_STATS}"
qdisc_stop dropped

# Exhaust tokens without overflowing the queue; drain before comparing snapshots.
qdisc_setup tc qdisc replace dev "${QDISC_DEVICE}" root handle 3: tbf \
	rate 64kbit burst 1600 latency 5s
integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
qdisc_send_packets 200
wait_until 10 0.1 qdisc_rate_limit_drained \
	|| fatal "TBF did not report overlimits and drain within 10s; see ${QDISC_STATS}"
assert_qdisc_metrics tbf
jq -e 'any(.[]; .root == true and .overlimits > 0 and .packets == 200 and .drops == 0)' \
	"${QDISC_STATS}" > /dev/null \
	|| fatal "TBF must transmit all 200 frames with nonzero overlimits and no drops: ${QDISC_STATS}"
qdisc_stop overlimits

# A real child qdisc must be absent, not merely a device with no child to collect.
qdisc_setup tc qdisc replace dev "${QDISC_DEVICE}" root handle 1: prio
qdisc_setup tc qdisc add dev "${QDISC_DEVICE}" parent 1:1 handle 10: netem
tc -j qdisc show dev "${QDISC_DEVICE}" | jq -e 'any(.[]; .kind == "netem" and .parent == "1:1")' > /dev/null \
	|| fatal "child netem qdisc was not installed"
integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
assert_qdisc_metrics prio
qdisc_stop root-only

qdisc_excluded="^${QDISC_DEVICE}$"
integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
assert_qdisc_filtered
qdisc_stop excluded

qdisc_excluded=''
qdisc_included='^huatuo-no-such-device$'
integration_huatuo_bamai_start write_config_bamai_netdev_qdisc
assert_qdisc_filtered
qdisc_stop unmatched
log_info "netdev_qdisc kernel statistics, traffic, drops, overlimits, root/noqueue selection and device filters passed"
