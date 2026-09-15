#!/usr/bin/env bash
#
# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

# Purpose: Validate the architecture and Docker/system-libvirt host environment.
# Caller: GitHub workflow os-distro-qemu-test.yml, as the qemu-0 step; qemu-local-test.sh locally.
# Environment:
# - LIBVIRT_URI: Libvirt connection URI; defaults to qemu:///system.
# - GITHUB_ACTIONS: GitHub-provided flag used to grant the hosted runner KVM access.
# Parameters:
# - ARCH: Optional architecture, amd64 (default) or arm64.
# Examples:
# - qemu-0-host-init.sh  # validate an amd64 host
# - qemu-0-host-init.sh arm64  # validate an arm64 host

set -euo pipefail

ARCH=${1:-amd64}
LIBVIRT_URI=${LIBVIRT_URI:-qemu:///system}
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
source "$SCRIPT_DIR/vm-test/logging.sh"
trap 'vm_phase_result qemu-0 "$?"' EXIT

case "$ARCH" in
amd64) ;;
arm64) ;;
*)
	vm_log_error "❌ Unsupported ARCH: '$ARCH', Supported ARCHs: amd64, arm64"
	exit 1
	;;
esac

required_commands=(
	docker flock jq libvirtd od python3 qemu-img realpath rsync
	sha256sum ssh ssh-keygen stat sudo virsh virt-customize virt-install zstd
)
missing_commands=()
for command_name in "${required_commands[@]}"; do
	command -v "$command_name" > /dev/null 2>&1 \
		|| missing_commands+=("$command_name")
done
if ! command -v cloud-localds > /dev/null 2>&1 \
	&& ! command -v genisoimage > /dev/null 2>&1; then
	missing_commands+=(cloud-localds-or-genisoimage)
fi
if ((${#missing_commands[@]})); then
	[[ -r /etc/os-release ]] || {
		vm_log_error "missing host tools: ${missing_commands[*]}; install them manually because /etc/os-release is unavailable"
		exit 1
	}
	# shellcheck disable=SC1091
	source /etc/os-release
	if [[ "${ID,,}" != ubuntu ]]; then
		vm_log_error "missing host tools on ${ID:-unknown}: ${missing_commands[*]}; automatic installation is only implemented for Ubuntu"
		exit 1
	fi

	declare -A command_packages=(
		["cloud-localds-or-genisoimage"]=cloud-image-utils
		[docker]=docker.io
		[flock]=util-linux
		[jq]=jq
		[libvirtd]=libvirt-daemon-system
		[od]=coreutils
		[python3]=python3
		["qemu-img"]=qemu-utils
		[realpath]=coreutils
		[rsync]=rsync
		[sha256sum]=coreutils
		[ssh]=openssh-client
		["ssh-keygen"]=openssh-client
		[stat]=coreutils
		[sudo]=sudo
		[virsh]=libvirt-clients
		["virt-customize"]=libguestfs-tools
		["virt-install"]=virtinst
		[zstd]=zstd
	)
	packages=()
	for command_name in "${missing_commands[@]}"; do
		package=${command_packages["$command_name"]:-}
		[[ -n "$package" ]] || {
			vm_log_error "no Ubuntu package mapping for missing host tool: $command_name"
			exit 1
		}
		[[ " ${packages[*]} " == *" $package "* ]] || packages+=("$package")
	done
	vm_log "installing packages for missing host tools: ${packages[*]}"
	sudo apt-get update
	sudo apt-get install -y "${packages[@]}"
fi

sudo -n true 2> /dev/null || {
	vm_log_error 'passwordless sudo is required for system libvirt VM tests'
	exit 1
}
[[ -e /dev/kvm ]] || {
	vm_log_error '/dev/kvm is unavailable; enable hardware virtualization on this runner'
	exit 1
}
if [[ ! -r /dev/kvm || ! -w /dev/kvm ]]; then
	[[ "${GITHUB_ACTIONS:-}" == true ]] || {
		vm_log_error 'the current user cannot access /dev/kvm; add it to the KVM group'
		exit 1
	}
	sudo chmod 0666 /dev/kvm
fi
host_kernel="/boot/vmlinuz-$(uname -r)"
if [[ -e "$host_kernel" && ! -r "$host_kernel" ]]; then
	sudo chmod 0644 "$host_kernel"
fi
sudo virsh --connect "$LIBVIRT_URI" uri > /dev/null
[[ "$(sudo virsh --connect "$LIBVIRT_URI" net-info default | awk '$1 == "Active:" {print $2}')" == yes ]] || {
	vm_log_error "libvirt network 'default' is not active on $LIBVIRT_URI"
	exit 1
}
sudo virsh --connect "$LIBVIRT_URI" net-dumpxml default \
	| grep -q '<range start=' || {
	vm_log_error "libvirt network 'default' has no DHCP range"
	exit 1
}
docker info > /dev/null 2>&1 || {
	vm_log_error 'the current user cannot access the Docker daemon'
	exit 1
}
vm_log "VM test host is ready: $LIBVIRT_URI"
