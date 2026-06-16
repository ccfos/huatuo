// Copyright 2025 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package utils

import (
	"context"

	common "huatuo-bamai/internal/profiler/common"
	executil "huatuo-bamai/internal/profiler/exec"
	pyhelper "huatuo-bamai/internal/profiler/helper/python"
)

type CmdResult = executil.CmdResult

func ExecCmd(ctx context.Context, pid int, binPath string, args ...string) CmdResult {
	return executil.ExecCmd(ctx, pid, binPath, args...)
}

func IsProcessInContainer(pid int) (bool, error) {
	return common.IsProcessInContainer(pid)
}

type PythonVersion = pyhelper.PythonVersion

func CheckExecPath(pid int, expectedPath string) error {
	return common.CheckExecPath(pid, expectedPath)
}

func GetPidsFromContainer(bamaiSvr, execPath, langKeyWord, containerID string) ([]int, error) {
	return common.GetPidsFromContainer(bamaiSvr, execPath, langKeyWord, containerID)
}

func EnsureMemrayPython(pid int, hostPythonPath, containerBase, injectorName string) (string, error) {
	return pyhelper.EnsureMemrayPython(pid, hostPythonPath, containerBase, injectorName)
}

func ResolveMemrayBundlePath(hostBundlePath string) (string, error) {
	return pyhelper.ResolveMemrayBundlePath(hostBundlePath)
}

func DetectTargetPythonVersion(pid int) (PythonVersion, bool, error) {
	return pyhelper.DetectTargetPythonVersion(pid)
}

func ResolveMemrayPythonPath(pid int, hostBundlePath string) (string, PythonVersion, bool, error) {
	return pyhelper.ResolveMemrayPythonPath(pid, hostBundlePath)
}

func SelectMemrayInjector(hostPythonPath string, version PythonVersion, versionKnown bool) (string, error) {
	return pyhelper.SelectMemrayInjector(hostPythonPath, version, versionKnown)
}
