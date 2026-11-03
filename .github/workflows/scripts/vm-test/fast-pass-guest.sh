#!/usr/bin/env bash

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

set -euo pipefail
cd /mnt/host
source .github/workflows/scripts/vm-test/run-in-vm.sh
export PATH="/usr/local/go/bin:${HOME}/go/bin:${PATH}"
export PYTHONPATH="/usr/local/lib/huatuo-test-python${PYTHONPATH:+:$PYTHONPATH}"
export GOCACHE=/root/.cache/go-build
export JAVA_PROFILER_DOCKER_IMAGE=eclipse-temurin:17-jdk
export JAVA_PROFILER_CONTAINERD_IMAGE=docker.io/library/eclipse-temurin:17-jdk
export NATIVE_PROFILER_DOCKER_IMAGE=busybox:1.36.1
export NATIVE_PROFILER_CONTAINERD_IMAGE=docker.io/library/busybox:1.36.1
export NATIVE_PROFILER_TEST_PRIVILEGED_CONTAINER=1
git config --global --add safe.directory /mnt/host
mode=${1:?expected build or run}
case "$mode" in
build)
	make build test-build SKIP_PORTABLE_GENERATION=1
	make install-tools
	go build -o _output/tools/pprof-tool ./build/pprof-tool
	# Include source objects for Go fixtures embedding BPF and generated sources
	# for unit tests. Binary artifacts are shared only within this pinned image.
	tar -I 'gzip -1' -cf /tmp/huatuo-fast-build.tar.gz \
		_output/bin _output/bpf _output/conf _output/tools _output/tests \
		_output/.bpf-build-stamp-0 _output/portable-generated.commit \
		_output/portable-generated.tar bpf/*.o internal/bpf/abi/*_types_generated.go
	;;
run)
	tar -xf /tmp/huatuo-fast-build.tar.gz
	rm /tmp/huatuo-fast-build.tar.gz
	tar -xf _output/portable-generated.tar
	[[ $(cat _output/portable-generated.commit) == "$(git rev-parse HEAD)" ]] || {
		echo 'shared build revision mismatch' >&2
		exit 1
	}
	# Checkout mtimes differ between workers; all objects came from this commit.
	touch _output/.bpf-build-stamp-0
	suite=${2:?suite is required}
	mkdir -p /tmp/huatuo-fast-result
	if [[ $suite == unit ]]; then
		make unit SKIP_PORTABLE_GENERATION=1
		cp _output/unit-coverage.* /tmp/huatuo-fast-result/
	else
		bash integration/run.sh --suite "$suite" -j 1 --test-list /tmp/huatuo-fast-tests.txt \
			--output-dir /tmp/huatuo-fast-result/report
	fi
	;;
*)
	echo 'usage: fast-pass-guest.sh build|run [suite]' >&2
	exit 2
	;;
esac
