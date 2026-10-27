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

# Exercise UDP drops through Dropwatch, the daemon and LocalFile rotation.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly STORAGE_ROTATION_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/dropwatch"
readonly STORAGE_ROTATION_BATCH_SIZE=128
readonly STORAGE_ROTATION_MIB=1
readonly STORAGE_MAX_ROTATED_FILES=2
readonly STORAGE_EVENT_RATE=1000
readonly STORAGE_ROTATION_SIZE_BYTES=$((STORAGE_ROTATION_MIB * 1024 * 1024))
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands jq ss curl stat
require_readable /sys/kernel/btf/vmlinux
tracepoint_available skb kfree_skb || skip "skb/kfree_skb tracepoint is unavailable"
integration_isolate_run_directory "$@"

storage_http_port=$(allocate_available_port) || fatal "failed to allocate bamai HTTP port"
storage_target_port=$(allocate_available_port udp) || fatal "failed to allocate UDP target port"
HUATUO_BAMAI_ADDR="http://127.0.0.1:${storage_http_port}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

# Keep this LocalFile-only case independent of inherited backend credentials.
STORAGE_ADDR=""
STORAGE_USERNAME=""
STORAGE_PASSWORD=""

storage_rotation_first_backup=""
storage_rotation_backups=()
trap huatuo_bamai_stop EXIT
shopt -s nullglob

storage_rotation_check_or_send() {
	storage_rotation_backups=("${STORAGE_ROTATION_EVENT}"-*)
	if [[ -z ${storage_rotation_first_backup} && ${#storage_rotation_backups[@]} -gt 0 ]]; then
		storage_rotation_first_backup=${storage_rotation_backups[0]}
	fi
	if [[ -n ${storage_rotation_first_backup} && ! -e ${storage_rotation_first_backup} &&
		${#storage_rotation_backups[@]} -eq ${STORAGE_MAX_ROTATED_FILES} ]]; then
		return 0
	fi
	# Keep supplying input until rotation also evicts an observed backup.
	storage_send_udp_packets "${storage_target_port}" "${STORAGE_ROTATION_BATCH_SIZE}"
	return 1
}

assert_storage_rotation_file() {
	local file=$1 size actual
	size=$(stat -c %s "${file}") || fatal "failed to read rotated file size: ${file}"
	((size > 0 && size <= STORAGE_ROTATION_SIZE_BYTES)) \
		|| fatal "rotated file size: ${file}: expected 1..${STORAGE_ROTATION_SIZE_BYTES} bytes, actual=${size}"
	actual=$(jq -s -e --argjson port "${storage_target_port}" 'length > 0 and all(.[];
		if .tracer_name == "dropwatch" and .tracer_type == "event"
			and .tracer_data.layers.ipv4.daddr == "127.0.0.99"
			and .tracer_data.layers.udp.dport == $port then true
		else error({tracer_name, tracer_type,
			daddr: .tracer_data.layers.ipv4.daddr,
			dport: .tracer_data.layers.udp.dport} | tostring)
		end)' "${file}" 2>&1) \
		|| fatal "persisted events: ${file}: expected nonempty dropwatch/event records for 127.0.0.99:${storage_target_port}, actual=${actual}"
}

assert_storage_rotation_log() {
	local log_file="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	[[ -r "${log_file}" ]] || fatal "huatuo-bamai log is not readable: ${log_file}"
	# XID timestamps can contain "fatal" inside successful debug records.
	assert_huatuo_bamai_log_clean || fatal "huatuo-bamai logged an error during local storage rotation"
}

integration_huatuo_bamai_start write_storage_events_config \
	--region integration --disable-kubelet --log-debug
wait_until 120 0.1 storage_rotation_check_or_send \
	|| fatal "rotation did not evict first backup: first=${storage_rotation_first_backup:-none}, backups=${#storage_rotation_backups[@]}, expected=${STORAGE_MAX_ROTATED_FILES}"

# Stop writes before checking complete JSON records and final file sizes.
assert_huatuo_bamai_shutdown
storage_rotation_files=("${STORAGE_ROTATION_EVENT}" "${STORAGE_ROTATION_EVENT}"-*)
assert_eq "${#storage_rotation_files[@]}" "$((STORAGE_MAX_ROTATED_FILES + 1))" "local event file count" \
	|| fatal "expected one active file and ${STORAGE_MAX_ROTATED_FILES} backups"
for storage_rotation_file in "${storage_rotation_files[@]}"; do
	assert_storage_rotation_file "${storage_rotation_file}"
done
assert_storage_rotation_log
jq -s -c '.[-1] | {tracer_name, tracer_type, layers: .tracer_data.layers}' "${STORAGE_ROTATION_EVENT}"
log_info "local storage evicted its first backup and retained ${STORAGE_MAX_ROTATED_FILES} backups"
