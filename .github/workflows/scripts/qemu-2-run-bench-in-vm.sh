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

# Runs the performance-overhead benchmark inside a QEMU VM (issue #336). The
# repo is bind-mounted at /mnt/host, so artifacts written to
# /mnt/host/bench/results/ are visible on the CI runner for upload.
#
# Args: <arch> <os-distro>  (passed by .github/workflows/benchmark.yml).
# Env:
#   BENCH_FAIL_ON_REGRESSION  1 to fail CI on a threshold breach.

set -xeuo pipefail

ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}
GOLANG_VERSION="1.24.0"

# Mirrors qemu-2-run-in-vm.sh: install a pinned Go toolchain.
function install_golang() {
	local GOLANG_URL="https://mirrors.aliyun.com/golang/go${GOLANG_VERSION}.linux-${ARCH}.tar.gz"
	local GOLANG_TAR="go${GOLANG_VERSION}.linux-${ARCH}.tar.gz"
	local need_install=1
	if command -v go > /dev/null 2>&1; then
		local goversion
		goversion=$(go version | awk '{print $3}' | sed 's/^go//')
		[[ "$goversion" == "$GOLANG_VERSION" ]] && need_install=0
	fi
	if [[ $need_install -eq 1 ]]; then
		wget -q -O "$GOLANG_TAR" "$GOLANG_URL"
		rm -rf /usr/local/go
		tar -C /usr/local -xzf "$GOLANG_TAR"
		rm -f "$GOLANG_TAR"
	fi
	export PATH="/usr/local/go/bin:${PATH}"
	export PATH="$(/usr/local/go/bin/go env GOPATH)/bin:${PATH}"
	go env -w GOPROXY=https://goproxy.cn,direct
}

# Build dependencies for `make all` + the benchmark workloads.
function prepare_bench_env() {
	case $OS_DISTRO in
	ubuntu*)
		sudo apt-get update
		sudo flock --wait 300 /var/lib/dpkg/lock-frontend true || true
		sudo apt-get install -y \
			make libbpf-dev clang git gcc jq capnproto python3 iputils-ping
		# Ubuntu 20.04 ships clang-10 which has a CO-RE relocation bug.
		[[ "$OS_DISTRO" != "ubuntu20.04" ]] || {
			sudo apt-get install -y clang-12
			sudo ln -sf /usr/bin/clang-12 /usr/local/bin/clang
		}
		;;
	esac
	which mockery || go install github.com/vektra/mockery/v2@latest
	which capnpc-go || go install capnproto.org/go/capnp/v3/capnpc-go@v3.1.0-alpha.2
	git config --global --add safe.directory /mnt/host
}

install_golang
prepare_bench_env

cd /mnt/host
make bench

echo -e "\n✅ benchmark finished; artifacts under /mnt/host/bench/results/"
ls -lah bench/results/ || true
