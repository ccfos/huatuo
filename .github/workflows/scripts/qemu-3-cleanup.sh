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

# Purpose: Own the always-run cleanup of VM state and the extracted package.
# Caller: GitHub workflow os-distro-qemu-test.yml, as the qemu-3 step; qemu-local-test.sh locally.
# Environment:
# - VM_ENV_FILE: Optional qemu-1 environment file; an unset value is a successful no-op.
# Parameters:
# - None.
# Examples:
# - VM_ENV_FILE=/tmp/run/vm.env qemu-3-cleanup.sh  # clean a completed VM run
# - qemu-3-cleanup.sh  # succeed when qemu-1 created no state

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)
source "$ROOT_DIR/.github/workflows/scripts/vm-test/logging.sh"
trap 'vm_phase_result qemu-3 "$?"' EXIT
if [[ -z "${VM_ENV_FILE:-}" ]]; then
	vm_log 'VM state was not created; cleanup is not needed.'
	exit 0
fi
"$ROOT_DIR/.github/workflows/scripts/vm-test/cleanup-vm-run.sh" "$VM_ENV_FILE"
