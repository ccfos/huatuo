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

# Purpose: Prepare the guest test environment and run Huatuo e2e tests in /mnt/host.
# Caller: qemu-2-run-in-vm.sh streams this helper to the guest over SSH.
# Environment:
# - HOME: Used to add the user's Go binary directory to PATH.
# - PATH: Extended with system and user Go binary directories.
# Parameters:
# - None.
# Examples:
# - run-in-vm.sh  # prepare the guest and run make e2e

set -euo pipefail

vm_log() {
	printf '[%s] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

vm_log_error() {
	vm_log "$*" >&2
}

print_sys_info() {
	uname -a
	[[ ! -f /etc/os-release ]] || cat /etc/os-release
	lscpu || true
	free -h
	df -h
	ip addr show || true
	docker version || true
	docker image ls --digests || true
	crictl version || true
	crictl images --digests || true
	kubectl get nodes -o wide || true
	kubectl get pods -A || true
}

configure_proxy() {
	local proxy_port=11008
	if ! timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/$1' _ "$proxy_port" 2> /dev/null; then
		return
	fi
	export http_proxy="http://127.0.0.1:${proxy_port}"
	export https_proxy="$http_proxy"
	export HTTP_PROXY="$http_proxy"
	export HTTPS_PROXY="$http_proxy"
	export all_proxy="socks5://127.0.0.1:${proxy_port}"
	export ALL_PROXY="$all_proxy"
	export no_proxy='127.0.0.1,localhost,10.96.0.0/12,10.244.0.0/16,.svc,.cluster.local'
	export NO_PROXY="$no_proxy"
}

install_test_dependencies() {
	local os_id os_like package_manager package
	local -a deb_packages=(curl gdb)
	local -a rpm_packages=(curl gdb)
	local -a missing_packages=()

	# Add temporary dependencies here until the VM image includes them.
	source /etc/os-release
	os_id=${ID,,}
	os_like=${ID_LIKE:-}
	os_like=${os_like,,}
	case " ${os_id} ${os_like} " in
	*ubuntu* | *debian*)
		for package in "${deb_packages[@]}"; do
			dpkg-query -W -f='${Status}' "$package" 2> /dev/null \
				| grep -q 'ok installed' || missing_packages+=("$package")
		done
		((${#missing_packages[@]} == 0)) && return
		export DEBIAN_FRONTEND=noninteractive
		apt-get -o DPkg::Lock::Timeout=300 update
		apt-get -o DPkg::Lock::Timeout=300 install -y --no-install-recommends \
			"${missing_packages[@]}"
		;;
	*fedora* | *rhel* | *centos* | *rocky* | *openeuler* | *anolis* | *opencloudos*)
		for package in "${rpm_packages[@]}"; do
			rpm -q "$package" > /dev/null 2>&1 || missing_packages+=("$package")
		done
		((${#missing_packages[@]} == 0)) && return
		if command -v dnf > /dev/null 2>&1; then
			package_manager=dnf
		elif command -v yum > /dev/null 2>&1; then
			package_manager=yum
		else
			vm_log_error "no RPM package manager found for $ID"
			return 1
		fi
		"$package_manager" install -y "${missing_packages[@]}"
		;;
	*)
		vm_log_error "unsupported guest package family: ID=$ID ID_LIKE=${ID_LIKE:-}"
		return 1
		;;
	esac
}

install_temporary_go_tools() {
	local tool command_name package attempt installed version
	local -a tools=(
		'oapi-codegen|github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen'
	)
	for tool in "${tools[@]}"; do
		command_name=${tool%%|*}
		package=${tool#*|}
		if command -v "$command_name" > /dev/null 2>&1; then
			version=$("$command_name" -version | awk 'NF {value=$0} END {print value}')
			[[ "$version" != v2.7.2 ]] || continue
		fi
		installed=false
		for attempt in 1 2 3; do
			if (cd /mnt/host && timeout 300 env GOTOOLCHAIN=local \
				go build -mod=vendor -o "/usr/local/bin/$command_name" "$package"); then
				installed=true
				break
			fi
			sleep $((attempt * 2))
		done
		[[ "$installed" == true ]] || {
			vm_log_error "failed to install temporary Go tool: $package"
			return 1
		}
		version=$("$command_name" -version | awk 'NF {value=$0} END {print value}')
		[[ "$version" == v2.7.2 ]] || {
			vm_log_error "unexpected $command_name version after installation: $version"
			return 1
		}
	done
}

configure_proxy
print_sys_info
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
install_test_dependencies
install_temporary_go_tools
export JAVA_PROFILER_DOCKER_IMAGE=eclipse-temurin:17-jdk
export JAVA_PROFILER_CONTAINERD_IMAGE=docker.io/library/eclipse-temurin:17-jdk
export NATIVE_PROFILER_DOCKER_IMAGE=busybox:1.36.1
export NATIVE_PROFILER_CONTAINERD_IMAGE=docker.io/library/busybox:1.36.1

cd /mnt/host
pwd
ls -alh .
git config --global --add safe.directory /mnt/host

# This is the single entry point for all guest test targets.
vm_log '⬅️⬅️⬅️ Running tests...'

make install-tools
make test

vm_log '✅✅✅ Tests passed.'
