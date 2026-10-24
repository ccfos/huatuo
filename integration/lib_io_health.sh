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

# Share owned MD array identities and loop cleanup across IO health cases.

io_health_md_init() {
	require_commands udevadm
	io_health_md_work_dir=$1
	md_device=""
	md_sysfs=""
	md_uuid=$(< /proc/sys/kernel/random/uuid)
	md_uuid=${md_uuid//-/}
	md_label="${2}-${md_uuid:0:12}"
	md_uuid="${md_uuid:0:8}:${md_uuid:8:8}:${md_uuid:16:8}:${md_uuid:24:8}"
	REQUESTED_MD_UUID=${md_uuid}
	SWAPPED_MD_UUID=""
	local word
	local -a words=()
	IFS=: read -r -a words <<< "${md_uuid}"
	for word in "${words[@]}"; do
		SWAPPED_MD_UUID+="${SWAPPED_MD_UUID:+:}${word:6:2}${word:4:2}${word:2:2}${word:0:2}"
	done
	md_uuid_bound=0
	io_health_loop_backings=()
	io_health_loop_devices=()
	mkdir -p "${io_health_md_work_dir}"
	printf 'requested_uuid=%s\nalternate_uuid=%s\nRecovery: verify the UUID and member ownership, stop the owned array, clear only its member metadata, then detach loops associated with the recorded backing files.\n' \
		"${REQUESTED_MD_UUID}" "${SWAPPED_MD_UUID}" > "${io_health_md_work_dir}/resources.txt"
}

io_health_md_command() {
	timeout --kill-after=2s 15s "$@" >> "${io_health_md_work_dir}/commands.log" 2>&1
}

# Device events must finish before a name or MD member is reused. The udev and
# mdadm state directories must refer to the same host state as those consumers.
io_health_md_wait_udev() {
	io_health_md_command udevadm settle --timeout=10
}

io_health_md_uuid_is_expected() {
	local actual=${1,,}
	if ((md_uuid_bound)); then
		[[ ${actual} == "${md_uuid}" ]]
	else
		# mdadm versions differ in the byte order used to export supplied UUIDs.
		[[ ${actual} == "${REQUESTED_MD_UUID}" || ${actual} == "${SWAPPED_MD_UUID}" ]]
	fi
}

io_health_md_owned_loops() {
	local image
	for image in "${io_health_loop_backings[@]}"; do
		[[ -e ${image} ]] || continue
		timeout --kill-after=2s 3s losetup --associated "${image}" --list --noheadings --output NAME || return 1
	done
}

io_health_md_create_loop() {
	local image=$1 loop
	# Record the backing before losetup: interruption can precede its output.
	io_health_loop_backings+=("${image}")
	printf 'backing=%s\n' "${image}" >> "${io_health_md_work_dir}/resources.txt"
	truncate -s 64M "${image}"
	loop=$(timeout --kill-after=2s 15s losetup --find --show "${image}") \
		|| fatal "cannot allocate owned loop backing ${image}"
	[[ ${loop} =~ ^/dev/loop[0-9]+$ && -b ${loop} ]] || fatal "invalid owned loop device: ${loop}"
	io_health_loop_devices+=("${loop}")
	printf 'loop=%s backing=%s\n' "${loop}" "${image}" >> "${io_health_md_work_dir}/resources.txt"
}

# Evidence cases supply one separately owned SCSI/NVMe member. MD-only cases
# accept no external members; callers override only this ownership predicate.
io_health_md_target_is_owned() {
	return 1
}

io_health_md_array_is_owned() {
	local details actual entry disk loops has_owned_member=0
	loops=$(io_health_md_owned_loops) || return 1
	details=$(timeout --kill-after=2s 3s mdadm --detail --export "${md_device}") || return 1
	actual=$(awk -F= '$1 == "MD_UUID" {print $2}' <<< "${details}")
	io_health_md_uuid_is_expected "${actual}" || return 1
	for entry in "${md_sysfs}"/md/dev-*; do
		[[ -d ${entry} ]] || continue
		disk=${entry##*/dev-}
		if grep -Fxq "/dev/${disk}" <<< "${loops}"; then
			has_owned_member=1
		else
			io_health_md_target_is_owned "${disk}" || return 1
			has_owned_member=1
		fi
	done
	# Reassembly may expose only a subset, but every present member must be ours.
	((has_owned_member)) || return 1
	if ((md_uuid_bound == 0)); then
		md_uuid=${actual,,}
		md_uuid_bound=1
		printf 'export_uuid=%s\n' "${md_uuid}" >> "${io_health_md_work_dir}/resources.txt"
	fi
}

assert_io_health_md_array_healthy() {
	local entry state degraded read_only members=0
	io_health_md_array_is_owned || return 1
	read -r degraded 2> /dev/null < "${md_sysfs}/md/degraded" || return 1
	read -r read_only 2> /dev/null < "${md_sysfs}/ro" || return 1
	printf 'array=%s degraded=%s read_only=%s\n' "${md_device}" "${degraded}" "${read_only}" \
		> "${io_health_md_work_dir}/array-health.txt"
	[[ ${degraded} == 0 && ${read_only} == 0 ]] || return 1
	for entry in "${md_sysfs}"/md/dev-*; do
		[[ -d ${entry} ]] || continue
		read -r state 2> /dev/null < "${entry}/state" || return 1
		printf 'member=%s state=%s\n' "${entry##*/dev-}" "${state}" >> "${io_health_md_work_dir}/array-health.txt"
		grep -Eq '(^|,)in_sync(,|$)' <<< "${state}" || return 1
		members=$((members + 1))
	done
	((members == 2))
}

io_health_md_create() {
	local minor
	io_health_md_wait_udev || fatal "udev did not settle before MD creation; inspect ${io_health_md_work_dir}/commands.log"
	if ! grep -q '\[raid1\]' /proc/mdstat; then
		require_commands modprobe
		timeout --kill-after=2s 15s modprobe raid1 || skip "RAID1 kernel personality is unavailable"
	fi
	for ((minor = 127; minor >= 1; minor--)); do
		[[ ! -e /sys/block/md${minor} ]] || continue
		[[ ! -e /dev/md${minor} || -b /dev/md${minor} ]] || continue
		md_device="/dev/md${minor}"
		md_sysfs="/sys/block/md${minor}"
		break
	done
	[[ -n ${md_device} ]] || fatal "no unused MD device name is available"
	printf 'array=%s\nname=%s\n' "${md_device}" "${md_label}" >> "${io_health_md_work_dir}/resources.txt"
	io_health_md_command mdadm --create "${md_device}" --metadata=1.2 --level=1 --raid-devices=2 \
		--name="${md_label}" --uuid="${REQUESTED_MD_UUID}" --assume-clean --run --force "$@" < /dev/null \
		|| fatal "cannot create owned RAID1; inspect ${io_health_md_work_dir}/commands.log"
	wait_until 10 0.2 io_health_md_array_is_owned || fatal "created array UUID or member ownership cannot be confirmed"
	io_health_md_wait_udev || fatal "udev did not settle after MD creation; inspect ${io_health_md_work_dir}/commands.log"
	wait_until 20 0.2 assert_io_health_md_array_healthy \
		|| fatal "created RAID1 is not healthy and writable; inspect ${io_health_md_work_dir}/array-health.txt"
}

io_health_md_clear_metadata() {
	local disk=$1 details
	if compgen -G "/sys/class/block/${disk##*/}/holders/*" > /dev/null; then
		log_error "MD cleanup: ${disk} still has block holders; retaining its metadata"
		return 1
	fi
	if details=$(timeout --kill-after=2s 3s mdadm --examine --export "${disk}" 2> /dev/null); then
		if ! io_health_md_uuid_is_expected "$(awk -F= '$1 == "MD_UUID" {print $2}' <<< "${details}")"; then
			log_error "MD cleanup: ${disk} UUID differs from ${md_uuid}; refusing to clear metadata"
			return 1
		fi
		io_health_md_command mdadm --zero-superblock --force "${disk}" || return 1
	fi
}

io_health_md_cleanup() {
	local failed=0 loops loop disk details actual
	local md_device md_sysfs
	# udev may reassemble the same UUID under a different MD name. Names alone
	# never authorize teardown; verify the UUID and every member before stopping.
	for md_sysfs in /sys/block/md*; do
		[[ -d ${md_sysfs}/md ]] || continue
		md_device="/dev/${md_sysfs##*/}"
		details=$(timeout --kill-after=2s 3s mdadm --detail --export "${md_device}") || continue
		actual=$(awk -F= '$1 == "MD_UUID" {print $2}' <<< "${details}")
		io_health_md_uuid_is_expected "${actual}" || continue
		if ! io_health_md_array_is_owned; then
			log_error "MD cleanup: refusing array ${md_device}; member ownership differs from UUID ${md_uuid}"
			failed=1
		elif ! io_health_md_command mdadm --stop --force "${md_device}" \
			|| ! wait_until 5 0.2 test ! -d "${md_sysfs}"; then
			log_error "MD cleanup: stop owned array ${md_device} UUID ${md_uuid} before reclaiming its members"
			failed=1
		fi
	done
	io_health_md_wait_udev || failed=1
	loops=$(io_health_md_owned_loops) || {
		log_error "MD cleanup: cannot resolve owned backing files in ${io_health_md_work_dir}"
		return 1
	}
	((failed == 0)) || return 1
	# Clear every member before detaching any loop so surviving metadata cannot
	# reassemble an array around a member whose backing has already disappeared.
	for disk in "$@"; do
		[[ -b ${disk} ]] || continue
		if ! io_health_md_target_is_owned "${disk##*/}" || ! io_health_md_clear_metadata "${disk}"; then
			log_error "MD cleanup: retaining external member ${disk}; verify its identity and holders"
			failed=1
		fi
	done
	while IFS= read -r loop; do
		[[ -z ${loop} ]] || io_health_md_clear_metadata "${loop}" || failed=1
	done <<< "${loops}"
	((failed == 0)) || return 1
	io_health_md_wait_udev || return 1
	while IFS= read -r loop; do
		[[ -n ${loop} ]] || continue
		if ! io_health_md_command losetup --detach "${loop}" \
			|| ! wait_until 5 0.2 test ! -e "/sys/block/${loop##*/}/loop/backing_file"; then
			log_error "MD cleanup: retaining ${loop}; inspect its holders, UUID and recorded backing file"
			failed=1
		fi
	done <<< "${loops}"
	io_health_md_wait_udev || failed=1
	((failed == 0))
}

assert_io_health_md_sync_action_increased() {
	local count
	[[ -s ${io_health_observer_event} ]] || return 1
	count=$(jq -s --arg array "${md_device##*/}" '
		[.[] | select(.tracer_name == "io_health" and .tracer_type == "event"
			and .tracer_data.type == "md_sync_action" and .tracer_data.array == $array
			and .tracer_data.new_state == "check")] | length
	' "${io_health_observer_event}") || return 1
	((count > io_health_observer_before))
}

io_health_md_observer_transition() {
	if assert_io_health_md_sync_action_increased; then return 0; fi
	io_health_observer_polls=$((io_health_observer_polls + 1))
	# Poll saved events promptly, while holding each input state for at least a
	# second so a slower observer still has time to see the transition.
	((io_health_observer_polls % 5 == 1)) || return 1
	if [[ $(< "${md_sysfs}/md/sync_action") == check ]]; then
		printf 'idle\n' > "${md_sysfs}/md/sync_action"
	else
		printf 'check\n' > "${md_sysfs}/md/sync_action"
	fi
	return 1
}

assert_io_health_md_observer_ready() {
	local io_health_observer_event=$1 io_health_observer_before=0 result=0
	local io_health_observer_polls=0
	io_health_md_array_is_owned || fatal "array ownership changed before observer readiness check"
	if [[ -s ${io_health_observer_event} ]]; then
		io_health_observer_before=$(jq -s --arg array "${md_device##*/}" '
			[.[] | select(.tracer_name == "io_health" and .tracer_type == "event"
				and .tracer_data.type == "md_sync_action" and .tracer_data.array == $array
				and .tracer_data.new_state == "check")] | length
		' "${io_health_observer_event}") || return 1
	fi
	# A controlled non-fault transition proves this array's observer is active
	# without starting member evidence or warming its cooldown.
	printf '1\n' > "${md_sysfs}/md/sync_speed_min"
	printf '1\n' > "${md_sysfs}/md/sync_speed_max"
	wait_until 20 0.2 io_health_md_observer_transition || result=1
	printf 'idle\n' > "${md_sysfs}/md/sync_action" || result=1
	printf 'system\n' > "${md_sysfs}/md/sync_speed_min" || result=1
	printf 'system\n' > "${md_sysfs}/md/sync_speed_max" || result=1
	if ((result)); then
		{
			printf 'array=%s uuid=%s baseline_check_events=%s\n' "${md_device}" "${md_uuid}" "${io_health_observer_before}"
			for attribute in "${md_sysfs}"/md/{array_state,degraded,sync_action,dev-*/state}; do
				[[ -r ${attribute} ]] || continue
				printf '%s=' "${attribute}"
				cat "${attribute}"
			done
			[[ ! -f ${io_health_observer_event} ]] || cat "${io_health_observer_event}"
		} > "${io_health_md_work_dir}/observer-readiness.txt" 2>&1
		log_error "MD observer did not report a new controlled transition; inspect ${io_health_md_work_dir}/observer-readiness.txt and huatuo.log"
		return 1
	fi
}

# A fresh pipe and the selected hooks must belong to the same load attempt.
io_health_probe_and_pipe_ready() {
	local log=$1
	shift
	[[ -r ${log} ]] || return 1
	awk -v required="$*" '
		function reset_session(key) {
			active = 0
			pipe = 0
			for (key in attached) delete attached[key]
			for (key in evidence) delete evidence[key]
		}
		BEGIN { count = split(required, targets, " ") }
		/msg="loaded BPF"/ && /bpf="?io_health[.]o"?( |$)/ { reset_session(); active = 1 }
		/msg="created BPF event pipe"/ && /map_name="?health_events"?( |$)/ {
			active = 1
			pipe = 1
			pipe_line = $0
		}
		/msg="io_health: kernel event session failed; will retry"/ { reset_session() }
		active && /msg="attached BPF (raw )?tracepoint"/ {
			if (!match($0, /attach_target=("[^"]+"|[^ ]+)/)) next
			target = substr($0, RSTART, RLENGTH)
			sub(/^attach_target="?/, "", target)
			sub(/"$/, "", target)
			for (slot = 1; slot <= count; slot++) {
				if (target ~ "^(" targets[slot] ")$") {
					attached[slot] = 1
					evidence[slot] = $0
				}
			}
		}
		END {
			if (!pipe || !count) exit 1
			for (slot = 1; slot <= count; slot++) if (!attached[slot]) exit 1
			print pipe_line
			for (slot = 1; slot <= count; slot++) print evidence[slot]
		}
	' "${log}"
}

assert_io_health_bpf_ready() {
	if ! wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" 0.2 \
		io_health_probe_and_pipe_ready "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "$@" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/io-health-bpf-readiness.log" 2>&1; then
		log_error "IO Health event pipe or required probes did not become ready: $*; inspect io-health-bpf-readiness.log and huatuo.log"
		return 1
	fi
	log_info "IO Health event pipe and required probes ready: $*"
}
