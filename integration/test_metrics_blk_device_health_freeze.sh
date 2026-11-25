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

# Check the public freeze Counter on the configured, exclusively owned scratch disk.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/config.sh"
require_commands awk jq timeout stat
readonly FREEZE_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/freeze"
WAIT_HUATUO_BAMAI_TIMEOUT=30
freeze_disk=""
freeze_queue=""
freeze_requests_original=""
freeze_requests_pending=0
freeze_work_dir=${FREEZE_WORK}
mkdir -p "${FREEZE_WORK}"

freeze_target_is_owned() {
	[[ -n ${freeze_disk} && /dev/${freeze_disk} -ef ${TEST_IO_DISK} &&
		$(stat -Lc '%d:%i' "/sys/block/${freeze_disk}") == "${io_test_disk_identity}" ]] \
		&& io_test_disk_is_free "${freeze_disk}"
}

cleanup_freeze() {
	local status=$? failed=0
	trap - EXIT
	trap '' INT TERM
	set +e
	huatuo_bamai_stop || failed=1
	restore_freeze_queue || failed=1
	if ((failed == 0)); then io_test_cleanup_disks || failed=1; fi
	printf 'cleanup_failed=%s execution_status=%s\n' "${failed}" "${status}" >> "${FREEZE_WORK}/resources.txt"
	if ((failed)); then
		log_error "freeze cleanup failed; inspect ${FREEZE_WORK}/resources.txt"
		status=1
	fi
	exit "${status}"
}
trap cleanup_freeze EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

restore_freeze_queue() {
	((freeze_requests_pending)) || return 0
	if ! freeze_target_is_owned || [[ ${freeze_queue} != /sys/block/${freeze_disk}/queue/nr_requests ]] \
		|| ! timeout --kill-after=2s 5s bash -c 'printf "%s\n" "$2" > "$1"' _ \
			"${freeze_queue}" "${freeze_requests_original}" \
		|| [[ $(< "${freeze_queue}") != "${freeze_requests_original}" ]]; then
		log_error "H05 cannot restore owned queue ${freeze_queue} to ${freeze_requests_original}; retaining its devices"
		return 1
	fi
	freeze_requests_pending=0
	printf 'freeze_restored=%s\n' "${freeze_requests_original}" >> "${FREEZE_WORK}/resources.txt"
}

freeze_counter_read() {
	local phase=$1 input="${freeze_work_dir}/freeze-${1}.metrics"
	huatuo_bamai_metrics > "${input}" || {
		printf 'H05 cannot scrape freeze Counter during %s; inspect %s.\n' "$phase" "$input" >&2
		return 1
	}
	io_test_metrics_json "${input}" > "${input}.json" || {
		printf 'H05 invalid metric exposition during %s; inspect %s.\n' "$phase" "$input" >&2
		return 1
	}
	freeze_count=$(jq -er --arg disk "${freeze_disk}" '
		[.[] | select(.name == "huatuo_bamai_blk_device_health_freeze_total")] as $samples
		| if all($samples[];
			.type == "counter" and .type_count == 1 and .label_count == 3
			and (.labels | keys) == ["disk", "hostname", "region"]
			and (.labels.disk | length > 0) and .labels.hostname == "huatuo-dev" and .labels.region == "dev"
			and (.raw_value | test("^[+]?[0-9]+([.][0-9]*)?([eE][+-]?[0-9]+)?$"))
			and (.value | type == "number") and (.value | isinfinite | not)
			and .value >= 0 and .value == (.value | floor))
			and all($samples | group_by(.labels)[]; length == 1)
		then [$samples[] | select(.labels.disk == $disk) | .value] | .[0] // 0
		else error("invalid freeze Counter type, labels or count") end
	' "${input}.json") || {
		printf 'H05 invalid freeze Counter during %s; inspect %s.\n' "$phase" "$input" >&2
		return 1
	}
}

assert_freeze_restored_and_idle() {
	local changed=$1 restored attempt
	# Restoration can freeze again; compare later scrapes with its cumulative value.
	freeze_counter_read restored || return 1
	restored=${freeze_count}
	awk -v value="${restored}" -v before="${changed}" 'BEGIN { exit !(value >= before) }' \
		|| {
			printf 'H05 freeze Counter regressed after restoring nr_requests: before=%s, after=%s.\n' "$changed" "$restored" >&2
			return 1
		}
	# Queue restoration has completed; each scrape reads the current BPF map.
	for attempt in 1 2; do
		freeze_counter_read "idle-${attempt}" || return 1
		[[ ${freeze_count} == "${restored}" ]] \
			|| {
				printf 'H05 idle scrape %s changed the freeze Counter: expected=%s actual=%s; inspect %s.\n' \
					"$attempt" "$restored" "$freeze_count" "${freeze_work_dir}/freeze-idle-${attempt}.metrics" >&2
				return 1
			}
	done
}

freeze_counter_ready() {
	freeze_attempt=$((freeze_attempt + 1))
	freeze_counter_read "changed-${freeze_attempt}" || fatal "cannot read freeze Counter during qualification"
	if awk -v value="$freeze_count" -v before="$freeze_before" 'BEGIN { exit !(value > before) }'; then return 0; fi
	freeze_target_is_owned || fatal "H05 target ownership changed during startup qualification"
	timeout --kill-after=2s 5s bash -c 'printf "%s\n" "$2" > "$1"' _ "${freeze_queue}" "${freeze_next}" \
		|| fatal "H05 owned queue change failed during startup qualification"
	[[ $(< "${freeze_queue}") == "${freeze_next}" ]] || fatal "H05 queue readback differs during startup qualification"
	if [[ ${freeze_next} == "${freeze_changed}" ]]; then freeze_next=${freeze_requests_original}; else freeze_next=${freeze_changed}; fi
	return 1
}

test_freeze_counter() {
	local freeze_changed freeze_before freeze_attempt=0 freeze_next
	freeze_queue="/sys/block/${freeze_disk}/queue/nr_requests"
	freeze_target_is_owned || fatal "H05 refusing queue change after target ownership changed"
	if [[ ! -r ${freeze_queue} || ! -w ${freeze_queue} ]]; then
		log_info "NOT_RUN H05: owned disk has no writable nr_requests control"
		return
	fi
	if ! read -r freeze_requests_original < "${freeze_queue}"; then
		log_info "NOT_RUN H05: owned queue nr_requests cannot be read"
		return
	fi
	if [[ ! ${freeze_requests_original} =~ ^[0-9]+$ ]] || ((freeze_requests_original < 8)); then
		log_info "NOT_RUN H05: nr_requests=${freeze_requests_original} has no safe smaller test value"
		return
	fi
	freeze_changed=$((freeze_requests_original / 2))
	freeze_counter_read baseline || fatal "cannot read freeze Counter baseline"
	freeze_before=${freeze_count}
	printf 'freeze_queue=%s original=%s requested=%s\n' \
		"${freeze_queue}" "${freeze_requests_original}" "${freeze_changed}" >> "${FREEZE_WORK}/resources.txt"
	# A timed-out write may have changed the queue before its status arrives.
	freeze_requests_pending=1
	freeze_target_is_owned || fatal "H05 target ownership changed before queue update"
	if ! timeout --kill-after=2s 5s bash -c 'printf "%s\n" "$2" > "$1"' _ "${freeze_queue}" "${freeze_changed}" \
		|| [[ $(< "${freeze_queue}") != "${freeze_changed}" ]]; then
		restore_freeze_queue || fatal "H05 queue restoration failed after rejected change"
		log_info "NOT_RUN H05: owned queue did not accept a different nr_requests value"
		return
	fi
	# HTTP readiness does not imply that the block probes have attached. Keep
	# supplying real queue changes until the public Counter proves readiness.
	freeze_next=${freeze_requests_original}
	wait_until 30 0.2 freeze_counter_ready \
		|| fatal "H05 confirmed queue changes did not increase the freeze Counter within 30s"
	freeze_changed=${freeze_count}
	restore_freeze_queue || fatal "H05 queue restoration failed after freeze check"
	assert_freeze_restored_and_idle "${freeze_changed}" || fatal "freeze Counter did not settle after restoration"
	log_info "H05: owned queue change increases freeze Counter and idle scrapes preserve it"
}

io_test_prepare_disks
freeze_disk=${io_test_disk_name}
io_test_start_bamai write_config_bamai_blk_device_health --region dev --disable-kubelet --log-debug
test_freeze_counter
