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

# Purpose: Extract /data from a VM image and verify its metadata and checksums.
# Caller: qemu-1-start-vm.sh invokes this helper before starting the VM.
# Environment:
# - VM_IMAGE_PULL_TIMEOUT: Optional timeout for each registry request.
# Parameters:
# - --image IMAGE: Required VM package image reference.
# - --package-dir DIR: Required empty extraction directory.
# - --distro NAME: Required expected distribution name.
# - --arch ARCH: Required expected architecture.
# - --pull always|never: Optional image pull policy; defaults to always.
# - --verify-only: Validate an existing extracted package.
# - -h, --help: Print usage information.
# Examples:
# - extract-and-verify-package.sh --image repo/vm:ubuntu.amd64 --package-dir /tmp/package --distro ubuntu --arch amd64 --pull never  # extract a local image

set -euo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
source "$SCRIPT_DIR/logging.sh"

usage() {
	cat << 'EOF'
Usage: extract-and-verify-package.sh --image IMAGE --package-dir DIR --distro NAME --arch ARCH [--pull always|never | --verify-only]
EOF
}

fail() {
	vm_log_error "extract-and-verify-package: $*"
	exit 1
}

image=""
package_dir=""
expected_distro=""
expected_arch=""
pull=always
verify_only=false
while (($#)); do
	case "$1" in
	--image)
		image=${2:?missing value for --image}
		shift 2
		;;
	--package-dir)
		package_dir=${2:?missing value for --package-dir}
		shift 2
		;;
	--distro)
		expected_distro=${2:?missing value for --distro}
		shift 2
		;;
	--arch)
		expected_arch=${2:?missing value for --arch}
		shift 2
		;;
	--pull)
		pull=${2:?missing value for --pull}
		shift 2
		;;
	--verify-only)
		verify_only=true
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) fail "unknown option: $1" ;;
	esac
done

[[ "$(uname -s)" == Linux ]] || fail "only Linux hosts are supported"
command -v jq > /dev/null 2>&1 || fail "required command not found: jq"
command -v sha256sum > /dev/null 2>&1 || fail "required command not found: sha256sum"
[[ -n "$image" && -n "$package_dir" && -n "$expected_distro" && -n "$expected_arch" ]] \
	|| fail "--image, --package-dir, --distro, and --arch are required"
[[ "$pull" == always || "$pull" == never ]] || fail "--pull must be always or never"
[[ ! -L "$package_dir" ]] || fail "package directory must not be a symbolic link: $package_dir"

extract_package() (
	command -v docker > /dev/null 2>&1 || fail "required command not found: docker"
	if [[ -e "$package_dir" ]]; then
		[[ -d "$package_dir" ]] || fail "package path is not a directory: $package_dir"
		[[ -z "$(ls -A -- "$package_dir")" ]] || fail "package directory must be empty: $package_dir"
	else
		mkdir -p -- "$package_dir"
	fi

	docker_config=$(mktemp -d)
	container=""
	cleanup() {
		if [[ -n "$container" ]]; then
			docker --config "$docker_config" rm -f "$container" > /dev/null 2>&1 || true
		fi
		rm -rf -- "$docker_config"
	}
	trap cleanup EXIT
	printf '{}\n' > "$docker_config/config.json"

	if [[ "$pull" == always ]]; then
		pull_command=(docker --config "$docker_config" pull "$image")
		if [[ -n ${VM_IMAGE_PULL_TIMEOUT:-} ]]; then
			pull_command=(timeout -k 10s "$VM_IMAGE_PULL_TIMEOUT" "${pull_command[@]}")
		fi
		for attempt in 1 2 3; do
			if "${pull_command[@]}"; then
				break
			else
				pull_status=$?
			fi
			vm_log_error "VM image pull attempt $attempt/3 failed for $image (exit $pull_status)"
			# A client timeout can leave the daemon downloading the previous request.
			((pull_status != 124 && pull_status != 137 && attempt < 3)) \
				|| fail "cannot pull $image; check registry connectivity and availability"
			sleep "$((attempt * 10))"
		done
	elif ! docker --config "$docker_config" image inspect "$image" > /dev/null 2>&1; then
		fail "local Docker image not found with --pull never: $image"
	fi
	container=$(docker --config "$docker_config" create "$image" /bin/true)
	docker --config "$docker_config" cp "$container:/data/." "$package_dir/" \
		|| fail "could not extract /data from $image"
)

if [[ "$verify_only" == true ]]; then
	[[ -d "$package_dir" ]] || fail "package path is not a directory: $package_dir"
else
	extract_package
fi
package_dir=$(realpath -- "$package_dir")

release="$package_dir/release.json"
sums="$package_dir/SHA256SUMS"
runner="$package_dir/bin/vm-runner"
[[ -d "$package_dir/bin" && ! -L "$package_dir/bin" ]] \
	|| fail "package directory is missing or unsafe: bin"
for path in "$release" "$sums" "$runner"; do
	[[ -f "$path" && ! -L "$path" ]] || fail "package file is missing or unsafe: ${path#"$package_dir/"}"
done
jq -e --arg distro "$expected_distro" --arg arch "$expected_arch" '
	(keys | sort) == (["architecture", "cloudInit", "created", "distro", "osVariant", "runnerApi", "schemaVersion"] | sort) and
	.schemaVersion == 1 and .runnerApi == 1 and
	(.created | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")) and
	(.distro | type == "string" and test("^[A-Za-z0-9._-]+$")) and
	(.architecture | type == "string" and test("^[A-Za-z0-9._-]+$")) and
	.distro == $distro and .architecture == $arch and
	(.osVariant | type == "string" and length > 0) and
	(.cloudInit | type == "boolean")
' "$release" > /dev/null || fail "unsupported or invalid release.json"

distro=$(jq -r .distro "$release")
arch=$(jq -r .architecture "$release")
archive="$package_dir/${distro}-${arch}.qcow2.zst"
[[ -f "$archive" && ! -L "$archive" ]] || fail "package archive is missing or unsafe: ${archive##*/}"

runner_hash=""
raw_seen=0
runner_seen=0
line_count=0
while IFS= read -r line || [[ -n "$line" ]]; do
	((line_count += 1))
	[[ "$line" =~ ^([0-9a-f]{64})\ \ ([^[:space:]]+)$ ]] || fail "invalid SHA256SUMS entry"
	hash=${BASH_REMATCH[1]}
	name=${BASH_REMATCH[2]}
	case "$name" in
	bin/vm-runner)
		runner_hash=$hash
		((runner_seen += 1))
		;;
	"${distro}-${arch}.qcow2") ((raw_seen += 1)) ;;
	*) fail "unexpected SHA256SUMS path: $name" ;;
	esac
done < "$sums"
[[ "$line_count" == 2 && "$runner_seen" == 1 && "$raw_seen" == 1 ]] \
	|| fail "SHA256SUMS must contain exactly one raw qcow2 and bin/vm-runner"
actual_hash=$(sha256sum "$runner" | awk '{print $1}')
[[ "$actual_hash" == "$runner_hash" ]] || fail "packaged runner checksum mismatch"
[[ -x "$runner" ]] || fail "packaged runner is not executable"

vm_log "validated $image package in $package_dir"
