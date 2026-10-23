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

# Called by the runner inside a per-case mount namespace.
set -euo pipefail

mount --make-rprivate /
runtime_dir="${HUATUO_BAMAI_TEST_TMPDIR}/run"
host_runtime_dir="${HUATUO_BAMAI_TEST_TMPDIR}/host-run"
mkdir -p "${runtime_dir}" "${host_runtime_dir}"
mount --rbind /var/run "${host_runtime_dir}"
# Preserve runtime sockets and other host services without sharing Huatuo locks.
shopt -s dotglob nullglob
for entry in "${host_runtime_dir}/"*; do
	name=${entry##*/}
	case ${name} in
	huatuo-bamai.pid | huatuo-apiserver.pid | huatuo-toolstream.sock) continue ;;
	esac
	ln -s "${entry}" "${runtime_dir}/${name}"
done
mount --bind "${runtime_dir}" /var/run
exec bash "$1"
