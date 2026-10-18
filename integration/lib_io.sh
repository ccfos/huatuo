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

# TEST_IO_DISK is a dedicated scratch device; tests may overwrite its contents.
export TEST_IO_DISK=${TEST_IO_DISK-/dev/disk/by-id/virtio-huatuo-ci-io}

io_test_require_disk() {
	[[ -b ${TEST_IO_DISK} ]] || fatal "test disk is missing: TEST_IO_DISK=${TEST_IO_DISK}; configure a dedicated scratch block device"
}

assert_io_fio_workload() {
	local output=$1 operation=$2 size=$3 count=$4
	jq -e --arg operation "${operation}" --argjson count "${count}" --argjson bytes "$((size * count))" '
		.jobs | length == 1 and .[0].error == 0 and .[0][$operation].total_ios == $count
		and .[0][$operation].io_bytes == $bytes
	' "${output}" > /dev/null || fatal "fio did not complete the requested ${operation} workload: ${output}"
}

assert_io_disk_activity() {
	local operation=$1 device=$2 before=$3 after=$4
	((after > before)) || fatal "disk statistics did not confirm completed ${operation} IO on ${device}"
}

io_test_disks=()
io_test_scsi_owned=0
io_test_scsi_owner=""
io_test_disk_writable=0
io_test_disk_exclusive=0
io_test_disk_identity=""
io_test_disk_name=""
io_test_storage_dir=""
io_test_cost_files=()
io_test_cost_settings=()
io_test_iotracing_ready_timeout=60

# Reuse the runner's host-service handoff while keeping daemon locks private.
io_test_isolate_run_directory() {
	[[ /var/run -ef ${HUATUO_BAMAI_TEST_TMPDIR}/run ]] && return 0
	[[ ${EUID} -eq 0 ]] || skip "IO daemon isolation requires root"
	require_commands unshare mount
	exec unshare --mount --propagation private bash "${ROOT_DIR}/integration/exec_case.sh" "$0"
}

io_test_start_bamai() {
	local io_test_config_writer=${1:-write_config_bamai_default}
	(($# == 0)) || shift
	[[ /var/run -ef ${HUATUO_BAMAI_TEST_TMPDIR}/run ]] \
		|| fatal "IO daemon runtime is not isolated; call io_test_isolate_run_directory before preparing resources"
	# Shared startup truncates its log and cleanup removes its PID file.
	io_test_bamai_start=$((${io_test_bamai_start:-0} + 1))
	io_test_bamai_log_dir="${HUATUO_BAMAI_TEST_TMPDIR}/io-daemon-${io_test_bamai_start}"
	mkdir "${io_test_bamai_log_dir}"
	ln -sfn "${io_test_bamai_log_dir}/bamai.conf" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	ln -sfn "${io_test_bamai_log_dir}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	ln -sfn "${io_test_bamai_log_dir}/huatuo-bamai.pid" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	printf 'start=%s time=%s config_writer=%s\n' \
		"${io_test_bamai_start}" "$(date -u +%FT%TZ)" "${io_test_config_writer}" \
		> "${io_test_bamai_log_dir}/resources.txt"
	integration_huatuo_bamai_start "${io_test_config_writer}" "$@"
	printf 'address=%s\n' "${HUATUO_BAMAI_ADDR}" >> "${io_test_bamai_log_dir}/resources.txt"
	printf 'address=%s runtime=%s/run\n' "${HUATUO_BAMAI_ADDR}" "${HUATUO_BAMAI_TEST_TMPDIR}" \
		>> "${HUATUO_BAMAI_TEST_TMPDIR}/io-runtime.resources"
}

io_test_report_daemon_logs() {
	local directory id
	local -a patterns=()
	for id in "$@"; do patterns+=(-e "${id}"); done
	for directory in "${HUATUO_BAMAI_TEST_TMPDIR}"/io-daemon-*; do
		[[ -f ${directory}/huatuo.log ]] || continue
		log_error "IO daemon history: ${directory} pid=$(cat "${directory}/huatuo-bamai.pid" 2> /dev/null || echo unknown)"
		cat "${directory}/resources.txt" >&2
		# Emit relevant late records to CI; the runner truncates other artifacts.
		grep -Ei 'level="?(error|warning|fatal)|panic|lost.*sample|drop.*sample' "${directory}/huatuo.log" \
			| tail -n 60 >&2 || true
		if ((${#patterns[@]})); then
			grep -F "${patterns[@]}" "${directory}/huatuo.log" | tail -n 80 >&2 || true
		else
			tail -n 80 "${directory}/huatuo.log" >&2
		fi
	done
}

# Isolate each IO fixture and wait for its owned resources to be released before
# starting the next one. Failed and interrupted workspaces retain their records.
io_test_run_cases() {
	local script=$1 selector=$2 name workspace io_case_pid="" status=0 io_case_signal=0 passed=0
	shift 2
	require_commands setsid unshare
	local io_case_root
	io_case_root=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/io-cases.XXXXXX")
	local HUATUO_BAMAI_TEST_TMPDIR=${io_case_root}
	log_info "IO fixture resources: ${HUATUO_BAMAI_TEST_TMPDIR}"
	trap 'if ((io_case_signal == 0)); then io_case_signal=130; [[ -z ${io_case_pid} ]] || kill -TERM "${io_case_pid}" 2>/dev/null || true; fi' INT
	trap 'if ((io_case_signal == 0)); then io_case_signal=143; [[ -z ${io_case_pid} ]] || kill -TERM "${io_case_pid}" 2>/dev/null || true; fi' TERM
	for name in "$@"; do
		((io_case_signal == 0)) || break
		workspace=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.XXXXXX")
		log_info "IO scenario ${name}: ${workspace}"
		printf 'scenario=%s workspace=%s\n' "${name}" "${workspace}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-cases.resources"
		HUATUO_BAMAI_TEST_TMPDIR="${workspace}" setsid env "${selector}=${name}" \
			unshare --mount --propagation private bash "${script}" &
		io_case_pid=$!
		printf 'scenario=%s pid=%s\n' "${name}" "${io_case_pid}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-cases.resources"
		((io_case_signal == 0)) || kill -TERM "${io_case_pid}" 2> /dev/null || true
		while true; do
			if wait "${io_case_pid}"; then status=0; else status=$?; fi
			kill -0 "${io_case_pid}" 2> /dev/null || break
			if ! wait_until 30 0.1 process_exited "${io_case_pid}" > "${workspace}/cleanup-wait.log" 2>&1; then
				tail -n 10 "${workspace}/cleanup-wait.log" >&2
				log_error "IO cleanup still running: pid=${io_case_pid} workspace=${workspace}; retaining resource records"
				trap - INT TERM
				return 1
			fi
		done
		io_case_pid=""
		if ((io_case_signal)) && [[ ${status} == 0 || ${status} == 77 || ${status} == 130 || ${status} == 143 ]]; then status=${io_case_signal}; fi
		integration_test_exit "${status}" "${workspace}" || status=1
		if ((io_case_signal)) && [[ ${status} == 0 || ${status} == 77 ]]; then status=${io_case_signal}; fi
		printf 'scenario=%s exit=%s\n' "${name}" "${status}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-cases.resources"
		case ${status} in
		0)
			passed=$((passed + 1))
			log_info "PASS IO scenario ${name}"
			;;
		77) log_info "SKIP IO scenario ${name}" ;;
		*) break ;;
		esac
	done
	if ((io_case_signal)) && [[ ${status} == 0 || ${status} == 77 ]]; then status=${io_case_signal}; fi
	trap - INT TERM
	[[ ${status} == 0 || ${status} == 77 ]] || return "${status}"
	rm -f -- "${HUATUO_BAMAI_TEST_TMPDIR}/io-cases.resources"
	rmdir -- "${HUATUO_BAMAI_TEST_TMPDIR}"
	((passed)) || skip "all selected IO scenarios lack their prerequisites"
}

# Kernel users can outlive an exited workload briefly; directory removal is the
# completion condition before the same cgroup path may be reused.
io_test_remove_cgroup() {
	local directory=$1
	[[ -n ${directory} && -d ${directory} ]] || return 0
	if ! wait_until 5 0.1 timeout --kill-after=1 1 bash -c '[[ ! -d $1 ]] || rmdir "$1"' _ "${directory}"; then
		log_error "owned cgroup remains: ${directory}; inspect its processes and child groups"
		[[ ! -r ${directory}/cgroup.procs ]] || timeout --kill-after=1 2 cat "${directory}/cgroup.procs" >&2
		return 1
	fi
}

io_test_record_resources() {
	[[ -n ${HUATUO_BAMAI_TEST_TMPDIR:-} ]] || return 0
	printf 'stage=%s scsi_owner=%s scsi_owned=%s writable=%s disks=%s\n' \
		"$1" "${io_test_scsi_owner}" "${io_test_scsi_owned}" \
		"${io_test_disk_writable}" "${io_test_disks[*]}" \
		>> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources"
}

io_test_disk_is_free() {
	local name=$1 mounts holder block="/sys/block/$1"
	[[ ${name} =~ ^[a-zA-Z0-9]+$ && -b /dev/${name} && -d ${block} ]] || {
		log_error "disk preflight: /dev/${name} is not a whole block device; configure a dedicated whole disk"
		return 1
	}
	mounts=$(lsblk -nr -o MOUNTPOINT "/dev/${name}") || {
		log_error "disk preflight: cannot query mountpoints for /dev/${name}"
		return 1
	}
	[[ -z ${mounts//[[:space:]]/} ]] || {
		log_error "disk preflight: /dev/${name} has mounted filesystems: ${mounts}"
		return 1
	}
	for holder in "${block}/holders/"* "${block}/${name}"*/holders/*; do
		[[ -e ${holder} || -L ${holder} ]] || continue
		log_error "disk preflight: /dev/${name} has a dependent device: ${holder}"
		return 1
	done
	return 0
}

io_test_scsi_disks_ready() {
	local count=$1 block model
	io_test_disks=()
	for block in /sys/block/sd*; do
		[[ -r ${block}/device/model && -b /dev/${block##*/} && -w ${block}/queue/iostats ]] || continue
		read -r model < "${block}/device/model"
		[[ ${model} == scsi_debug ]] && io_test_disks+=("${block##*/}")
	done
	((${#io_test_disks[@]} == count))
}

# Keep the queue open while checking identity so a reused device name cannot
# redirect this owned-disk write to another queue.
io_test_enable_owned_disk_stats() {
	local name=$1 block="/sys/block/$1" identity queue vendor model major minor status=0
	identity=$(stat -Lc '%d:%i' "${block}") || return 1
	exec {queue}> "${block}/queue/iostats" || return 1
	if ! read -r vendor < "${block}/device/vendor" \
		|| ! read -r model < "${block}/device/model" \
		|| [[ ${vendor} != "${io_test_scsi_owner}" || ${model} != scsi_debug ]] \
		|| ! read -r major minor < <(stat -Lc '%t %T' "/dev/${name}") \
		|| [[ ! -b /dev/${name} || $(< "${block}/dev") != "$((16#${major})):$((16#${minor}))" ]] \
		|| [[ $(stat -Lc '%d:%i' "${block}") != "${identity}" ]]; then
		exec {queue}>&-
		return 1
	fi
	printf '1\n' >&"${queue}" || status=$?
	exec {queue}>&-
	return "${status}"
}

io_test_report_disk_lock() {
	local path=/run/lock/huatuo-kernel-tests.lock inode locks
	local snapshot="${HUATUO_BAMAI_TEST_TMPDIR}/io-disk-locks.txt"
	printf 'topology_lock=%s time=%s pid=%s\n' "${path}" "$(date -u +%FT%TZ)" "${BASHPID}"
	stat -Lc 'device=%d inode=%i' "${path}" || return 1
	inode=$(stat -Lc '%i' "${path}") || return 1
	timeout --kill-after=1 2 awk -v inode="${inode}" '$(NF - 2) ~ (":" inode "$")' /proc/locks > "${snapshot}" || {
		printf '/proc/locks read_exit=%s\n' "$?"
		return 1
	}
	locks=$(< "${snapshot}")
	printf '%s\n' "${locks}"
	if [[ -n ${locks} ]] && command -v fuser > /dev/null && command -v timeout > /dev/null; then
		timeout --kill-after=1 2 fuser -v "${path}" || true
	fi
}

io_test_lock_disks() {
	local limit=${1:-300} started=${SECONDS} status=0
	require_commands flock
	[[ -z ${io_health_md_lock_fd:-} ]] || return 0
	mkdir -p /run/lock
	[[ ! -L /run/lock/huatuo-kernel-tests.lock ]] || fatal "shared kernel device lock must not be a symlink"
	exec {io_health_md_lock_fd}<> /run/lock/huatuo-kernel-tests.lock
	log_info "IO topology lock requested: pid=${BASHPID} fd=${io_health_md_lock_fd} budget_seconds=${limit}"
	io_test_report_disk_lock >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources" 2>&1 || true
	flock --exclusive --timeout "${limit}" "${io_health_md_lock_fd}" || status=$?
	printf 'lock_exit=%s wait_seconds=%s pid=%s fd=%s time=%s\n' \
		"${status}" "$((SECONDS - started))" "${BASHPID}" "${io_health_md_lock_fd}" "$(date -u +%FT%TZ)" \
		>> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources"
	if ((status != 0)); then
		io_test_report_disk_lock >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources" 2>&1 || true
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources" >&2
		fatal "cannot acquire shared kernel device lock within ${limit} seconds (exit ${status})"
	fi
	log_info "IO topology lock acquired: pid=${BASHPID} fd=${io_health_md_lock_fd} wait_seconds=$((SECONDS - started))"
}

assert_io_test_disk_identity() {
	local name=$1 owner
	if [[ ${name} == "${io_test_disk_name}" ]]; then
		[[ -b ${TEST_IO_DISK} && $(readlink -f "${TEST_IO_DISK}") == "/dev/${name}" &&
		$(stat -Lc '%d:%i' "/sys/block/${name}") == "${io_test_disk_identity}" ]]
	else
		((io_test_scsi_owned)) || return 1
		read -r owner < "/sys/block/${name}/device/vendor" || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]]
	fi
}

io_test_prepare_disks() {
	local name sectors devices attribute value iostats
	io_test_require_disk
	require_commands readlink lsblk stat
	io_test_lock_disks
	name=$(readlink -f "${TEST_IO_DISK}") || fatal "cannot resolve TEST_IO_DISK=${TEST_IO_DISK}"
	name=${name##*/}
	{
		printf 'stage=preflight configured_disk=%s resolved_disk=/dev/%s\n' "${TEST_IO_DISK}" "${name}"
		# Activity is diagnostic here; workloads own completion and idle checks.
		for attribute in stat inflight; do
			if read -r value 2> /dev/null < "/sys/block/${name}/${attribute}"; then
				printf '%s=%s\n' "${attribute}" "${value}"
			else
				printf '%s=unavailable\n' "${attribute}"
			fi
		done
	} >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources"
	io_test_disk_is_free "${name}" \
		|| fatal "disk preflight failed: TEST_IO_DISK=${TEST_IO_DISK} resolved=/dev/${name}; inspect io-disks.resources"
	devices=$(lsblk -nr -o NAME "/dev/${name}") || fatal "cannot query block topology for /dev/${name}"
	[[ ${devices} == "${name}" ]] \
		|| fatal "disk preflight: /dev/${name} has partitions or dependent devices: ${devices}"
	read -r sectors < "/sys/block/${name}/size" || fatal "cannot read /sys/block/${name}/size"
	[[ ${sectors} =~ ^[0-9]+$ ]] && ((sectors >= 262144)) \
		|| fatal "disk preflight: /dev/${name} requires at least 262144 sectors (128 MiB), actual=${sectors}"
	read -r iostats < "/sys/block/${name}/queue/iostats" || fatal "cannot read /sys/block/${name}/queue/iostats"
	[[ ${iostats} != 0 ]] || fatal "disk preflight: /dev/${name} requires queue/iostats enabled, actual=${iostats}"
	io_test_disk_name=${name}
	io_test_disk_identity=$(stat -Lc '%d:%i' "/sys/block/${name}")
	io_test_disks=("${name}")
	io_test_disk_writable=1
	io_test_disk_exclusive=1
	log_info "using configured IO test disk: TEST_IO_DISK=${TEST_IO_DISK} device=/dev/${name}"
	io_test_record_resources selected
}

# Keep backing-file IO off the guest system disk. Callers remove their loop/MD
# users first; cleanup refuses to discard a mount that is still busy.
io_test_prepare_storage() {
	local filesystem=${1:-ext4}
	local format_options=()
	require_commands "mkfs.${filesystem}" mount umount mountpoint findmnt timeout
	if [[ -n ${io_test_storage_dir} ]] && mountpoint -q "${io_test_storage_dir}"; then
		[[ $(findmnt -n -o SOURCE --target "${io_test_storage_dir}") -ef ${TEST_IO_DISK} ]] \
			|| fatal "IO backing filesystem no longer belongs to TEST_IO_DISK=${TEST_IO_DISK}"
		return 0
	fi
	io_test_prepare_disks
	io_test_storage_dir="${HUATUO_BAMAI_TEST_TMPDIR}/io-storage"
	mkdir -p "${io_test_storage_dir}"
	printf 'storage_device=%s storage_mount=%s\n' "${TEST_IO_DISK}" "${io_test_storage_dir}" \
		>> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources"
	if [[ ${filesystem} == xfs ]]; then
		mapfile -t format_options < <(io_test_xfs_format_options)
		timeout --kill-after=2s 15s mkfs.xfs -q -f "${format_options[@]}" "${TEST_IO_DISK}"
	else
		timeout --kill-after=2s 15s mkfs.ext4 -q -F -m 0 -E lazy_itable_init=0,lazy_journal_init=0 "${TEST_IO_DISK}"
	fi
	timeout --kill-after=2s 10s mount -t "${filesystem}" "${TEST_IO_DISK}" "${io_test_storage_dir}"
}

io_test_save_cost_settings() {
	local root=$1 prefix=$2 number=$3 field value
	((${#io_test_cost_files[@]} == 0)) || return 0
	for field in model qos; do
		# Numeric parameters select user mode; restore ctrl last.
		value=$(awk -v device="${number}" '$1 == device {
			printf "%s", $1
			for (i = 2; i <= NF; i++) {
				if ($i ~ /^ctrl=/) ctrl = $i
				else printf " %s", $i
			}
			printf " %s\n", ctrl
		}' "${root}/${prefix}.cost.${field}") || return 1
		if [[ -z ${value} ]]; then
			value="${number} ctrl=auto"
			[[ ${field} != qos ]] || value="${number} enable=0 ctrl=auto"
		fi
		io_test_cost_files+=("${root}/${prefix}.cost.${field}")
		io_test_cost_settings+=("${value}")
	done
}

io_test_prepare_scsi_disks() {
	local count=$1 name owner load_status=0 signal_status=0
	io_test_disks=()
	io_test_scsi_owned=0
	io_test_scsi_owner=""
	io_test_disk_writable=0
	io_test_lock_disks

	if [[ ! -e /sys/module/scsi_debug ]] \
		&& command -v modprobe > /dev/null \
		&& command -v modinfo > /dev/null \
		&& modinfo scsi_debug > /dev/null 2>&1; then
		require_commands timeout setsid
		read -r owner < /proc/sys/kernel/random/uuid || fatal "cannot create scsi_debug ownership marker"
		io_test_scsi_owner=${owner%%-*}
		[[ ${io_test_scsi_owner} =~ ^[[:xdigit:]]{8}$ ]] || fatal "invalid scsi_debug ownership marker"
		io_test_record_resources creating
		# Record ownership before allowing interruption to run caller cleanup.
		trap 'signal_status=130' INT
		trap 'signal_status=143' TERM
		setsid timeout --kill-after=1s 10s modprobe --first-time scsi_debug \
			dev_size_mb=128 add_host="${count}" inq_vendor="${io_test_scsi_owner}" || load_status=$?
		printf 'scsi_load_exit=%s\n' "${load_status}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources"
		# A timeout can follow kernel insertion; claim only our marked module.
		if [[ -r /sys/module/scsi_debug/parameters/inq_vendor ]] \
			&& read -r owner < /sys/module/scsi_debug/parameters/inq_vendor \
			&& [[ ${owner} == "${io_test_scsi_owner}" ]]; then
			io_test_scsi_owned=1
		fi
		trap 'exit 130' INT
		trap 'exit 143' TERM
		io_test_record_resources created
		((signal_status == 0)) || return "${signal_status}"
		if ((load_status != 0)); then
			io_test_cleanup_scsi_disks || fatal "failed scsi_debug creation left owned resources; see io-disks.resources"
			((load_status != 124 && load_status != 137)) \
				|| fatal "scsi_debug creation did not finish (exit ${load_status}); inspect io-disks.resources"
			log_warn "scsi_debug creation failed (status ${load_status})"
		else
			((io_test_scsi_owned)) || fatal "cannot verify ownership of scsi_debug test disks"
		fi

		if ((io_test_scsi_owned)); then
			wait_until 10 0.1 io_test_scsi_disks_ready "${count}" \
				|| fatal "scsi_debug did not create ${count} test disks"
			for name in "${io_test_disks[@]}"; do
				io_test_enable_owned_disk_stats "${name}" || fatal "owned disk ${name} identity or iostats is not ready"
			done
			io_test_disk_writable=1
			log_info "created IO test disks: ${io_test_disks[*]}"
		fi
	fi
	io_test_disk_exclusive=${io_test_scsi_owned}
	io_test_record_resources selected
}

io_test_release_owned_scsi_module() {
	local owner
	[[ -e /sys/module/scsi_debug ]] || return 0
	read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
	printf 'scsi_owner=%s expected=%s\n' "${owner}" "${io_test_scsi_owner}"
	[[ -n ${io_test_scsi_owner} && ${owner} == "${io_test_scsi_owner}" ]] || return 1
	timeout --kill-after=1 2 modprobe -r scsi_debug
}

io_test_cleanup_scsi_disks() {
	if ((io_test_scsi_owned)); then
		# udev may still hold a freshly created disk after a case exits.
		if ! wait_until 10 0.1 io_test_release_owned_scsi_module >> "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources" 2>&1; then
			tail -n 20 "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources" >&2
			return 1
		fi
		io_test_scsi_owned=0
	fi
	io_test_scsi_owner=""
}

io_test_cleanup_disks() {
	local index
	for index in "${!io_test_cost_files[@]}"; do
		assert_io_test_disk_identity "${io_test_disk_name}" || return 1
		printf '%s\n' "${io_test_cost_settings[index]}" > "${io_test_cost_files[index]}" || return 1
	done
	io_test_cost_files=()
	io_test_cost_settings=()
	io_test_cleanup_scsi_disks || return 1
	if [[ -n ${io_test_storage_dir} ]]; then
		if mountpoint -q "${io_test_storage_dir}"; then
			assert_io_test_disk_identity "${io_test_disk_name}" || return 1
			[[ $(findmnt -n -o SOURCE --target "${io_test_storage_dir}") -ef ${TEST_IO_DISK} ]] || return 1
			timeout --kill-after=2s 10s umount "${io_test_storage_dir}" || {
				log_error "IO backing filesystem remains: ${io_test_storage_dir}; stop its users before reclaiming TEST_IO_DISK=${TEST_IO_DISK}"
				return 1
			}
		fi
		rmdir "${io_test_storage_dir}" || return 1
		io_test_storage_dir=""
	fi
	io_test_record_resources released
	io_test_scsi_owner=""
	io_test_disk_writable=0
	io_test_disk_exclusive=0
}

# Decode exposition into facts; each scenario owns the schema and value checks.
io_test_metrics_json() {
	jq -Rn '
		[inputs] as $lines
		| [$lines[] | select(startswith("# TYPE "))
			| capture("^# TYPE (?<name>[^ ]+) (?<kind>[^ ]+)$")] as $types
		| [$lines[] | select(startswith("#") | not) | select(test("^[ \t]*$") | not)
			| if test("^[a-zA-Z_:][a-zA-Z0-9_:]*(\\{.*\\})?[ \t]+[^ \t]+([ \t]+[^ \t]+)?[ \t]*$") then
				capture("^(?<name>[a-zA-Z_:][a-zA-Z0-9_:]*)(?:\\{(?<raw_labels>.*)\\})?[ \t]+(?<raw_value>[^ \t]+)(?:[ \t]+[^ \t]+)?[ \t]*$")
			else error("invalid metric exposition: " + .) end
			| if (.raw_value | test("^[+-]?([0-9]+(\\.[0-9]*)?|\\.[0-9]+)([eE][+-]?[0-9]+)?$|^[+-]?Inf$|^NaN$")) then .
				else error("invalid metric value: " + .raw_value) end
			| . as $sample
			| (.raw_labels // "") as $raw
			| [$raw | match("[a-zA-Z_][a-zA-Z0-9_]*=\"(?:[^\"\\\\]|\\\\.)*\"(?:,|$)"; "g") | .string] as $tokens
			| if ($tokens | join("")) != $raw then error("invalid metric labels: " + $raw) else . end
			| [$tokens[] | sub(",$"; "")
				| capture("^(?<key>[^=]+)=(?<value>.*)$") | .value |= fromjson] as $pairs
			| [$types[] | select(.name == $sample.name) | .kind] as $kinds
			| {name: .name, type: ($kinds[0] // null), type_count: ($kinds | length),
				labels: ($pairs | from_entries), label_count: ($pairs | length),
				raw_value: .raw_value, value: (try (.raw_value | tonumber) catch null)}]
	' "$1"
}

# Keep owned XFS fixtures mountable on older supported kernels.
io_test_xfs_format_options() {
	local usage feature
	usage=$(mkfs.xfs 2>&1 || true)
	for feature in inobtcount bigtime; do
		if [[ ${usage} == *"${feature}="* ]]; then
			printf '%s\n' -m "${feature}=0"
		fi
	done
	for feature in nrext64 exchange; do
		if [[ ${usage} == *"${feature}="* ]]; then
			printf '%s\n' -i "${feature}=0"
		fi
	done
	if [[ ${usage} == *"parent="* ]]; then
		printf '%s\n' -n parent=0
	fi
}

# Exercise the shipped CLI, accepting only the known module-symbol limitation.
io_test_xfs_available() {
	local binary=$1 object=$2 device=$3 evidence=$4 status=0
	mkdir -p "${evidence}"
	timeout --kill-after=2s "$((io_test_iotracing_ready_timeout + 1))s" "${binary}" --bpf-path "${object}" \
		--duration 1 --device "${device}" --output json \
		> "${evidence}/xfs-cli.json" 2> "${evidence}/xfs-cli.err" || status=$?
	((status != 0)) || return 0
	[[ ${status} == 1 ]] \
		&& grep -Eq 'xfs_file_read_iter|xfs_file_write_iter|xfs_filemap_page_mkwrite' "${evidence}/xfs-cli.err" \
		&& grep -q 'cannot assign requested address' "${evidence}/xfs-cli.err" || return 1
	awk '$3 == "xfs_file_read_iter" || $3 == "xfs_file_write_iter" || $3 == "xfs_filemap_page_mkwrite"' \
		/proc/kallsyms > "${evidence}/xfs-symbols.txt" || return 1
	awk '
		{ count[$3]++ }
		NF != 4 || $1 !~ /^[[:xdigit:]]+$/ || length($1) > 16 || $1 ~ /^0+$/ ||
			($2 != "t" && $2 != "T") || $4 != "[xfs]" { bad = 1 }
		END { exit !(count["xfs_file_read_iter"] == 1 && count["xfs_file_write_iter"] == 1 &&
			count["xfs_filemap_page_mkwrite"] == 1 && !bad) }
	' "${evidence}/xfs-symbols.txt" || return 1
	return 77
}
