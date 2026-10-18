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

# Prefer owned SCSI disks, then unused virtio disks, then read-only fallbacks.
# Existing disks are writable only with explicit opt-in and a fresh idle check;
# only disks created here may have their configuration changed or be removed.

io_test_disks=()
io_test_scsi_owned=0
io_test_scsi_owner=""
io_test_disk_writable=0

# Isolate each IO fixture and wait for its owned resources to be released before
# starting the next one. Keep their records outside runner-owned storage so
# a caller timeout cannot remove PID files before IO cleanup finishes.
io_test_run_cases() {
	local script=$1 selector=$2 name workspace io_case_pid="" status=0 io_case_signal=0 passed=0
	shift 2
	require_commands setsid unshare
	local HUATUO_BAMAI_TEST_TMPDIR
	HUATUO_BAMAI_TEST_TMPDIR=$(mktemp -d /tmp/huatuo-test.XXXXXX)
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
			sleep 0.1
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
	if ! wait_until 5 0.1 bash -c '[[ ! -d $1 ]] || rmdir "$1"' _ "${directory}"; then
		log_error "owned cgroup remains: ${directory}; inspect its processes and child groups"
		[[ ! -r ${directory}/cgroup.procs ]] || cat "${directory}/cgroup.procs" >&2
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
	local name=$1 mounts reads writes block="/sys/block/$1"
	[[ ${name} =~ ^[a-zA-Z0-9]+$ && -b /dev/${name} && -r ${block}/inflight ]] || return 1
	command -v lsblk > /dev/null || return 1
	mounts=$(lsblk -nr -o MOUNTPOINT "/dev/${name}") || return 1
	[[ -z ${mounts//[[:space:]]/} ]] || return 1
	compgen -G "${block}/holders/*" > /dev/null && return 1
	compgen -G "${block}/${name}*/holders/*" > /dev/null && return 1
	read -r reads writes < "${block}/inflight" || return 1
	[[ ${reads} == 0 && ${writes} == 0 ]]
}

io_test_scsi_disks_ready() {
	local count=$1 block model
	io_test_disks=()
	for block in /sys/block/sd*; do
		[[ -r ${block}/device/model && -b /dev/${block##*/} ]] || continue
		read -r model < "${block}/device/model"
		[[ ${model} == scsi_debug ]] && io_test_disks+=("${block##*/}")
	done
	((${#io_test_disks[@]} == count))
}

io_test_prepare_disks() {
	local count=$1 block name sectors owner load_status=0 signal_status=0
	io_test_disks=()
	io_test_scsi_owned=0
	io_test_scsi_owner=""
	io_test_disk_writable=0
	[[ ${TEST_IO_ALLOW_FREE_DISK_WRITES:-0} =~ ^[01]$ ]] \
		|| fatal "TEST_IO_ALLOW_FREE_DISK_WRITES must be 0 or 1"
	unset TEST_IOLATENCY_IO_DEVICE TEST_IOLATENCY_IO_EXCLUSIVE

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
		setsid timeout 10s modprobe --first-time scsi_debug \
			dev_size_mb=128 add_host="${count}" inq_vendor="${io_test_scsi_owner}" || load_status=$?
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
			io_test_cleanup_disks || fatal "failed scsi_debug creation left owned resources; see io-disks.resources"
			log_warn "scsi_debug creation failed (status ${load_status}); selecting an existing disk"
		else
			((io_test_scsi_owned)) || fatal "cannot verify ownership of scsi_debug test disks"
		fi

		if ((io_test_scsi_owned)); then
			wait_until 10 0.1 io_test_scsi_disks_ready "${count}" \
				|| fatal "scsi_debug did not create ${count} test disks"
			for name in "${io_test_disks[@]}"; do
				printf '1\n' > "/sys/block/${name}/queue/iostats"
			done
			io_test_disk_writable=1
			log_info "created IO test disks: ${io_test_disks[*]}"
		fi
	fi
	if ((io_test_scsi_owned == 0)); then
		# iostats must already be enabled. Kernel timestamp availability is
		# checked by IOLatency itself; preparation never changes existing disks.
		for block in /sys/block/vd* /sys/block/sd* /sys/block/nvme*; do
			name=${block##*/}
			[[ -b /dev/${name} && -r ${block}/queue/iostats && -r ${block}/size ]] || continue
			[[ $(< "${block}/queue/iostats") -ne 0 ]] || continue
			[[ ${name} != nvme* || -d ${block}/mq ]] || continue
			read -r sectors < "${block}/size"
			[[ ${sectors} =~ ^[0-9]+$ ]] || continue
			((sectors >= 262144)) || continue
			if ((${#io_test_disks[@]} == 0)); then
				io_test_disks=("${name}")
			fi
			if io_test_disk_is_free "${name}"; then
				io_test_disks=("${name}")
				if [[ ${name} == vd* ]]; then
					io_test_disk_writable=${TEST_IO_ALLOW_FREE_DISK_WRITES:-0}
				fi
				break
			fi
		done
		if ((${#io_test_disks[@]})); then
			log_info "using existing IO test disk: ${io_test_disks[0]} writable=${io_test_disk_writable}; no exclusivity assumed"
		fi
	fi
	if ((${#io_test_disks[@]})); then
		export TEST_IOLATENCY_IO_DEVICE=/dev/${io_test_disks[0]}
		export TEST_IOLATENCY_IO_EXCLUSIVE=${io_test_scsi_owned}
	else
		log_info "SKIP disk-dependent IO checks: no eligible SCSI/NVMe/virtio disk is available"
	fi
	io_test_record_resources selected
}

io_test_cleanup_disks() {
	if ((io_test_scsi_owned)); then
		# udev may still hold a freshly created disk after a case exits.
		timeout --kill-after=1s 10s bash -c \
			'while [[ -e /sys/module/scsi_debug ]]; do
				[[ -n $1 && -r /sys/module/scsi_debug/parameters/inq_vendor ]] \
					&& read -r owner < /sys/module/scsi_debug/parameters/inq_vendor \
					&& [[ ${owner} == "$1" ]] || exit 1
				modprobe -r scsi_debug && exit 0
				sleep 0.1
			done' io-test-scsi-cleanup "${io_test_scsi_owner}" || return 1
		io_test_scsi_owned=0
	fi
	io_test_record_resources released
	io_test_scsi_owner=""
	io_test_disk_writable=0
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

# Exercise the shipped CLI, accepting only the known module-symbol limitation.
io_test_xfs_available() {
	local binary=$1 object=$2 device=$3 evidence=$4 status=0
	mkdir -p "${evidence}"
	timeout --kill-after=2s 10s "${binary}" --bpf-path "${object}" \
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
