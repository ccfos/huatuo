#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MEMRAY_VENDOR_DIR="${1:-$ROOT_DIR/third_party/memray-lite}"
MEMRAY_BUNDLE_OUTPUT="${2:-$ROOT_DIR/_output/tools/memray-lite}"
MEMRAY_PYTHON_BIN="${MEMRAY_PYTHON_BIN:-python3}"
MEMRAY_PYTHON_BINS="${MEMRAY_PYTHON_BINS:-}"
MEMRAY_WHEEL_OUTPUT="${MEMRAY_WHEEL_OUTPUT:-}"

normalize_path() {
	local path="$1"
	if [[ "$path" = /* ]]; then
		printf '%s\n' "$path"
	else
		printf '%s/%s\n' "$PWD" "$path"
	fi
}

MEMRAY_VENDOR_DIR="$(normalize_path "$MEMRAY_VENDOR_DIR")"
MEMRAY_BUNDLE_OUTPUT="$(normalize_path "$MEMRAY_BUNDLE_OUTPUT")"
if [[ -n "$MEMRAY_WHEEL_OUTPUT" ]]; then
	MEMRAY_WHEEL_OUTPUT="$(normalize_path "$MEMRAY_WHEEL_OUTPUT")"
fi

if [[ ! -d "$MEMRAY_VENDOR_DIR" ]]; then
	echo "memray-lite vendor directory not found: $MEMRAY_VENDOR_DIR" >&2
	exit 1
fi

if [[ ! -f "$MEMRAY_VENDOR_DIR/scripts/bundle_memray.py" ]]; then
	echo "bundle script not found: $MEMRAY_VENDOR_DIR/scripts/bundle_memray.py" >&2
	exit 1
fi

mkdir -p "$(dirname "$MEMRAY_BUNDLE_OUTPUT")"

python_version() {
	local python_bin="$1"
	"$python_bin" -c 'import sys; print(f"{sys.version_info[0]}.{sys.version_info[1]}")'
}

python_missing_modules() {
	local python_bin="$1"
	"$python_bin" - << 'PY'
import importlib.util

required = ["pip", "setuptools", "wheel", "Cython", "pkgconfig"]
missing = [name for name in required if importlib.util.find_spec(name) is None]
print(" ".join(missing))
PY
}

build_wheel() {
	local python_bin="$1"
	local outdir="$2"

	if "$python_bin" -m build --help > /dev/null 2>&1; then
		# Avoid isolated builds here because some host pip mirrors cannot resolve
		# setuptools into the temporary build env, while the required build deps are
		# already installed on the Huatuo build host.
		"$python_bin" -m build --wheel --no-isolation --outdir "$outdir"
		return
	fi

	"$python_bin" setup.py bdist_wheel --dist-dir "$outdir"
}

declare -A MEMRAY_PYTHON_BY_VERSION=()
MEMRAY_TEMP_DIRS=()
REGISTER_PYTHON_ERROR=""

cleanup_temp_dirs() {
	local path
	for path in "${MEMRAY_TEMP_DIRS[@]}"; do
		rm -rf "$path"
	done
}

trap cleanup_temp_dirs EXIT

register_python_bin() {
	local python_bin="$1"
	local resolved_bin
	local version
	local missing_modules

	REGISTER_PYTHON_ERROR=""

	if ! resolved_bin="$(command -v "$python_bin" 2> /dev/null)"; then
		return 1
	fi
	version="$(python_version "$resolved_bin")"
	if [[ -z "$version" ]]; then
		return 1
	fi
	missing_modules="$(python_missing_modules "$resolved_bin")"
	if [[ -n "$missing_modules" ]]; then
		REGISTER_PYTHON_ERROR="$resolved_bin:$missing_modules"
		return 2
	fi
	if [[ -z "${MEMRAY_PYTHON_BY_VERSION[$version]:-}" ]]; then
		MEMRAY_PYTHON_BY_VERSION["$version"]="$resolved_bin"
	fi
	return 0
}

if [[ -n "$MEMRAY_PYTHON_BINS" ]]; then
	for python_bin in $MEMRAY_PYTHON_BINS; do
		register_status=0
		register_python_bin "$python_bin" || register_status=$?
		if [[ "$register_status" -eq 0 ]]; then
			continue
		fi
		case "$register_status" in
		1)
			echo "python interpreter not found or unusable: $python_bin" >&2
			exit 1
			;;
		2)
			echo "python interpreter missing build modules: $REGISTER_PYTHON_ERROR" >&2
			exit 1
			;;
		esac
	done
else
	for python_bin in python3.6 python3.7 python3.8 python3.9 python3.10 python3.11 python3.12 python3.13 "$MEMRAY_PYTHON_BIN"; do
		register_status=0
		register_python_bin "$python_bin" || register_status=$?
		if [[ "$register_status" -eq 0 ]]; then
			continue
		fi
		case "$register_status" in
		2)
			echo "skip memray runtime build for $REGISTER_PYTHON_ERROR" >&2
			;;
		esac
	done
fi

if [[ "${#MEMRAY_PYTHON_BY_VERSION[@]}" -eq 0 ]]; then
	echo "no usable python interpreter found for memray bundle build" >&2
	exit 1
fi

if [[ -n "$MEMRAY_WHEEL_OUTPUT" && "${#MEMRAY_PYTHON_BY_VERSION[@]}" -ne 1 ]]; then
	echo "MEMRAY_WHEEL_OUTPUT only supports single-version bundle builds" >&2
	exit 1
fi

rm -rf "$MEMRAY_BUNDLE_OUTPUT"
mkdir -p "$MEMRAY_BUNDLE_OUTPUT/runtimes"

cd "$MEMRAY_VENDOR_DIR"

mapfile -t MEMRAY_VERSIONS < <(printf '%s\n' "${!MEMRAY_PYTHON_BY_VERSION[@]}" | sort -V)

for version in "${MEMRAY_VERSIONS[@]}"; do
	MEMRAY_PYTHON_BIN="${MEMRAY_PYTHON_BY_VERSION[$version]}"
	MEMRAY_RUNTIME_KEY="py${version}"
	MEMRAY_RUNTIME_OUTPUT="$MEMRAY_BUNDLE_OUTPUT/runtimes/$MEMRAY_RUNTIME_KEY"
	MEMRAY_COMMANDS_OUTPUT="$MEMRAY_RUNTIME_OUTPUT/python/memray/commands"

	if [[ -n "$MEMRAY_WHEEL_OUTPUT" ]]; then
		WHEEL_PATH="$MEMRAY_WHEEL_OUTPUT"
	else
		WHEEL_DIR="$(mktemp -d "${TMPDIR:-/tmp}/memray-wheel.${MEMRAY_RUNTIME_KEY}.XXXXXX")"
		MEMRAY_TEMP_DIRS+=("$WHEEL_DIR")
		build_wheel "$MEMRAY_PYTHON_BIN" "$WHEEL_DIR"
		WHEEL_PATH="$(find "$WHEEL_DIR" -maxdepth 1 -name 'memray-*.whl' -print | sort | tail -n 1)"
		if [[ -z "$WHEEL_PATH" ]]; then
			echo "memray wheel build did not produce a wheel for $MEMRAY_RUNTIME_KEY" >&2
			exit 1
		fi
	fi

	"$MEMRAY_PYTHON_BIN" scripts/bundle_memray.py \
		--python "$MEMRAY_PYTHON_BIN" \
		--wheel "$WHEEL_PATH" \
		--clean \
		--output "$MEMRAY_RUNTIME_OUTPUT"

	# The wheel build currently omits these attach helper scripts, but profiler2
	# still relies on the GDB payload being present in the final bundle.
	mkdir -p "$MEMRAY_COMMANDS_OUTPUT"
	cp -f src/memray/commands/_attach.gdb "$MEMRAY_COMMANDS_OUTPUT/_attach.gdb"
	cp -f src/memray/commands/_attach.lldb "$MEMRAY_COMMANDS_OUTPUT/_attach.lldb"
done

# Keep the vendored source tree clean after bundle generation so this step
# remains a build wiring change instead of a source mutation.
rm -rf build src/memray.egg-info src/vendor/libbacktrace/install src/memray/_memray.cpp
