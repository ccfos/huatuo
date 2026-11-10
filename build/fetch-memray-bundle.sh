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

set -euo pipefail
shopt -s nullglob

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MEMRAY_BUNDLE_OUTPUT="${1:-$ROOT_DIR/_output/tools/memray}"
MEMRAY_BUNDLE_VERSION="${MEMRAY_BUNDLE_VERSION:-1.19.1-1}"
MEMRAY_BUNDLE_REPOSITORY="${MEMRAY_BUNDLE_REPOSITORY:-https://github.com/hao-lee/memray}"
MEMRAY_BUNDLE_ARCHITECTURE="${MEMRAY_BUNDLE_ARCHITECTURE:-$(uname -m)}"
MEMRAY_BUNDLE_ARCHIVE="${MEMRAY_BUNDLE_ARCHIVE:-}"
MEMRAY_BUNDLE_SHA256="${MEMRAY_BUNDLE_SHA256:-}"

case "$MEMRAY_BUNDLE_ARCHITECTURE" in
x86_64 | amd64)
	MEMRAY_BUNDLE_ARCHITECTURE=x86_64
	;;
aarch64 | arm64)
	MEMRAY_BUNDLE_ARCHITECTURE=aarch64
	;;
*)
	echo "unsupported Memray bundle architecture: $MEMRAY_BUNDLE_ARCHITECTURE" >&2
	exit 1
	;;
esac

if [[ -z "$MEMRAY_BUNDLE_SHA256" ]]; then
	case "$MEMRAY_BUNDLE_VERSION:$MEMRAY_BUNDLE_ARCHITECTURE" in
	1.19.1-1:x86_64)
		MEMRAY_BUNDLE_SHA256=c82e0df79d26a9d7a84a98647d9cbff470e6992655833345ebe73c9ac1df13d6
		;;
	1.19.1-1:aarch64)
		MEMRAY_BUNDLE_SHA256=b75702ee32e45d876e0e294e75dee4b9d89238320390d783202a4c6f4169f4d6
		;;
	*)
		echo "no pinned checksum for Memray bundle $MEMRAY_BUNDLE_VERSION/$MEMRAY_BUNDLE_ARCHITECTURE" >&2
		echo "set MEMRAY_BUNDLE_SHA256 when overriding the bundle version" >&2
		exit 1
		;;
	esac
fi

if [[ "$MEMRAY_BUNDLE_OUTPUT" != /* ]]; then
	MEMRAY_BUNDLE_OUTPUT="$PWD/$MEMRAY_BUNDLE_OUTPUT"
fi
if [[ "$MEMRAY_BUNDLE_OUTPUT" == / ]]; then
	echo "refusing to install a Memray bundle at /" >&2
	exit 1
fi

archive_name="memray-huatuo-${MEMRAY_BUNDLE_VERSION}-linux-glibc-${MEMRAY_BUNDLE_ARCHITECTURE}.tar.gz"
release_tag="huatuo-bundle-v${MEMRAY_BUNDLE_VERSION}"
archive_url="${MEMRAY_BUNDLE_REPOSITORY}/releases/download/${release_tag}/${archive_name}"
output_parent="$(dirname "$MEMRAY_BUNDLE_OUTPUT")"
mkdir -p "$output_parent"

# Keep the inventory stable across traversal order and Python bytecode writes.
bundle_files() {
	local bundle=$1 path
	find "$bundle" -type d -name __pycache__ -prune -o -type f \
		! -path "$bundle/.bundle-files" ! -path "$bundle/.bundle-checksums" \
		! -path "$bundle/.bundle-sha256" -print0 \
		| while IFS= read -r -d '' path; do
			printf './%s\0' "${path#"$bundle/"}"
		done | LC_ALL=C sort -z
}

# Recompute the complete manifest rather than trusting paths in a cached one.
bundle_checksums() {
	(
		cd "$1" || exit 1
		bundle_files . | xargs -0 -r sha256sum -- || exit 1
		sha256sum -- .bundle-files
	)
}

validate_bundle() {
	local bundle=$1 version runtime file path actual_checksum_hash expected_checksum_hash
	local -a extensions injectors
	# Runtime fingerprinting and container staging do not follow source links.
	# Keep host-only installations subject to the same regular-file contract.
	path="$(find "$bundle" ! -type d ! -type f -print -quit)" || return 1
	if [[ -n "$path" ]]; then
		echo "unsupported file in Memray bundle (only regular files and directories are allowed): $path" >&2
		return 1
	fi
	for file in manifest.json .bundle-files .bundle-checksums; do
		if [[ ! -f "$bundle/$file" || ! -r "$bundle/$file" || ! -s "$bundle/$file" ]]; then
			echo "Memray bundle is missing or has an empty file: $bundle/$file" >&2
			return 1
		fi
	done
	for version in 3.7 3.8 3.9 3.10 3.11 3.12 3.13 3.14; do
		runtime="$bundle/runtimes/py$version"
		for file in runtime.json python/memray/__init__.py python/memray/_metadata.py \
			python/memray/_version.py python/memray/_ipython/__init__.py \
			python/memray/commands/_attach.gdb python/memray/commands/_attach.lldb; do
			path="$runtime/$file"
			if [[ ! -f "$path" || ! -r "$path" || ! -s "$path" ]]; then
				echo "Memray bundle is missing or has an empty file: $path" >&2
				return 1
			fi
		done
		extensions=("$runtime/python/memray/"_memray*.so)
		injectors=("$runtime/python/memray/"_inject*.so)
		if [[ ${#extensions[@]} != 1 || ${#injectors[@]} != 1 ]]; then
			echo "Memray bundle must contain one extension and one injector for Python $version" >&2
			return 1
		fi
		for path in "${extensions[@]}" "${injectors[@]}"; do
			if [[ ! -f "$path" || ! -r "$path" || ! -s "$path" ]]; then
				echo "Memray bundle shared object is missing or empty: $path" >&2
				return 1
			fi
		done
	done
	# Include vendored dependencies without maintaining their file names here.
	while IFS= read -r -d '' file; do
		if [[ ! -f "$bundle/$file" || ! -r "$bundle/$file" ]]; then
			echo "Memray bundle file is missing or unreadable: $bundle/$file" >&2
			return 1
		fi
	done < "$bundle/.bundle-files"
	actual_checksum_hash="$(bundle_checksums "$bundle" | sha256sum)" || return 1
	expected_checksum_hash="$(sha256sum < "$bundle/.bundle-checksums")" || return 1
	if [[ "$actual_checksum_hash" != "$expected_checksum_hash" ]]; then
		echo "Memray bundle file contents or inventory have changed: $bundle" >&2
		return 1
	fi
}

checksum_marker="$MEMRAY_BUNDLE_OUTPUT/.bundle-sha256"
if [[ -f "$checksum_marker" ]] \
	&& [[ "$(< "$checksum_marker")" == "$MEMRAY_BUNDLE_SHA256" ]]; then
	if validate_bundle "$MEMRAY_BUNDLE_OUTPUT"; then
		echo "Memray bundle $MEMRAY_BUNDLE_VERSION for $MEMRAY_BUNDLE_ARCHITECTURE is already installed"
		exit 0
	fi
	echo "Memray bundle cache is incomplete or changed; reinstalling" >&2
fi

work_dir="$(mktemp -d "$output_parent/.memray-bundle.XXXXXX")"
backup_path=""

cleanup() {
	rm -rf "$work_dir"
	if [[ -n "$backup_path" && -e "$backup_path" ]]; then
		if [[ -e "$MEMRAY_BUNDLE_OUTPUT" ]]; then
			rm -rf "$backup_path"
		else
			mv "$backup_path" "$MEMRAY_BUNDLE_OUTPUT"
		fi
	fi
}
trap cleanup EXIT

download_path="$work_dir/$archive_name"
if [[ -n "$MEMRAY_BUNDLE_ARCHIVE" ]]; then
	if [[ ! -f "$MEMRAY_BUNDLE_ARCHIVE" ]]; then
		echo "Memray bundle archive not found: $MEMRAY_BUNDLE_ARCHIVE" >&2
		exit 1
	fi
	cp "$MEMRAY_BUNDLE_ARCHIVE" "$download_path"
else
	echo "downloading Memray bundle $MEMRAY_BUNDLE_VERSION for $MEMRAY_BUNDLE_ARCHITECTURE"
	curl --fail --location --retry 3 --output "$download_path" "$archive_url"
fi

actual_sha256="$(sha256sum "$download_path" | awk '{print $1}')"
if [[ "$actual_sha256" != "$MEMRAY_BUNDLE_SHA256" ]]; then
	echo "Memray bundle checksum mismatch for $archive_name" >&2
	echo "expected: $MEMRAY_BUNDLE_SHA256" >&2
	echo "actual:   $actual_sha256" >&2
	exit 1
fi

extract_dir="$work_dir/extract"
mkdir -p "$extract_dir"
tar -xzf "$download_path" -C "$extract_dir"

bundle_root="$extract_dir/memray"
if [[ ! -d "$bundle_root" ]]; then
	echo "Memray archive does not contain the expected memray bundle root" >&2
	exit 1
fi

file_list="$bundle_root/.bundle-files"
bundle_files "$bundle_root" > "$file_list"
bundle_checksums "$bundle_root" > "$bundle_root/.bundle-checksums"
validate_bundle "$bundle_root"

printf '%s\n' "$MEMRAY_BUNDLE_SHA256" > "$bundle_root/.bundle-sha256"

if [[ -e "$MEMRAY_BUNDLE_OUTPUT" ]]; then
	backup_path="$output_parent/.memray-backup.$$"
	mv "$MEMRAY_BUNDLE_OUTPUT" "$backup_path"
fi

if ! mv "$bundle_root" "$MEMRAY_BUNDLE_OUTPUT"; then
	if [[ -n "$backup_path" && -e "$backup_path" ]]; then
		mv "$backup_path" "$MEMRAY_BUNDLE_OUTPUT"
		backup_path=""
	fi
	exit 1
fi

if [[ -n "$backup_path" ]]; then
	rm -rf "$backup_path"
	backup_path=""
fi

echo "installed Memray bundle at $MEMRAY_BUNDLE_OUTPUT"
