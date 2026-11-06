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

# DCB PFC counters come from the rtnetlink IEEE interface, which only drivers
# with dcbnl support answer; veth, netdevsim and dummy return EOPNOTSUPP.
# Select an existing PFC-capable NIC and skip when the host has none.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_net.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly NETDEV_DCB_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/dcb-metrics"
readonly NETDEV_DCB_METRICS="${NETDEV_DCB_RESPONSE}.txt"
readonly NETDEV_DCB_REFERENCE_BEFORE="${HUATUO_BAMAI_TEST_TMPDIR}/dcb-reference-before.txt"
readonly NETDEV_DCB_REFERENCE_AFTER="${HUATUO_BAMAI_TEST_TMPDIR}/dcb-reference-after.txt"
readonly NETDEV_DCB_PROBE="${HUATUO_BAMAI_TEST_TMPDIR}/dcb-probe.txt"
readonly NETDEV_DCB_FIXTURES="${HUATUO_BAMAI_TEST_TMPDIR}/netdev-dcb-fixtures"

require_commands python3 curl ss awk grep

NETDEV_DCB_PORT=$(allocate_available_port) || fatal "cannot allocate netdev_dcb HTTP port"
readonly NETDEV_DCB_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${NETDEV_DCB_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

netdev_dcb_pattern=""
netdev_dcb_device=""

cleanup() {
	huatuo_bamai_stop
}
trap 'netdev_dcb_exit_status=$?
if ! cleanup && [[ ${netdev_dcb_exit_status} == 0 || ${netdev_dcb_exit_status} == 77 ]]; then
	netdev_dcb_exit_status=1
fi
exit "${netdev_dcb_exit_status}"' EXIT

# Independent reference for the IEEE PFC counters. Argument list holds the
# interfaces to probe; unsupported devices are ignored.
dcb_query() {
	python3 - "$@" << 'PY'
import errno
import socket
import struct
import sys

RTM_GETDCB = 78
DCB_CMD_IEEE_GET = 21
DCB_ATTR_IFNAME = 1
DCB_ATTR_IEEE = 13
DCB_ATTR_IEEE_PFC = 2
NLM_F_REQUEST = 1
NLMSG_ERROR = 2
NLMSG_DONE = 3
AF_UNSPEC = 0
PFC_LEN = 136


class Unsupported(Exception):
	pass


def nlattr(kind, payload):
	raw = struct.pack("<HH", 4 + len(payload), kind) + payload
	return raw + b"\0" * (-len(raw) % 4)


def parse_attrs(data):
	offset = 0
	while offset + 4 <= len(data):
		length, kind = struct.unpack_from("<HH", data, offset)
		if length < 4 or offset + length > len(data):
			raise RuntimeError("malformed netlink attribute")
		yield kind, data[offset + 4:offset + length]
		offset += (length + 3) & ~3


def query(ifname):
	payload = bytes([AF_UNSPEC, DCB_CMD_IEEE_GET]) + b"\0\0"
	payload += nlattr(DCB_ATTR_IFNAME, ifname.encode() + b"\0")
	request = struct.pack(
		"<IHHII", 16 + len(payload), RTM_GETDCB, NLM_F_REQUEST, 1, 0
	) + payload
	with socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, socket.NETLINK_ROUTE) as sock:
		sock.bind((0, 0))
		sock.settimeout(5)
		sock.send(request)
		for _ in range(8):
			reply = sock.recv(65536)
			length, kind = struct.unpack_from("<IH", reply)
			body = reply[16:length]
			if kind == NLMSG_ERROR:
				code = struct.unpack_from("<i", body, 0)[0]
				if code:
					raise OSError(-code, "dcb netlink request failed")
				continue
			if kind == NLMSG_DONE:
				break
			if len(body) < 4:
				raise RuntimeError("short dcb reply")
			for attr_kind, attr_value in parse_attrs(body[4:]):
				if attr_kind != DCB_ATTR_IEEE:
					continue
				for sub_kind, sub_value in parse_attrs(attr_value):
					if sub_kind != DCB_ATTR_IEEE_PFC:
						continue
					if len(sub_value) < PFC_LEN:
						raise Unsupported("short IEEE PFC attribute")
					requests = struct.unpack_from("<8Q", sub_value, 8)
					indications = struct.unpack_from("<8Q", sub_value, 72)
					return requests, indications
	raise Unsupported("no IEEE PFC attribute")


for name in sys.argv[1:]:
	try:
		requests, indications = query(name)
	except OSError as err:
		if err.errno in (errno.EOPNOTSUPP, errno.ENODEV):
			continue
		print(
			f"{name}: {err.strerror} (errno {err.errno})",
			file=sys.stderr,
		)
		raise SystemExit(1)
	except Unsupported:
		continue
	for prio, value in enumerate(requests):
		print(f"pfc_send_total|{name}|{prio}|{value}")
	for prio, value in enumerate(indications):
		print(f"pfc_received_total|{name}|{prio}|{value}")
PY
}

dcb_reference() {
	local file=$1
	dcb_query "${netdev_dcb_device}" > "${file}" \
		|| fatal "cannot read DCB PFC counters for ${netdev_dcb_device}"
	[[ -s ${file} ]] || fatal "DCB device ${netdev_dcb_device} lost PFC support"
}

assert_dcb_counter_between() {
	local actual=$1 low=$2 high=$3 description=$4
	assert_netdev_counter_value "${actual}" "${description}" \
		|| fatal "${description}: expected a nonnegative counter, actual=${actual}"
	awk -v actual="${actual}" -v low="${low}" -v high="${high}" \
		'BEGIN { exit !(actual + 0 >= low + 0 && actual + 0 <= high + 0) }' \
		|| fatal "${description}: actual=${actual}, expected ${low}..${high}"
}

# Counters may advance while the daemon scrapes, so accept the metric between
# the two references and never assume the kernel values stand still.
assert_dcb_metrics() {
	local name value device prio samples key count
	local -A before=() after=() found=()
	dcb_reference "${NETDEV_DCB_REFERENCE_BEFORE}"
	assert_metrics_response "${NETDEV_DCB_RESPONSE}"
	dcb_reference "${NETDEV_DCB_REFERENCE_AFTER}"

	while IFS='|' read -r name device prio value; do
		key="${name}/${prio}"
		before[${key}]=${value}
	done < "${NETDEV_DCB_REFERENCE_BEFORE}"
	while IFS='|' read -r name device prio value; do
		key="${name}/${prio}"
		[[ -n ${before[${key}]+present} ]] \
			|| fatal "DCB reference counters changed: ${key}"
		after[${key}]=${value}
	done < "${NETDEV_DCB_REFERENCE_AFTER}"
	count=8
	assert_eq "${#before[@]}" "$((count * 2))" "DCB PFC counter count" \
		|| fatal "DCB ${netdev_dcb_device}: expected send/received counters for 8 priorities"
	assert_eq "${#after[@]}" "${#before[@]}" "DCB PFC counter count" \
		|| fatal "DCB ${netdev_dcb_device}: reference counters disappeared"

	assert_netdev_collector_success "${NETDEV_DCB_METRICS}" netdev_dcb
	samples=$(awk '
		/^huatuo_bamai_netdev_dcb_/ {
			name = $1; sub(/\{.*/, "", name)
			device = $1; sub(/.*[,{]device="/, "", device); sub(/".*/, "", device)
			prio = $1; sub(/.*[,{]prio="/, "", prio); sub(/".*/, "", prio)
			print name "|" $2 "|" device "|" prio
		}' "${NETDEV_DCB_METRICS}") || fatal "cannot parse DCB metrics: ${NETDEV_DCB_METRICS}"
	[[ -n ${samples} ]] || fatal "no DCB metrics: ${NETDEV_DCB_METRICS}"
	while IFS='|' read -r name value device prio; do
		[[ ${device} == "${netdev_dcb_device}" ]] \
			|| fatal "DCB: unexpected device ${device}; see ${NETDEV_DCB_METRICS}"
		key="${name#huatuo_bamai_netdev_dcb_}/${prio}"
		[[ -n ${before[${key}]+present} ]] \
			|| fatal "DCB: unexpected counter/prio ${key}; see ${NETDEV_DCB_METRICS}"
		found[${key}]=$((${found[${key}]:-0} + 1))
		assert_dcb_counter_between "${value}" "${before[${key}]}" "${after[${key}]}" "DCB ${key}"
	done <<< "${samples}"
	for key in "${!before[@]}"; do
		assert_eq "${found[${key}]:-0}" 1 "DCB ${key} sample count" \
			|| fatal "DCB ${netdev_dcb_device}: expected each counter/prio once; see ${NETDEV_DCB_METRICS}"
	done
	assert_netdev_counter_type "${NETDEV_DCB_METRICS}" huatuo_bamai_netdev_dcb_pfc_send_total
	assert_netdev_counter_type "${NETDEV_DCB_METRICS}" huatuo_bamai_netdev_dcb_pfc_received_total
	grep '^huatuo_bamai_netdev_dcb_' "${NETDEV_DCB_METRICS}" \
		| grep -F "device=\"${netdev_dcb_device}\""
}

assert_dcb_filtered() {
	assert_metrics_response "${NETDEV_DCB_RESPONSE}"
	assert_netdev_collector_success "${NETDEV_DCB_METRICS}" netdev_dcb
	if grep -q '^huatuo_bamai_netdev_dcb_' "${NETDEV_DCB_METRICS}"; then
		fatal "filtered DCB device produced metrics; see ${NETDEV_DCB_METRICS}"
	fi
}

# 1. Find a real NIC that implements the IEEE PFC query. The fixture sysfs only
# lists names, so the netdev_dcb collector still uses the live netlink device.
dcb_candidates=()
for dcb_path in /sys/class/net/*; do
	dcb_candidates+=("${dcb_path##*/}")
done
dcb_query "${dcb_candidates[@]}" > "${NETDEV_DCB_PROBE}" \
	|| fatal "DCB netlink probe failed; see ${NETDEV_DCB_PROBE}"
netdev_dcb_device=$(awk -F'|' 'NR == 1 { print $2 }' "${NETDEV_DCB_PROBE}")
[[ -n ${netdev_dcb_device} ]] || skip "no netdev exposes DCB IEEE PFC counters"
readonly netdev_dcb_device

dcb_regex=$(printf '%s\n' "${netdev_dcb_device}" | sed 's/[][\\.^$*+?(){}|]/\\&/g')
# lo is always present and returns EOPNOTSUPP, so the collector must skip it.
netdev_dcb_pattern="^${dcb_regex}$|^lo$"
log_info "netdev_dcb device=${netdev_dcb_device}"

cp -a "${HUATUO_BAMAI_TEST_FIXTURES}" "${NETDEV_DCB_FIXTURES}"
rm -rf "${NETDEV_DCB_FIXTURES}/sys/class/net"
mkdir -p "${NETDEV_DCB_FIXTURES}/sys/class/net/${netdev_dcb_device}" \
	"${NETDEV_DCB_FIXTURES}/sys/class/net/lo"

# 2. Read the live PFC counters and compare them with the public metrics.
integration_huatuo_bamai_start write_config_bamai_netdev_dcb \
	--region dev --procfs-prefix "${NETDEV_DCB_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
assert_dcb_metrics
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_dcb
mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/dcb-enabled.log"

# 3. Restart with a pattern that matches only the non-DCB loopback device.
netdev_dcb_pattern='^lo$'
integration_huatuo_bamai_start write_config_bamai_netdev_dcb \
	--region dev --procfs-prefix "${NETDEV_DCB_FIXTURES}" \
	--disable-storage --disable-kubelet --log-debug
assert_dcb_filtered
assert_huatuo_bamai_shutdown
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" netdev_dcb
log_info "netdev_dcb PFC counters, device labels and filters passed for ${netdev_dcb_device}"
