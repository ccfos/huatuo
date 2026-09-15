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

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS_DIR="$ROOT_DIR/_output/tools"
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

ASYNC_PROFILER_VERSION=4.1
PY_SPY_VERSION=0.4.1

case "${TEST_TOOLS_ARCH:-$(uname -m)}" in
x86_64 | amd64)
	ASYNC_PROFILER_ARCH=x64
	ASYNC_PROFILER_SHA256=3b13a38a0063f6970d985a379ddaed91bcf37e239a1ea461d09eacf629f3dde1
	PY_SPY_PLATFORM=manylinux_2_5_x86_64.manylinux1_x86_64
	PY_SPY_SHA256=6a80ec05eb8a6883863a367c6a4d4f2d57de68466f7956b6367d4edd5c61bb29
	;;
aarch64 | arm64)
	ASYNC_PROFILER_ARCH=arm64
	ASYNC_PROFILER_SHA256=d0cb9c97c380672b625c06e5a3ed578e990f4674c6aae8b5249f584c4c9ac50e
	PY_SPY_PLATFORM=manylinux_2_17_aarch64.manylinux2014_aarch64
	PY_SPY_SHA256=ee776b9d512a011d1ad3907ed53ae32ce2f3d9ff3e1782236554e22103b5c084
	;;
*)
	echo "unsupported install-tools architecture: ${TEST_TOOLS_ARCH:-$(uname -m)}" >&2
	exit 1
	;;
esac

ASYNC_PROFILER_URL="https://github.com/async-profiler/async-profiler/releases/download/v${ASYNC_PROFILER_VERSION}/async-profiler-${ASYNC_PROFILER_VERSION}-linux-${ASYNC_PROFILER_ARCH}.tar.gz"
PY_SPY_URL="https://github.com/benfred/py-spy/releases/download/v${PY_SPY_VERSION}/py_spy-${PY_SPY_VERSION}-py2.py3-none-${PY_SPY_PLATFORM}.whl"

ASYNC_PROFILER_ARCHIVE="$WORK_DIR/async-profiler.tar.gz"
PY_SPY_WHEEL="$WORK_DIR/py-spy.whl"
curl_retry_args=(--retry 5 --retry-delay 2)
if curl --help all 2> /dev/null | grep -q -- '--retry-all-errors'; then
	curl_retry_args+=(--retry-all-errors)
fi
curl -fsSL --connect-timeout 15 --max-time 300 \
	"${curl_retry_args[@]}" \
	"$ASYNC_PROFILER_URL" -o "$ASYNC_PROFILER_ARCHIVE"
curl -fsSL --connect-timeout 15 --max-time 300 \
	"${curl_retry_args[@]}" \
	"$PY_SPY_URL" -o "$PY_SPY_WHEEL"
printf '%s  %s\n' "$ASYNC_PROFILER_SHA256" "$ASYNC_PROFILER_ARCHIVE" \
	| sha256sum --check --strict -
printf '%s  %s\n' "$PY_SPY_SHA256" "$PY_SPY_WHEEL" \
	| sha256sum --check --strict -

mkdir -p "$WORK_DIR/tools/async-profiler"
tar -xzf "$ASYNC_PROFILER_ARCHIVE" --strip-components=1 \
	-C "$WORK_DIR/tools/async-profiler"
mkdir -p "$WORK_DIR/tools/py-spy" "$(dirname "$TOOLS_DIR")"
python3 - "$PY_SPY_WHEEL" "$WORK_DIR/tools/py-spy/py-spy" << 'PY'
import pathlib
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as wheel:
    matches = [name for name in wheel.namelist() if name.endswith("/scripts/py-spy")]
    if len(matches) != 1:
        raise SystemExit("py-spy executable is missing or ambiguous in release wheel")
    pathlib.Path(sys.argv[2]).write_bytes(wheel.read(matches[0]))
PY
chmod 0755 "$WORK_DIR/tools/py-spy/py-spy"
[[ -x "$WORK_DIR/tools/async-profiler/bin/asprof" ]] \
	|| {
		echo "async-profiler archive has no bin/asprof" >&2
		exit 1
	}
[[ -f "$WORK_DIR/tools/async-profiler/lib/libasyncProfiler.so" ]] \
	|| {
		echo "async-profiler archive has no lib/libasyncProfiler.so" >&2
		exit 1
	}

rm -rf "$TOOLS_DIR"
mv "$WORK_DIR/tools" "$TOOLS_DIR"
