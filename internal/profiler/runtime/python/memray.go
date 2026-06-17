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

package python

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/profiler/fileutil"
	"huatuo-bamai/internal/profiler/procutil"
)

const defaultMemrayBundleDir = "/tmp/memray-lite"

var libpythonVersionRe = regexp.MustCompile(`libpython(\d+)\.(\d+)`)

type PythonVersion struct {
	Major int
	Minor int
}

func (v PythonVersion) String() string {
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

func (v PythonVersion) RuntimeKey() string {
	return fmt.Sprintf("py%d.%d", v.Major, v.Minor)
}

// ResolveMemrayBundlePath returns the host-visible memray bundle directory.
// When the caller does not provide --tool-path, profiler falls back to the
// bundle that is laid out next to the built binary under _output/tools.
func ResolveMemrayBundlePath(hostBundlePath string) (string, error) {
	if hostBundlePath != "" {
		return hostBundlePath, nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve profiler executable: %w", err)
	}

	return filepath.Clean(filepath.Join(filepath.Dir(exePath), "..", "tools", "memray-lite")), nil
}

func DetectTargetPythonVersion(pid int) (PythonVersion, bool, error) {
	maps, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return PythonVersion{}, false, err
	}
	match := libpythonVersionRe.FindStringSubmatch(string(maps))
	if len(match) != 3 {
		return PythonVersion{}, false, nil
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return PythonVersion{}, false, err
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		return PythonVersion{}, false, err
	}
	return PythonVersion{Major: major, Minor: minor}, true, nil
}

func listMemrayRuntimePaths(hostBundlePath string) (map[string]string, error) {
	runtimesDir := filepath.Join(hostBundlePath, "runtimes")
	entries, err := os.ReadDir(runtimesDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ret := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runtimeKey := entry.Name()
		hostPythonPath := filepath.Join(runtimesDir, runtimeKey, "python")
		info, err := os.Stat(filepath.Join(hostPythonPath, "memray"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			ret[runtimeKey] = hostPythonPath
		}
	}

	return ret, nil
}

func sortedRuntimeKeys(runtimes map[string]string) []string {
	keys := make([]string, 0, len(runtimes))
	for key := range runtimes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ResolveMemrayPythonPath returns the host-side memray python site-packages
// directory that matches the target process. It supports both the legacy single
// runtime bundle layout and the newer versioned runtime layout.
func ResolveMemrayPythonPath(pid int, hostBundlePath string) (string, PythonVersion, bool, error) {
	runtimes, err := listMemrayRuntimePaths(hostBundlePath)
	if err != nil {
		return "", PythonVersion{}, false, err
	}
	if len(runtimes) != 0 {
		version, ok, err := DetectTargetPythonVersion(pid)
		if err != nil {
			return "", PythonVersion{}, false, err
		}
		if ok {
			if hostPythonPath, found := runtimes[version.RuntimeKey()]; found {
				return hostPythonPath, version, true, nil
			}
			return "", version, true, fmt.Errorf(
				"no memray runtime for python %s under %s (available: %s)",
				version.String(),
				hostBundlePath,
				strings.Join(sortedRuntimeKeys(runtimes), ", "),
			)
		}
		if len(runtimes) == 1 {
			for _, hostPythonPath := range runtimes {
				return hostPythonPath, PythonVersion{}, false, nil
			}
		}
		return "", PythonVersion{}, false, fmt.Errorf(
			"cannot detect target python version for pid %d; bundle has multiple runtimes (%s)",
			pid,
			strings.Join(sortedRuntimeKeys(runtimes), ", "),
		)
	}

	hostPythonPath := filepath.Join(hostBundlePath, "python")
	if _, err := os.Stat(filepath.Join(hostPythonPath, "memray")); err != nil {
		return "", PythonVersion{}, false, fmt.Errorf("memray python directory missing at %s: %w", hostPythonPath, err)
	}

	version, ok, err := DetectTargetPythonVersion(pid)
	if err != nil {
		return "", PythonVersion{}, false, err
	}
	return hostPythonPath, version, ok, nil
}

// EnsureMemrayPython ensures the selected memray python site-packages directory
// is available to the target process. It returns the container-visible python
// path (which may be the host path when the process is not in a container).
func EnsureMemrayPython(pid int, hostPythonPath, containerBase, injectorName string) (string, error) {
	if containerBase == "" {
		containerBase = defaultMemrayBundleDir
	}

	if _, err := os.Stat(filepath.Join(hostPythonPath, "memray")); err != nil {
		return "", fmt.Errorf("memray python directory missing at %s: %w", hostPythonPath, err)
	}

	inContainer, err := procutil.IsProcessInContainer(pid)
	if err != nil {
		return "", err
	}

	if !inContainer {
		// No staging necessary; return host path so attach uses it directly.
		return hostPythonPath, nil
	}

	containerPython := filepath.Join(containerBase, "python")
	hostView := fmt.Sprintf("/proc/%d/root%s", pid, containerPython)

	needsCopy := false
	if _, err := os.Stat(hostView); os.IsNotExist(err) {
		needsCopy = true
	} else if err != nil {
		return "", err
	}

	if !needsCopy {
		targetInjector := filepath.Join(hostView, "memray", injectorName)
		if _, err := os.Stat(targetInjector); os.IsNotExist(err) {
			needsCopy = true
		} else if err != nil {
			return "", err
		} else if !fileContainsSymbol(targetInjector, "memray_schedule_client_direct") {
			needsCopy = true
		}
	}

	if needsCopy {
		if err := fileutil.CopyDir(hostPythonPath, hostView); err != nil {
			return "", fmt.Errorf("copy memray bundle to container: %w", err)
		}
	}

	return containerPython, nil
}

func fileContainsSymbol(path, symbol string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(symbol))
}

// SelectMemrayInjector returns the injector filename that should be used for memray attach.
func SelectMemrayInjector(hostPythonPath string, version PythonVersion, versionKnown bool) (string, error) {
	glob := filepath.Join(hostPythonPath, "memray", "_inject*.so")
	matches, err := filepath.Glob(glob)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no injector shared object found under %s", glob)
	}

	if versionKnown {
		needle := fmt.Sprintf("cpython-%d%d", version.Major, version.Minor)
		for _, candidate := range matches {
			base := filepath.Base(candidate)
			if strings.Contains(base, needle) {
				return base, nil
			}
		}
	}

	var preferred string
	for _, candidate := range matches {
		base := filepath.Base(candidate)
		if strings.Contains(base, "cpython") && !strings.Contains(base, ".abi3.") {
			return base, nil
		}
		if preferred == "" {
			preferred = base
		}
	}
	return preferred, nil
}
