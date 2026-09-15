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

# Purpose: Extract a verified VM package, start the VM, and sync the source tree.
# Caller: GitHub workflow os-distro-qemu-test.yml, as the qemu-1 step; qemu-local-test.sh locally.
# Environment:
# - VM_IMAGE_REF: Image reference; defaults from DISTRO and ARCH.
# - VM_IMAGE_PULL: Image pull policy, always (default) or never.
# - RUNNER_TEMP: Preferred parent directory for per-run storage.
# - TMPDIR: Storage fallback when RUNNER_TEMP is unset; defaults to /tmp.
# - VM_CONTEXT_OUTPUT: File that receives state for subsequent caller steps.
# - GITHUB_ENV: Workflow state output fallback when VM_CONTEXT_OUTPUT is unset.
# Parameters:
# - ARCH: Optional architecture; defaults to amd64.
# - DISTRO: Optional distribution; defaults to ubuntu24.04.
# - NAME: Optional VM name.
# - MAC: Optional VM MAC address.
# - IP: Optional VM IP address.
# Examples:
# - qemu-1-start-vm.sh  # start the default Ubuntu amd64 VM
# - qemu-1-start-vm.sh arm64 ubuntu24.04 huatuo-vm 4A:6F:6C:69:6E:20  # set VM identity

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)
source "$ROOT_DIR/.github/workflows/scripts/vm-test/logging.sh"
trap 'vm_phase_result qemu-1 "$?"' EXIT
ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}
VM_NAME=${3:-}
VM_MAC=${4:-}
VM_IP=${5:-}
VM_IMAGE_REF=${VM_IMAGE_REF:-huatuo/os-distro-test:${OS_DISTRO}.${ARCH}}
VM_IMAGE_PULL=${VM_IMAGE_PULL:-always}

run_root=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/huatuo-vm-${OS_DISTRO}-${ARCH}.XXXXXXXX")
SSH_KEY="$run_root/id_ed25519_vm"
package_dir="$run_root/package"
state_dir="$run_root/state"
env_file="$run_root/vm.env"
runner="$package_dir/bin/vm-runner"
context_output=${VM_CONTEXT_OUTPUT:-${GITHUB_ENV:-}}
printf '%s\n' "$package_dir" > "$run_root/package.owner"

write_context() {
	[[ -n "$context_output" ]] || return
	printf 'VM_STATE_DIR=%s\nVM_RUNNER=%s\nVM_PACKAGE_DIR=%s\nVM_ENV_FILE=%s\n' \
		"$state_dir" "$runner" "$package_dir" "$env_file" >> "$context_output"
}
write_context

"$ROOT_DIR/.github/workflows/scripts/vm-test/extract-and-verify-package.sh" \
	--image "$VM_IMAGE_REF" --package-dir "$package_dir" \
	--distro "$OS_DISTRO" --arch "$ARCH" --pull "$VM_IMAGE_PULL"

start_args=(
	start
	--package "$package_dir"
	--state-dir "$state_dir"
	--env-output "$env_file"
	--ssh-key "$SSH_KEY"
	--vcpus 4
	--memory 8192
	--disk-size 20G
	--init-kubernetes
)
[[ -z "$VM_NAME" ]] || start_args+=(--name "$VM_NAME")
[[ -z "$VM_MAC" ]] || start_args+=(--mac "$VM_MAC")
[[ -z "$VM_IP" ]] || start_args+=(--ip "$VM_IP")
# The package checksum verification above makes this runner the trusted VM lifecycle boundary.
"$runner" "${start_args[@]}"

printf 'VM_PACKAGE_DIR=%q\nVM_ENV_FILE=%q\nVM_DISTRO=%q\nVM_ARCH=%q\n' \
	"$package_dir" "$env_file" "$OS_DISTRO" "$ARCH" >> "$env_file"
# shellcheck disable=SC1090
source "$env_file"
if [[ -n "$context_output" ]]; then
	printf 'VM_IP=%s\nVM_NAME=%s\nVM_MAC=%s\nVM_SSH_KEY=%s\n' \
		"$VM_IP" "$VM_NAME" "$VM_MAC" "$VM_SSH_KEY" >> "$context_output"
fi
"$ROOT_DIR/.github/workflows/scripts/vm-test/sync-source-to-vm.sh" "$env_file"
vm_log "VM environment: $env_file"
