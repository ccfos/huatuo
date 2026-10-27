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

# Purpose: Stop and delete VM state, then safely remove the package extracted for the run.
# Caller: qemu-3-cleanup.sh owns cleanup and invokes this helper.
# Environment:
# - VM_ENV_FILE: Environment-file fallback when the positional parameter is omitted.
# - VM_STATE_DIR: State-directory fallback when the environment file does not provide one.
# - VM_RUNNER: Runner fallback when the environment file does not provide one.
# - VM_PACKAGE_DIR: Package-directory fallback when the environment file does not provide one.
# Parameters:
# - VM_ENV_FILE: Optional runner-generated environment file; overrides the variable of the same name.
# Examples:
# - cleanup-vm-run.sh /tmp/run/vm.env  # clean using an explicit environment file

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
source "$SCRIPT_DIR/logging.sh"

env_file=${1:-${VM_ENV_FILE:-}}
[[ -n "$env_file" ]] || {
	vm_log_error 'usage: cleanup-vm-run.sh VM_ENV_FILE'
	exit 2
}

state_dir=${VM_STATE_DIR:-}
runner=${VM_RUNNER:-}
package_dir=${VM_PACKAGE_DIR:-}
if [[ -f "$env_file" && ! -L "$env_file" ]]; then
	# vm.env is emitted by the checksum-verified packaged runner.
	source "$env_file"
	state_dir=${VM_STATE_DIR:-$state_dir}
	runner=${VM_RUNNER:-$runner}
	package_dir=${VM_PACKAGE_DIR:-$package_dir}
fi
run_root=$(dirname "$env_file")
state_dir=${state_dir:-$run_root/state}
package_dir=${package_dir:-$run_root/package}
runner=${runner:-$package_dir/bin/vm-runner}

if [[ -f "$state_dir/state.json" ]]; then
	[[ -x "$runner" ]] || {
		vm_log_error "cleanup: packaged runner is unavailable: $runner"
		exit 1
	}
	# Only the packaged runner understands and verifies the ownership recorded in state.json.
	"$runner" cleanup --state-dir "$state_dir"
else
	vm_log 'VM state is already absent; cleanup is not needed.'
fi

disk_owner="$run_root/test-disk.owner"
if [[ -e "$disk_owner" || -L "$disk_owner" ]]; then
	[[ -f "$disk_owner" && ! -L "$disk_owner" && "$(stat -c %u "$disk_owner")" == "$(id -u)" ]] || {
		vm_log_error "cleanup: refusing unsafe test disk ownership file: $disk_owner"
		exit 1
	}
	{
		IFS= read -r owned_disk || true
		IFS= read -r owned_run || true
		IFS= read -r owned_identity || true
	} < "$disk_owner"
	[[ -n "$owned_disk" && "$owned_run" == "$(realpath -- "$run_root")" &&
	"$owned_disk" == "$(sudo realpath -m -- "$owned_disk")" &&
	(-z "${VM_TEST_DISK:-}" || "$VM_TEST_DISK" == "$owned_disk") ]] || {
		vm_log_error "cleanup: test disk does not belong to this run: ${VM_TEST_DISK:-$owned_disk}"
		exit 1
	}
	if sudo test -e "$owned_disk" || sudo test -L "$owned_disk"; then
		if ! sudo test -f "$owned_disk" || sudo test -L "$owned_disk"; then
			vm_log_error "cleanup: refusing non-regular test disk: $owned_disk"
			exit 1
		fi
		[[ -n "$owned_identity" && "$(sudo stat -c '%d:%i' -- "$owned_disk")" == "$owned_identity" ]] || {
			vm_log_error "cleanup: test disk identity changed: $owned_disk"
			exit 1
		}
		sudo rm -f -- "$owned_disk"
	fi
	rm -f -- "$disk_owner"
elif [[ -n "${VM_TEST_DISK:-}" ]] && { sudo test -e "$VM_TEST_DISK" || sudo test -L "$VM_TEST_DISK"; }; then
	vm_log_error "cleanup: test disk ownership file is missing: $disk_owner"
	exit 1
fi

if [[ -e "$package_dir" ]]; then
	[[ -d "$package_dir" && ! -L "$package_dir" ]] || {
		vm_log_error "cleanup: refusing unsafe package path: $package_dir"
		exit 1
	}
	package_dir=$(realpath -- "$package_dir")
	owner_file="$(dirname "$package_dir")/package.owner"
	[[ -f "$owner_file" && ! -L "$owner_file" ]] || {
		vm_log_error "cleanup: package ownership file is missing: $owner_file"
		exit 1
	}
	IFS= read -r owned_package < "$owner_file"
	[[ "$owned_package" == "$package_dir" && "$(stat -c %u "$package_dir")" == "$(id -u)" ]] || {
		vm_log_error "cleanup: refusing package directory not owned by this run: $package_dir"
		exit 1
	}
	rm -rf -- "$package_dir"
	rm -f -- "$owner_file"
	vm_log "removed extracted VM package: $package_dir"
fi

rmdir "$run_root" 2> /dev/null || true
