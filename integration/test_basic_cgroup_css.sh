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

# Verify cgroup CSS sync and lifecycle events across the BPF ABI boundary.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly PROBE_SOURCE="${ROOT_DIR}/integration/testdata/test_cgroup_css_probe.go"
readonly PROBE_BIN="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-css-probe"
readonly PROBE_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-css-probe.log"
readonly PROBE_BUILD_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-css-probe-build.log"
readonly EVENT_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-css-events.jsonl"
readonly VALID_EVENTS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/valid-cgroup-css-events.json"
readonly READY_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-css-probe.ready"
readonly GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"
readonly SYNC_ID="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
readonly EVENT_ID="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
readonly PROBE_READY_TIMEOUT=15
readonly PROBE_READY_INTERVAL=0.1

probe_pid=""
cgroup_parent=""

cleanup() {
	if [[ -n "${probe_pid}" ]]; then
		kill "${probe_pid}" 2> /dev/null || true
		wait "${probe_pid}" 2> /dev/null || true
	fi
	rm -rf -- "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
	[[ -n "${cgroup_parent}" ]] || return 0
	rmdir "${cgroup_parent}/${EVENT_ID}" 2> /dev/null || true
	rmdir "${cgroup_parent}/${SYNC_ID}" 2> /dev/null || true
	rmdir "${cgroup_parent}" 2> /dev/null || true
}
trap cleanup EXIT

[[ $EUID -eq 0 ]] || skip "requires root"

command -v go > /dev/null || skip "go command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
[[ -r /proc/sys/kernel/random/uuid ]] \
	|| skip "kernel UUID source is not readable: /proc/sys/kernel/random/uuid"
[[ -r /sys/kernel/btf/vmlinux ]] \
	|| skip "kernel BTF is not readable: /sys/kernel/btf/vmlinux"
[[ -r "${ROOT_DIR}/_output/bpf/cgroup_css_sync.o" ]] \
	|| fatal "cgroup CSS sync BPF object not found"
[[ -r "${ROOT_DIR}/_output/bpf/cgroup_css_events.o" ]] \
	|| fatal "cgroup CSS events BPF object not found"

tracepoint_available cgroup cgroup_mkdir \
	|| skip "cgroup/cgroup_mkdir tracepoint is not available"
tracepoint_available cgroup cgroup_rmdir \
	|| skip "cgroup/cgroup_rmdir tracepoint is not available"

if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
	readonly CGROUP_ROOT=/sys/fs/cgroup
	readonly SYNC_SYMBOL=memory_current_read
	readonly NOTIFY_FILE=memory.current
elif [[ -d /sys/fs/cgroup/memory ]]; then
	readonly CGROUP_ROOT=/sys/fs/cgroup/memory
	readonly SYNC_SYMBOL=cgroup_clone_children_read
	readonly NOTIFY_FILE=cgroup.clone_children
else
	skip "supported cgroup v1/v2 hierarchy is not available"
fi

kprobe_available "${SYNC_SYMBOL}" \
	|| skip "${SYNC_SYMBOL} is not available for kprobe"

parent_uuid=$(< /proc/sys/kernel/random/uuid)
cgroup_parent="${CGROUP_ROOT}/huatuo-css-test-${parent_uuid}"
if ! mkdir "${cgroup_parent}" 2> "${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-mkdir.log"; then
	skip "cgroup hierarchy is not writable: ${CGROUP_ROOT}"
fi
if [[ ${SYNC_SYMBOL} == memory_current_read ]]; then
	# Enable memory so the child exposes memory.current.
	echo +memory > "${cgroup_parent}/cgroup.subtree_control" \
		|| skip "memory controller cannot be enabled in test cgroup"
fi
mkdir "${cgroup_parent}/${SYNC_ID}"
[[ -r "${cgroup_parent}/${SYNC_ID}/${NOTIFY_FILE}" ]] \
	|| skip "cgroup CSS notification file is not readable: ${cgroup_parent}/${SYNC_ID}/${NOTIFY_FILE}"

mkdir -p "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
GOCACHE="${GO_CACHE_DIR}" \
	GOTMPDIR="${GO_TMP_DIR}" \
	go build -mod=vendor -tags=integration -o "${PROBE_BIN}" "${PROBE_SOURCE}" \
	> "${PROBE_BUILD_LOG}" 2>&1 \
	|| fatal "failed to build cgroup CSS probe:"$'\n'"$(< "${PROBE_BUILD_LOG}")"

"${PROBE_BIN}" \
	"${ROOT_DIR}/_output/bpf" \
	"${cgroup_parent}/${SYNC_ID}/${NOTIFY_FILE}" \
	"${EVENT_ID}" \
	"${SYNC_SYMBOL}" \
	"${READY_FILE}" \
	> "${EVENT_FILE}" 2> "${PROBE_LOG}" &
probe_pid=$!

wait_until "${PROBE_READY_TIMEOUT}" "${PROBE_READY_INTERVAL}" \
	test -f "${READY_FILE}" \
	|| fatal "cgroup CSS probe did not attach lifecycle hooks"

mkdir "${cgroup_parent}/${EVENT_ID}"
rmdir "${cgroup_parent}/${EVENT_ID}"

if ! wait "${probe_pid}"; then
	probe_pid=""
	fatal "cgroup CSS probe failed"
fi
probe_pid=""

jq -s -e \
	--arg sync_id "${SYNC_ID}" \
	--arg event_id "${EVENT_ID}" '
    . as $events
    | (
        ([$events[] | [.source, .operation, .knode_name]] | sort) == ([
          ["sync", "update", $sync_id],
          ["events", "update", $event_id],
          ["events", "remove", $event_id]
        ] | sort)
        and all($events[];
          (.cgroup | type == "string")
          and (.cgroup | test("^0x[1-9a-f][0-9a-f]*$"))
          and (.cgroup_root | type == "number")
          and (.cgroup_level | type == "number") and .cgroup_level > 0
          and (.css_count | type == "number") and .css_count > 0
        )
        and ([$events[] | select(.source == "events") | .cgroup]
          | unique | length == 1)
      )
    | select(.)
    | [$events[] | select(.source == "events")]
  ' "${EVENT_FILE}" > "${VALID_EVENTS_FILE}" \
	|| fatal "cgroup CSS probe output failed semantic validation"

log_info "basic cgroup CSS test passed"
log_info "valid cgroup CSS lifecycle events:"
jq '.' "${VALID_EVENTS_FILE}"
