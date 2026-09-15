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

# Purpose: Stream the repository-owned guest test script into the running VM.
# Caller: GitHub workflow os-distro-qemu-test.yml, as the qemu-2 step; qemu-local-test.sh locally.
# Environment:
# - VM_ENV_FILE: Required qemu-1 environment file containing VM connection state.
# - VM_PROXY_HOST: Optional host proxy address; requires VM_PROXY_PORT.
# - VM_PROXY_PORT: Optional host proxy port; requires VM_PROXY_HOST.
# Parameters:
# - None.
# Examples:
# - VM_ENV_FILE=/tmp/run/vm.env qemu-2-run-in-vm.sh

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)
source "$ROOT_DIR/.github/workflows/scripts/vm-test/logging.sh"
trap 'vm_phase_result qemu-2 "$?"' EXIT
(($# == 0)) || {
	vm_log_error 'usage: qemu-2-run-in-vm.sh'
	exit 2
}
: "${VM_ENV_FILE:?VM_ENV_FILE is required; run qemu-1-start-vm.sh first}"
[[ -f "$VM_ENV_FILE" && ! -L "$VM_ENV_FILE" ]] || {
	vm_log_error "qemu-2: vm.env is missing or unsafe: $VM_ENV_FILE"
	exit 1
}
# vm.env is emitted by the checksum-verified packaged runner.
source "$VM_ENV_FILE"

proxy_host=${VM_PROXY_HOST:-}
proxy_port=${VM_PROXY_PORT:-}
proxy_guest_port=11008
if [[ -n "$proxy_host" || -n "$proxy_port" ]]; then
	[[ -n "$proxy_host" && -n "$proxy_port" ]] || {
		vm_log_error 'qemu-2: VM_PROXY_HOST and VM_PROXY_PORT must be set together'
		exit 2
	}
	[[ "$proxy_host" =~ ^[A-Za-z0-9.-]+$ ]] || {
		vm_log_error "qemu-2: invalid VM_PROXY_HOST: $proxy_host"
		exit 2
	}
	[[ "$proxy_port" =~ ^[0-9]{1,5}$ ]] || {
		vm_log_error "qemu-2: invalid VM_PROXY_PORT: $proxy_port"
		exit 2
	}
	proxy_port=$((10#$proxy_port))
	((proxy_port >= 1 && proxy_port <= 65535)) || {
		vm_log_error "qemu-2: invalid VM_PROXY_PORT: $proxy_port"
		exit 2
	}
	if ! timeout 2 bash -c 'exec 3<>"/dev/tcp/$1/$2"' \
		_ "$proxy_host" "$proxy_port" 2> /dev/null; then
		vm_log_error "qemu-2: configured proxy is not reachable: ${proxy_host}:${proxy_port}"
		exit 1
	fi
fi
ssh_opts=(
	-i "$VM_SSH_KEY"
	-o BatchMode=yes
	-o ExitOnForwardFailure=yes
	-o StrictHostKeyChecking=no
	-o UserKnownHostsFile=/dev/null
)
if [[ -n "$proxy_port" ]]; then
	ssh_opts+=(-R "${proxy_guest_port}:${proxy_host}:${proxy_port}")
fi

# The streamed script owns all guest setup and test commands; qemu-2 only provides transport.
vm_log "running guest tests on $VM_IP"
ssh "${ssh_opts[@]}" "root@$VM_IP" bash -s \
	< "$ROOT_DIR/.github/workflows/scripts/vm-test/run-in-vm.sh"
