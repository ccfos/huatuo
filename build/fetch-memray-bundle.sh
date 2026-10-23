#!/usr/bin/env bash
set -euo pipefail

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

checksum_marker="$MEMRAY_BUNDLE_OUTPUT/.bundle-sha256"
if [[ -f "$checksum_marker" ]] \
	&& [[ "$(< "$checksum_marker")" == "$MEMRAY_BUNDLE_SHA256" ]]; then
	echo "Memray bundle $MEMRAY_BUNDLE_VERSION for $MEMRAY_BUNDLE_ARCHITECTURE is already installed"
	exit 0
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
if [[ ! -f "$bundle_root/manifest.json" || ! -d "$bundle_root/runtimes" ]]; then
	echo "Memray archive does not contain the expected memray bundle root" >&2
	exit 1
fi

for version in 3.7 3.8 3.9 3.10 3.11 3.12 3.13 3.14; do
	if [[ ! -f "$bundle_root/runtimes/py$version/runtime.json" ]]; then
		echo "Memray archive is missing the Python $version runtime" >&2
		exit 1
	fi
done

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
