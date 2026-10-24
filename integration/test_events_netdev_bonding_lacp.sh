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

# Ubuntu 20.04 CI (5.4.0-216-generic) exposes the symbol but rejects its
# ad_disable_collecting_distributing kprobe with EADDRNOTAVAIL during attach.
if [[ -r /etc/os-release ]] && (
	source /etc/os-release
	[[ ${ID:-} == ubuntu && ${VERSION_ID:-} == 20.04 && $(uname -r) == 5.4.0-216-generic ]]
); then
	skip "Ubuntu 20.04 kernel 5.4.0-216-generic: LACP kprobe attach fails with EADDRNOTAVAIL"
fi

require_commands ip tc jq curl unshare awk grep

# Both the registration check and /proc/net/bonding must see the test bonds.
# An anonymous namespace also removes every test link when the case exits.
if [[ ${1:-} != --isolated ]]; then
	unshare --net true || skip "network namespace creation is unavailable"
	exec unshare --net bash "$0" --isolated
fi

# A host-side proxy is unreachable from the private network namespace.
export no_proxy=127.0.0.1 NO_PROXY=127.0.0.1

readonly LACP_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/lacp"
readonly LACP_SAMPLE="${HUATUO_BAMAI_TEST_TMPDIR}/lacp-event.json"
readonly LACP_PARSE_ERROR="${HUATUO_BAMAI_TEST_TMPDIR}/lacp-parse.err"

cleanup() {
	local status=$?
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		ip -details link show > "${HUATUO_BAMAI_TEST_TMPDIR}/lacp-links.txt" 2>&1 || true
		tc -statistics filter show dev lacpa1 ingress > "${HUATUO_BAMAI_TEST_TMPDIR}/lacp-filter.txt" 2>&1 || true
		for bond in lacpa lacpb; do
			cat "/proc/net/bonding/${bond}" > "${HUATUO_BAMAI_TEST_TMPDIR}/${bond}-failure.txt" 2>&1 || true
		done
	fi
	huatuo_bamai_stop || true
}
trap cleanup EXIT

lacp_converged() {
	local bond
	for bond in lacpa lacpb; do
		# Each slave has actor and partner state; bits 4/5 are collecting/distributing.
		awk '
			/port state:/ {
				count++
				if (int($3 / 16) % 2 != 1 || int($3 / 32) % 2 != 1) invalid++
			}
			END { exit !(count == 4 && invalid == 0) }
		' "/proc/net/bonding/${bond}" || return 1
	done
}

lacp_bpf_attached() {
	grep 'attached BPF and created event pipe' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		| grep -qE 'map_name="?ad_event_map"?([[:space:]]|$)'
}

lacp_event_is_stored() {
	[[ -s ${LACP_EVENT} ]] || return 1
	# The affected actor must leave collecting/distributing, excluding startup events.
	jq -s -e '
		first(.[] | select(
			.tracer_name == "lacp"
			and .tracer_type == "event"
			and (.tracer_id | type == "string" and length > 0)
			and (.tracer_data.content | type == "string")
			and (.tracer_data.content |
				contains("/proc/net/bonding/lacpa\n")
				and contains("/proc/net/bonding/lacpb\n")
				and contains("Bonding Mode: IEEE 802.3ad Dynamic link aggregation")
				and contains("Slave Interface: lacpa1\nMII Status: up")
				and contains("Slave Interface: lacpa2")
				and contains("Slave Interface: lacpb1")
				and contains("Slave Interface: lacpb2")
			)
			and (.tracer_data.content
				| split("Slave Interface: lacpa1\n")[1]
				| split("Slave Interface:")[0]
				| capture("port state: (?<state>[0-9]+)")
				| .state | tonumber
				| ((. / 16 | floor) % 2 == 0) and ((. / 32 | floor) % 2 == 0)
			)
		))
	' "${LACP_EVENT}" > "${LACP_SAMPLE}" 2> "${LACP_PARSE_ERROR}"
}

ip link set lo up
ip link add lacpa type bond mode 802.3ad miimon 100 lacp_rate fast \
	|| skip "802.3ad bonding creation is unavailable"
ip link add lacpb type bond mode 802.3ad miimon 100 lacp_rate fast
grep -qE '[[:space:]]ad_disable_collecting_distributing([[:space:]]|$)' /proc/kallsyms \
	|| skip "kernel symbol ad_disable_collecting_distributing is unavailable"

for n in 1 2; do
	ip link add "lacpa${n}" type veth peer name "lacpb${n}" \
		|| skip "veth creation is unavailable"
	ip link set "lacpa${n}" master lacpa
	ip link set "lacpb${n}" master lacpb
	ip link set "lacpa${n}" up
	ip link set "lacpb${n}" up
done
tc qdisc add dev lacpa1 clsact || skip "tc clsact qdisc is unavailable"
# Probe filter support before negotiation so unsupported kernels skip, not time out.
tc filter add dev lacpa1 ingress protocol 0x8809 pref 1 flower action drop \
	|| skip "tc flower/drop support is unavailable"
tc filter del dev lacpa1 ingress protocol 0x8809 pref 1
ip link set lacpa up
ip link set lacpb up
wait_until 30 0.2 lacp_converged || fatal "LACP did not enter collecting/distributing on all four slaves"

# Registration runs during Go init, so the bonds must exist before startup.
integration_huatuo_bamai_start write_lacp_events_config \
	--region dev --disable-kubelet --log-debug
wait_until 15 0.2 lacp_bpf_attached || fatal "LACP BPF event pipe was not attached"
wait_until 30 0.2 lacp_converged || fatal "LACP lost collecting/distributing before the fault"
for bond in lacpa lacpb; do
	cat "/proc/net/bonding/${bond}" > "${HUATUO_BAMAI_TEST_TMPDIR}/${bond}-before.txt"
done

# Carrier loss can bypass the probed mux transition; expire LACP with carrier up.
tc filter add dev lacpa1 ingress protocol 0x8809 pref 1 flower action drop
wait_until 20 0.2 lacp_event_is_stored || fatal \
	"no matching LACP event after member LACPDU loss; parser: $(cat "${LACP_PARSE_ERROR}" 2> /dev/null || true)"

assert_huatuo_bamai_shutdown
lacp_event_is_stored || fatal "stored LACP event is invalid after shutdown"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
log_info "LACPDU loss persisted both bonds, all four slaves, and disabled collecting/distributing"
