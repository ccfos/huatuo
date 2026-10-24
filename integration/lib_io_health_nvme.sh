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

# Own one loop-transport NVMe target through configfs. The backend is another
# private loop file so kernels without regular-file NVMe backends also qualify.
# Public ABI: Documentation/ABI/stable/configfs-nvmet and nvme-connect(1).

nvme_real_command=""
nvme_controller=""
nvme_namespace=""
nvme_backend_loop=""
nvme_backend="${WORK_DIR}/nvme.img"
nvme_fixture_id=$(< /proc/sys/kernel/random/uuid)
nvme_host_id=$(< /proc/sys/kernel/random/uuid)
nvme_nqn="nqn.2014-08.org.nvmexpress:uuid:${nvme_fixture_id}"
nvme_host_nqn="nqn.2014-08.org.nvmexpress:uuid:${nvme_host_id}"
nvme_config_root=/sys/kernel/config/nvmet
nvme_subsystem="${nvme_config_root}/subsystems/${nvme_nqn}"
nvme_host="${nvme_config_root}/hosts/${nvme_host_nqn}"
nvme_port=""
nvme_port_claimed=0
nvme_config_claimed=0

nvme_set() {
	timeout --kill-after=2s 10s bash -c 'printf "%s\n" "$2" > "$1"' _ "$1" "$2"
}

nvme_controller_owned() {
	local controller=$1
	[[ -r /sys/class/nvme/${controller}/subsysnqn && -r /sys/class/nvme/${controller}/transport ]] || return 1
	[[ $(< "/sys/class/nvme/${controller}/subsysnqn") == "${nvme_nqn}" &&
	$(< "/sys/class/nvme/${controller}/transport") == loop ]]
}

nvme_namespace_owned() {
	local name=$1 file value
	[[ -b /dev/${name} && ! -e /sys/class/block/${name}/partition ]] || return 1
	# Namespace UUID/NGUID is set before connection, independently of names
	# assigned to controllers and multipath namespace heads by the host.
	for file in "/sys/class/block/${name}/uuid" "/sys/class/block/${name}/nguid" "/sys/class/block/${name}/wwid"; do
		[[ -r ${file} ]] || continue
		read -r value < "${file}" || continue
		value=${value,,}
		value=${value//-/}
		value=${value//:/}
		[[ ${value} == "${nvme_fixture_id//-/}" || ${value} == "uuid.${nvme_fixture_id//-/}" ||
			${value} == "eui.${nvme_fixture_id//-/}" ]] && return 0
	done
	return 1
}

nvme_find_fixture() {
	local path name controllers=() namespaces=()
	for path in /sys/class/nvme/nvme*; do
		name=${path##*/}
		nvme_controller_owned "${name}" && controllers+=("${name}")
	done
	for path in /sys/class/block/nvme*n*; do
		name=${path##*/}
		nvme_namespace_owned "${name}" && namespaces+=("${name}")
	done
	((${#controllers[@]} == 1 && ${#namespaces[@]} == 1)) || return 1
	nvme_controller=${controllers[0]}
	nvme_namespace=${namespaces[0]}
}

nvme_qualify_topology() {
	local namespace_path controller_path subsystem path name
	local -a controllers=()
	namespace_path=$(readlink -f "/sys/class/block/${nvme_namespace}") \
		|| fatal "NVMe namespace device path is unreadable"
	controller_path=$(readlink -f "/sys/class/nvme/${nvme_controller}") \
		|| fatal "NVMe controller path is unreadable"
	printf 'namespace_device=%s\ncontroller_device=%s\n' "${namespace_path}" "${controller_path}" \
		> "${WORK_DIR}/nvme-topology.txt"
	if [[ ${namespace_path} == "${controller_path}/"* ]]; then return; fi
	if [[ ${namespace_path} == /sys/devices/virtual/nvme-subsystem/* ]]; then
		subsystem=${namespace_path%/*}
		for path in "${subsystem}"/nvme*; do
			name=${path##*/}
			[[ ${name} =~ ^nvme[0-9]+$ && -e ${path} ]] || continue
			printf 'head_controller=%s target=%s\n' "${name}" "$(readlink -f "${path}")" >> "${WORK_DIR}/nvme-topology.txt"
			controllers+=("${name}")
		done
		if ((${#controllers[@]} != 1)); then
			skip "H06 NVMe NOT_APPLICABLE: namespace head lacks a unique controller; see nvme-topology.txt"
		fi
		[[ ${controllers[0]} == "${nvme_controller}" &&
			$(readlink -f "${subsystem}/${controllers[0]}") == "${controller_path}" ]] \
			|| fatal "NVMe namespace head does not belong to the private controller"
		return
	fi
	skip "H06 NVMe NOT_APPLICABLE: namespace has no supported unique controller topology; see nvme-topology.txt"
}

nvme_fixture_is_owned() {
	local loops
	[[ -n ${nvme_controller} && -n ${nvme_namespace} ]] || return 1
	nvme_controller_owned "${nvme_controller}" || return 1
	nvme_namespace_owned "${nvme_namespace}" || return 1
	[[ -r ${nvme_subsystem}/namespaces/1/device_path ]] || return 1
	[[ $(< "${nvme_subsystem}/namespaces/1/device_path") == "${nvme_backend_loop}" ]] || return 1
	loops=$(timeout --kill-after=2s 3s losetup --associated "${nvme_backend}" --list --noheadings --output NAME) || return 1
	grep -Fxq "${nvme_backend_loop}" <<< "${loops}"
}

nvme_fixture_prepare() {
	local port
	require_commands nvme modprobe modinfo readlink
	nvme_real_command=$(command -v nvme)
	modinfo nvme-loop > "${WORK_DIR}/nvme-loop-module.txt" 2>&1 \
		|| skip "H06 NVMe NOT_APPLICABLE: nvme-loop module is unavailable"
	findmnt -n -t configfs --target /sys/kernel/config > /dev/null \
		|| skip "H06 NVMe BLOCKED: configfs is not mounted at /sys/kernel/config"
	timeout --kill-after=2s 10s modprobe nvme-loop || skip "H06 NVMe BLOCKED: cannot load nvme-loop"
	wait_until 5 0.1 test -d "${nvme_config_root}/subsystems" || fatal "NVMe target configfs did not appear"
	[[ ! -e ${nvme_subsystem} && ! -e ${nvme_host} ]] || fatal "generated NVMe fixture identity already exists"
	printf 'nvme_nqn=%s\nnvme_host=%s\nnvme_backend=%s\nnvme_namespace_id=%s\n' \
		"${nvme_nqn}" "${nvme_host_nqn}" "${nvme_backend}" "${nvme_fixture_id}" >> "${WORK_DIR}/resources.txt"
	truncate -s 64M "${nvme_backend}"
	nvme_backend_loop=$(timeout --kill-after=2s 10s losetup --find --show "${nvme_backend}") \
		|| fatal "cannot create private NVMe backing loop"
	nvme_config_claimed=1
	mkdir "${nvme_subsystem}" "${nvme_host}"
	nvme_set "${nvme_subsystem}/attr_allow_any_host" 0
	ln -s "${nvme_host}" "${nvme_subsystem}/allowed_hosts/${nvme_host_nqn}"
	mkdir "${nvme_subsystem}/namespaces/1"
	nvme_set "${nvme_subsystem}/namespaces/1/device_path" "${nvme_backend_loop}"
	nvme_set "${nvme_subsystem}/namespaces/1/device_nguid" "${nvme_fixture_id}"
	if [[ -e ${nvme_subsystem}/namespaces/1/device_uuid ]]; then
		nvme_set "${nvme_subsystem}/namespaces/1/device_uuid" "${nvme_fixture_id}"
	fi
	nvme_set "${nvme_subsystem}/namespaces/1/enable" 1
	for ((port = 33000; port < 33100; port++)); do
		[[ ! -e ${nvme_config_root}/ports/${port} ]] || continue
		nvme_port="${nvme_config_root}/ports/${port}"
		printf 'nvme_port=%s\n' "${nvme_port}" >> "${WORK_DIR}/resources.txt"
		mkdir "${nvme_port}"
		nvme_port_claimed=1
		break
	done
	[[ -n ${nvme_port} ]] || fatal "no unused NVMe loop port is available"
	nvme_set "${nvme_port}/addr_trtype" loop
	ln -s "${nvme_subsystem}" "${nvme_port}/subsystems/${nvme_nqn}"
	wait_until 5 0.1 test -c /dev/nvme-fabrics || fatal "NVMe fabrics control device did not appear"
	timeout --kill-after=2s 15s "${nvme_real_command}" connect --transport=loop \
		--nqn="${nvme_nqn}" --hostnqn="${nvme_host_nqn}" >> "${WORK_DIR}/commands.log" 2>&1 \
		|| fatal "cannot connect the private NVMe loop target"
	wait_until 10 0.1 nvme_find_fixture \
		|| skip "H06 NVMe BLOCKED: private controller/namespace cannot be uniquely identified by NQN and UUID/NGUID"
	nvme_fixture_is_owned || fatal "NVMe fixture ownership cannot be confirmed"
	nvme_qualify_topology
	printf 'nvme_controller=%s\nnvme_namespace=%s\nnvme_backing_loop=%s\n' \
		"${nvme_controller}" "${nvme_namespace}" "${nvme_backend_loop}" >> "${WORK_DIR}/resources.txt"
	if ! timeout --kill-after=2s 10s "${nvme_real_command}" smart-log "/dev/${nvme_controller}" -o json \
		> "${WORK_DIR}/nvme-smart-reference.json" 2> "${WORK_DIR}/nvme-smart-reference.err"; then
		skip "H06 NVMe BLOCKED: private target does not provide readable SMART evidence"
	fi
	jq -e 'has("critical_warning") and has("media_errors")' "${WORK_DIR}/nvme-smart-reference.json" > /dev/null \
		|| skip "H06 NVMe BLOCKED: target SMART output lacks the documented evidence fields"
}

nvme_fixture_cleanup() {
	local failed=0 path controller loops loop
	[[ -n ${nvme_real_command} ]] || return 0
	if [[ -n ${nvme_namespace} ]]; then
		if compgen -G "/sys/class/block/${nvme_namespace}/holders/*" > /dev/null \
			|| compgen -G "/sys/class/block/${nvme_namespace}/${nvme_namespace}*/holders/*" > /dev/null; then
			log_error "NVMe cleanup: owned namespace ${nvme_namespace} still has block holders; retaining target ${nvme_nqn}"
			return 1
		fi
	fi
	if ((nvme_config_claimed)); then
		for path in /sys/class/nvme/nvme*; do
			controller=${path##*/}
			nvme_controller_owned "${controller}" || continue
			if ! timeout --kill-after=2s 10s "${nvme_real_command}" disconnect --device="${controller}" \
				|| ! wait_until 5 0.1 test ! -e "/sys/class/nvme/${controller}"; then
				log_error "NVMe cleanup: controller ${controller}, NQN ${nvme_nqn} remains connected"
				failed=1
			fi
		done
		if [[ -n ${nvme_port} && -L ${nvme_port}/subsystems/${nvme_nqn} ]]; then
			if [[ $(readlink -f "${nvme_port}/subsystems/${nvme_nqn}") == "${nvme_subsystem}" ]]; then
				rm "${nvme_port}/subsystems/${nvme_nqn}" || failed=1
			else
				failed=1
			fi
		fi
		if [[ -n ${nvme_port} && -d ${nvme_port} ]]; then
			if ((nvme_port_claimed)); then
				rmdir "${nvme_port}" || failed=1
			else
				log_error "NVMe cleanup: creation of ${nvme_port} was interrupted; verify its ownership before removal"
				failed=1
			fi
		fi
		if [[ -d ${nvme_subsystem}/namespaces/1 ]]; then
			if [[ -z $(< "${nvme_subsystem}/namespaces/1/device_path") ||
			$(< "${nvme_subsystem}/namespaces/1/device_path") == "${nvme_backend_loop}" ]]; then
				nvme_set "${nvme_subsystem}/namespaces/1/enable" 0 || failed=1
				rmdir "${nvme_subsystem}/namespaces/1" || failed=1
			else
				log_error "NVMe cleanup: namespace backend identity changed; retaining ${nvme_subsystem}"
				failed=1
			fi
		fi
		if [[ -L ${nvme_subsystem}/allowed_hosts/${nvme_host_nqn} ]]; then
			if [[ $(readlink -f "${nvme_subsystem}/allowed_hosts/${nvme_host_nqn}") == "${nvme_host}" ]]; then
				rm "${nvme_subsystem}/allowed_hosts/${nvme_host_nqn}" || failed=1
			else
				failed=1
			fi
		fi
		[[ ! -d ${nvme_subsystem} ]] || rmdir "${nvme_subsystem}" || failed=1
		[[ ! -d ${nvme_host} ]] || rmdir "${nvme_host}" || failed=1
	fi
	[[ -e ${nvme_backend} ]] || return "${failed}"
	if loops=$(timeout --kill-after=2s 3s losetup --associated "${nvme_backend}" --list --noheadings --output NAME); then
		while IFS= read -r loop; do
			[[ -n ${loop} ]] || continue
			if ((failed)) || compgen -G "/sys/class/block/${loop##*/}/holders/*" > /dev/null \
				|| ! timeout --kill-after=2s 5s losetup --detach "${loop}" \
				|| ! wait_until 5 0.1 test ! -e "/sys/class/block/${loop##*/}/loop/backing_file"; then
				log_error "NVMe cleanup: retaining ${loop}, backing=${nvme_backend}; remove owned target users before detaching"
				failed=1
			fi
		done <<< "${loops}"
	else
		failed=1
	fi
	((failed == 0))
}
