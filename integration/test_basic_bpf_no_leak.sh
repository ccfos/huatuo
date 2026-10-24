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

# Verify BPF reclamation after SIGTERM on a shared kernel. The observer records
# resource IDs synchronously, including pins and short-lived descendant FDs.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_bpf.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly BPF_LEAK_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/bpf"
readonly BPF_LEAK_AGENT_BIN="${HUATUO_BAMAI_BIN}"
readonly BPF_LEAK_EXIT_TIMEOUT=20
readonly BPF_LEAK_RECLAIM_TIMEOUT=20

command -v bpftool > /dev/null || skip "bpftool command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v gcc > /dev/null || skip "gcc command is not installed"
command -v timeout > /dev/null || skip "timeout command is not installed"
BPF_LEAK_PORT=$(allocate_available_port) || fatal "failed to allocate an Agent port"
readonly BPF_LEAK_PORT
HUATUO_BAMAI_METRICS_API="http://127.0.0.1:${BPF_LEAK_PORT}/metrics"

bpf_leak_observer_pid=""
bpf_leak_stable_polls=0
bpf_leak_foreign_dir=""

cleanup() {
	if [[ -z "${bpf_leak_observer_pid}" && -r "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" ]]; then
		bpf_leak_observer_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	fi
	# EXITKILL in the observer also covers reparented descendants. Only stop our job.
	if [[ -n "${bpf_leak_observer_pid}" ]] && jobs -pr | grep -Fxq "${bpf_leak_observer_pid}"; then
		stop_by_pid "${bpf_leak_observer_pid}" 0 || true
		wait "${bpf_leak_observer_pid}" 2> /dev/null || true
	fi
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" || log_warn "cannot remove Agent PID file"
	bpf_remove_pin_directory "${bpf_leak_foreign_dir}" || log_warn "cannot remove unrelated workload pins"
}
trap cleanup EXIT

bpf_leak_read_inventory() {
	local record
	# Ignore an unfinished last line while the observer is still appending.
	while IFS= read -r record; do
		printf '%s\n' "${record}" || fatal "cannot copy BPF observation record"
	done < "${BPF_LEAK_DIR}/observation/resources.jsonl" > "${BPF_LEAK_DIR}/records.jsonl" \
		|| fatal "cannot read BPF observation journal"
	jq -s 'if all(.[]; (.kind == "prog" or .kind == "map" or .kind == "link")
		and (.id | type == "number" and . > 0)
		and (.pid | type == "number" and . > 0)
		and (.created | type == "boolean")
		and ((has("pin") | not) or (.pin | type == "string")))
		then . else error("invalid BPF observation record") end' \
		"${BPF_LEAK_DIR}/records.jsonl" > "${BPF_LEAK_DIR}/records.json" \
		|| fatal "cannot parse BPF resource observations"
	if [[ -r "${BPF_LEAK_DIR}/observation/complete.json" ]]; then
		jq -e --slurpfile completion "${BPF_LEAK_DIR}/observation/complete.json" \
			'length == $completion[0].record_count' "${BPF_LEAK_DIR}/records.json" > /dev/null \
			|| fatal "BPF observation journal is truncated or incomplete"
	fi
	if ! "${bpf_link_enumeration_available}" && jq -e 'any(.[]; .kind == "link")' \
		"${BPF_LEAK_DIR}/records.json" > /dev/null; then
		fatal "Agent created BPF links but bpftool cannot enumerate them; install a matching bpftool"
	fi
	jq -e '([.[] | select(.legacy_attachment? == true) | .id] -
		[.[] | select(.created and .kind == "prog") | .id]) | length == 0' \
		"${BPF_LEAK_DIR}/records.json" > /dev/null \
		|| fatal "cannot verify a borrowed program behind an unenumerable legacy attachment"
}

bpf_leak_collect_and_check_ready() {
	bpf_snapshot "${BPF_LEAK_DIR}/running"
	bpf_leak_read_inventory

	jq -n --slurpfile records "${BPF_LEAK_DIR}/records.json" \
		--slurpfile running "${BPF_LEAK_DIR}/running.json" '
		def created_ids($kind): [$records[0][] | select(.created and .kind == $kind) | .id] | unique;
		created_ids("prog") as $program_ids |
		created_ids("map") as $map_ids |
		created_ids("link") as $link_ids |
		{program_ids: $program_ids, map_ids: $map_ids, link_ids: $link_ids,
		 program_count: ([$running[0].prog[] | select(.id as $id | $program_ids | index($id))] | length),
		 map_count: ([$running[0].map[] | select(.id as $id | $map_ids | index($id))] | length),
		 attachment_count: (([$running[0].perf[] | select(.prog_id as $id | $program_ids | index($id))] | length)
			+ ([$running[0].link[] | select(.id as $id | $link_ids | index($id))] | length))}
		' > "${BPF_LEAK_DIR}/owned.next.json" || fatal "cannot assemble Agent BPF inventory"
	if cmp -s "${BPF_LEAK_DIR}/owned.json" "${BPF_LEAK_DIR}/owned.next.json"; then
		bpf_leak_stable_polls=$((bpf_leak_stable_polls + 1))
	else
		bpf_leak_stable_polls=0
	fi
	mv "${BPF_LEAK_DIR}/owned.next.json" "${BPF_LEAK_DIR}/owned.json" \
		|| fatal "cannot publish Agent BPF inventory"
	jq -e '.program_count > 0 and .map_count > 0 and .attachment_count > 0' \
		"${BPF_LEAK_DIR}/owned.json" > /dev/null && ((bpf_leak_stable_polls >= 2))
}

bpf_leak_freeze_candidates() {
	bpf_leak_read_inventory
	jq -n --slurpfile records "${BPF_LEAK_DIR}/records.json" \
		--slurpfile before "${BPF_LEAK_DIR}/before.json" '
		def created_ids($kind): [$records[0][] | select(.created and .kind == $kind) | .id] | unique;
		{prog: created_ids("prog"), map: created_ids("map"), link: created_ids("link")} as $created |
		[$records[0][] | select(has("pin")) | {kind, id, path: .pin} |
		 . as $pin | select($created[.kind] | index($pin.id) | not) |
		 select(any($before[0][$pin.kind][]; .id == $pin.id and
			any(.pinned[]?; . == $pin.path)) | not)] | unique as $pins |
		{created: $created, pins: $pins}
		' > "${BPF_LEAK_DIR}/candidates.json" || fatal "cannot freeze Agent resource candidates"
}

bpf_leak_write_remaining() {
	local phase=$1
	jq -n --slurpfile candidates "${BPF_LEAK_DIR}/candidates.json" \
		--slurpfile current "${BPF_LEAK_DIR}/${phase}.json" '
		$candidates[0] as $owned | $current[0] as $current |
		def surviving($kind): [$current[$kind][] | select(.id as $id | $owned.created[$kind] | index($id))];
		{prog: surviving("prog"), map: surviving("map"), link: surviving("link"),
		 perf: [$current.perf[] | select(.prog_id as $id | $owned.created.prog | index($id))],
		 pins: [$owned.pins[] | . as $pin |
			select(any($current[$pin.kind][]; .id == $pin.id and any(.pinned[]?; . == $pin.path)))]}
		' > "${BPF_LEAK_DIR}/remaining.json" || fatal "cannot inspect Agent BPF reclamation"
}

bpf_leak_write_remaining_ids() {
	local kind
	mkdir -p "${BPF_LEAK_DIR}/recheck" || fatal "cannot create BPF recheck workspace"
	for kind in prog map link; do
		jq -r --arg kind "${kind}" '[
			(.[$kind][] | .id), (.pins[] | select(.kind == $kind) | .id)] | unique[]' \
			"${BPF_LEAK_DIR}/remaining.json" > "${BPF_LEAK_DIR}/recheck/${kind}-ids.txt" \
			|| fatal "cannot record remaining BPF ${kind} IDs"
	done
}

bpf_leak_check_reclaimed() {
	local deadline=$1 kind id destination
	local directory="${BPF_LEAK_DIR}/recheck"
	(($(date +%s) < deadline)) || return 1
	for kind in prog map link; do
		: > "${directory}/${kind}.jsonl" || fatal "cannot create BPF recheck output"
		while read -r id; do
			(($(date +%s) < deadline)) || return 1
			destination="${directory}/${kind}-${id}.json"
			if bpf_query_by_id "${kind}" "${id}" "${destination}" "${deadline}"; then
				jq -c . "${destination}" >> "${directory}/${kind}.jsonl" \
					|| fatal "cannot record surviving BPF ${kind} ID ${id}"
			fi
		done < "${directory}/${kind}-ids.txt" || fatal "cannot read BPF ${kind} recheck IDs"
		jq -s . "${directory}/${kind}.jsonl" > "${directory}/${kind}.json" \
			|| fatal "cannot assemble BPF ${kind} recheck results"
	done
	(($(date +%s) < deadline)) || return 1
	bpf_query perf "${directory}/perf.json" "${deadline}"
	bpf_write_snapshot "${directory}"
	bpf_leak_write_remaining recheck
	bpf_leak_write_remaining_ids
	(($(date +%s) < deadline)) || return 1
	jq -e 'all(.[]; length == 0)' "${BPF_LEAK_DIR}/remaining.json" > /dev/null
}

assert_bpf_loaded() {
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" 1 bpf_leak_collect_and_check_ready \
		|| fatal "expected loaded and attached BPF with stable IDs; actual: $(< "${BPF_LEAK_DIR}/owned.json")"
	log_info "Agent BPF inventory: $(jq -c . "${BPF_LEAK_DIR}/owned.json")"
}

assert_bpf_instance_exited() {
	local status=0
	wait_until "${BPF_LEAK_EXIT_TIMEOUT}" 0.1 process_exited "${bpf_leak_observer_pid}" \
		|| fatal "expected Agent and all traced descendants to exit within ${BPF_LEAK_EXIT_TIMEOUT}s"
	wait "${bpf_leak_observer_pid}" || status=$?
	jq -e --argjson root "$(< "${BPF_LEAK_DIR}/observation/agent.pid")" \
		'.complete == true and .sigterm_forwarded == true and .root_pid == $root' \
		"${BPF_LEAK_DIR}/observation/complete.json" > /dev/null \
		|| fatal "BPF observation incomplete; see ${BPF_LEAK_DIR}/observation and huatuo.log"
	log_info "Agent and descendants exited after SIGTERM: status=${status}"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	bpf_leak_observer_pid=""
}

assert_bpf_reclaimed() {
	local deadline=$1 remaining
	bpf_leak_write_remaining after
	bpf_leak_write_remaining_ids
	(($(date +%s) < deadline)) || fatal "BPF verification exceeded its reclamation deadline"
	if jq -e 'all(.[]; length == 0)' "${BPF_LEAK_DIR}/remaining.json" > /dev/null; then
		return 0
	fi
	remaining=$((deadline - $(date +%s)))
	if ((remaining <= 0)) || ! wait_until "${remaining}" 1 bpf_leak_check_reclaimed "${deadline}"; then
		fatal "expected Agent BPF reclamation within ${BPF_LEAK_RECLAIM_TIMEOUT}s; remaining: $(< "${BPF_LEAK_DIR}/remaining.json")"
	fi
	(($(date +%s) < deadline)) || fatal "BPF verification exceeded its reclamation deadline"
}

assert_bpf_foreign_resources_alive() {
	local kind
	local kinds=(map prog)
	if "${bpf_link_enumeration_available}"; then
		kinds+=(link)
	fi
	for kind in "${kinds[@]}"; do
		bpf_query_pinned "${kind}" "${bpf_leak_foreign_dir}/${kind}" \
			"${BPF_LEAK_DIR}/foreign-${kind}.json"
		jq -e '.id | type == "number" and . > 0' "${BPF_LEAK_DIR}/foreign-${kind}.json" > /dev/null \
			|| fatal "invalid unrelated BPF ${kind} result"
	done
}

mkdir -p "${BPF_LEAK_DIR}/observation"
echo '{}' > "${BPF_LEAK_DIR}/owned.json"
bpf_build_observer_tools "${BPF_LEAK_DIR}"
printf '#!/usr/bin/env bash\nexec %q %q -- %q "$@"\n' \
	"${BPF_LEAK_DIR}/observer" "${BPF_LEAK_DIR}/observation" "${BPF_LEAK_AGENT_BIN}" \
	> "${BPF_LEAK_DIR}/observed-agent.sh"
chmod +x "${BPF_LEAK_DIR}/observed-agent.sh"
bpf_probe_link_enumeration "${BPF_LEAK_DIR}"
bpf_snapshot "${BPF_LEAK_DIR}/before"

HUATUO_BAMAI_BIN="${BPF_LEAK_DIR}/observed-agent.sh" \
	BAMAI_HTTP_PORT="${BPF_LEAK_PORT}" integration_huatuo_bamai_start write_bamai_http_config
bpf_leak_observer_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
assert_bpf_loaded

# These pins are created outside the observed process tree, after the baseline.
bpf_leak_foreign_dir=$(bpf_create_pin_directory) || fatal "cannot allocate unrelated BPF pins"
"${BPF_LEAK_DIR}/workload" map "${bpf_leak_foreign_dir}"
"${BPF_LEAK_DIR}/workload" prog "${bpf_leak_foreign_dir}"
if "${bpf_link_enumeration_available}"; then
	"${BPF_LEAK_DIR}/workload" link "${bpf_leak_foreign_dir}"
fi

# The observer forwards SIGTERM while the Agent identity is still held by its parent.
kill -TERM "${bpf_leak_observer_pid}" || fatal "cannot request Agent SIGTERM"
assert_bpf_instance_exited
bpf_leak_reclaim_deadline=$(($(date +%s) + BPF_LEAK_RECLAIM_TIMEOUT))
bpf_leak_freeze_candidates

bpf_snapshot "${BPF_LEAK_DIR}/after" "${bpf_leak_reclaim_deadline}"
assert_bpf_reclaimed "${bpf_leak_reclaim_deadline}"
assert_bpf_foreign_resources_alive
log_info "Agent BPF resources were reclaimed while unrelated pinned resources remained"
