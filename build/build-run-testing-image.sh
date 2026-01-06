#!/bin/bash
#
# Author: Tonghao Zhang <tonghao@bamaicloud.com>
#
# Copyright 2025 The HuaTuo Authors
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

WORKSPACE_DIR=$(git rev-parse --show-toplevel)

set -ex

docker build --no-cache --quiet --network host -t huatuo/huatuo-dev:latest \
	-f ${WORKSPACE_DIR}/Dockerfile.devel ${WORKSPACE_DIR}
docker run -it --rm --privileged --network host \
	-v ${WORKSPACE_DIR}:/workspace -w /workspace huatuo/huatuo-dev:latest \
	sh -c "git config --global --add safe.directory /workspace && make gen && make check && make build"
