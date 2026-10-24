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
# - Optional --prepare-only or --test-only; omitted runs both phases.
# Examples:
# - run-in-vm.sh  # prepare the guest and run make test

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
	local guest_hostname guest_ip proxy_port=11008
	if ! timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/$1' _ "$proxy_port" 2> /dev/null; then
		return
	fi
	guest_hostname=$(hostname)
	guest_ip=$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i <= NF; i++) if ($i == "src") {print $(i + 1); exit}}')
	export http_proxy="http://127.0.0.1:${proxy_port}"
	export https_proxy="$http_proxy"
	export HTTP_PROXY="$http_proxy"
	export HTTPS_PROXY="$http_proxy"
	export all_proxy="socks5://127.0.0.1:${proxy_port}"
	export ALL_PROXY="$all_proxy"
	export no_proxy="127.0.0.1,localhost,${guest_ip},${guest_hostname},10.96.0.0/12,10.244.0.0/16,.svc,.cluster.local"
	export NO_PROXY="$no_proxy"
}

install_test_dependencies() {
	local os_id os_like package_manager package
	local -a deb_packages=(curl gdb fio mdadm xfsprogs build-essential libelf-dev kmod cloud-guest-utils "linux-headers-$(uname -r)")
	local -a rpm_packages=(
		curl gdb fio mdadm xfsprogs gcc make elfutils-libelf-devel kmod cloud-utils-growpart kernel-headers
		"kernel-devel-$(uname -r)"
	)
	local -a missing_packages=()
	local -a package_options=()

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
		# Rocky's EPEL repository is unrelated to kernel module builds and its
		# mirror metadata is less reliable than the base repositories.
		if [[ "$os_id" == rocky ]]; then
			package_options+=(--disablerepo='epel*')
		fi
		# openEuler publishes kernel-devel/kernel-headers in its binary OS
		# repositories. Its update and everything repositories carry very
		# large metadata (tens of MB) served by slow mirrors, so skip them
		# and only fall back to the full set if a build dependency is missing
		# from the restricted repositories.
		if [[ "$os_id" == openeuler ]]; then
			package_options+=(
				--disablerepo=EPOL --disablerepo=debuginfo --disablerepo=source
				--disablerepo=update-source --disablerepo=update
				--disablerepo=everything
			)
		fi
		if ! "$package_manager" "${package_options[@]}" install -y "${missing_packages[@]}"; then
			((${#package_options[@]})) || return 1
			vm_log "install with restricted repositories failed; retrying with all repositories"
			"$package_manager" install -y "${missing_packages[@]}"
		fi
		;;
	*)
		vm_log_error "unsupported guest package family: ID=$ID ID_LIKE=${ID_LIKE:-}"
		return 1
		;;
	esac
}

grow_root_filesystem() {
	local device filesystem parent block_type partition output status=0
	device=$(findmnt -n -o SOURCE /)
	filesystem=$(findmnt -n -o FSTYPE /)
	case "$filesystem" in xfs | ext4) ;; *) return 0 ;; esac
	device=$(readlink -f "$device" 2> /dev/null || printf '%s' "$device")
	read -r parent block_type < <(lsblk -ndo PKNAME,TYPE "$device")
	[[ "$block_type" == part ]] || return 0
	partition=${device##*/}
	partition=${partition#"$parent"}
	partition=${partition#p}
	[[ -n "$parent" && "$partition" =~ ^[0-9]+$ ]] || {
		vm_log_error "cannot identify root partition: $device"
		return 1
	}
	# Enlarging the virtual disk does not grow every image's root filesystem.
	output=$(LC_ALL=C growpart "/dev/$parent" "$partition" 2>&1) || status=$?
	printf '%s\n' "$output"
	if [[ "$status" != 0 && !( "$status" == 1 && "$output" == NOCHANGE:* ) ]]; then
		return "$status"
	fi
	case "$filesystem" in
	xfs) xfs_growfs / || return ;;
	ext4) resize2fs "$device" || return ;;
	esac
	df -h /
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

prefetch_storage_images() {
	source integration/lib_storage.sh
	local image
	mkdir -p _output/storage-image-prefetch
	for image in "${STORAGE_ES7_IMAGE}" "${STORAGE_OPENSEARCH_IMAGE}"; do
		vm_log "preloading storage image: ${image}"
		timeout 5m docker pull "${image}" > "_output/storage-image-prefetch/${image##*/}.log" 2>&1 &
		storage_prefetch_pids+=("$!")
	done
}

stop_storage_image_prefetch() {
	local status=$? pid
	trap - EXIT
	for pid in "${storage_prefetch_pids[@]}"; do
		# Only signal jobs still owned by this shell; completed PIDs may be reused.
		if jobs -pr | grep -qx "${pid}"; then kill "${pid}" 2> /dev/null || true; fi
		wait "${pid}" 2> /dev/null || vm_log "storage image prefetch did not finish successfully; see _output/storage-image-prefetch"
	done
	exit "${status}"
}

run_guest_tests() {
	configure_proxy
	export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
	export GOCACHE=/root/.cache/go-build
	if [[ ${1:-} != --test-only ]]; then
		print_sys_info
		install_test_dependencies
		grow_root_filesystem
	fi
	# CI restores the Go cache after growing the filesystem, before any Go builds.
	if [[ ${1:-} == --prepare-only ]]; then exit 0; fi
	cd /mnt/host
	if [[ ! -f _output/portable-generated.commit ]]; then
		install_temporary_go_tools
	fi
	export JAVA_PROFILER_DOCKER_IMAGE=eclipse-temurin:17-jdk
	export JAVA_PROFILER_CONTAINERD_IMAGE=docker.io/library/eclipse-temurin:17-jdk
	export NATIVE_PROFILER_DOCKER_IMAGE=busybox:1.36.1
	export NATIVE_PROFILER_CONTAINERD_IMAGE=docker.io/library/busybox:1.36.1

	pwd
	ls -alh .
	git config --global --add safe.directory /mnt/host

	# This is the single entry point for all guest test targets.
	vm_log '⬅️⬅️⬅️ Running tests...'

	# Hide image downloads behind compilation and unit tests. Compatibility cases
	# still perform their normal pull checks, including when prefetch fails.
	storage_prefetch_pids=()
	trap stop_storage_image_prefetch EXIT
	prefetch_storage_images
	make install-tools
	if [[ -f _output/portable-generated.commit ]]; then
		make test SKIP_PORTABLE_GENERATION=1
	else
		make test
	fi

	vm_log '✅✅✅ Tests passed.'
}

# Also support sourcing the entry function in local regression fixtures.
if [[ ${BASH_SOURCE[0]:-$0} == "$0" ]]; then run_guest_tests "$@"; fi
