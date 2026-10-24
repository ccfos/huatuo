// Copyright 2026 The HuaTuo Authors
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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const defaultMemrayBundleDir = "/tmp/memray"

var (
	libpythonVersionRe        = regexp.MustCompile(`libpython(\d+)\.(\d+)`)
	pythonExecutableVersionRe = regexp.MustCompile(`^python(\d+)\.(\d+)(?:[a-z].*)?$`)
)

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

	return filepath.Clean(filepath.Join(filepath.Dir(exePath), "..", "tools", "memray")), nil
}

func pythonVersionFromMatch(match []string) (PythonVersion, bool, error) {
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

func DetectTargetPythonVersion(pid int) (PythonVersion, bool, error) {
	maps, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return PythonVersion{}, false, err
	}
	if match := libpythonVersionRe.FindStringSubmatch(string(maps)); len(match) == 3 {
		return pythonVersionFromMatch(match)
	}

	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return PythonVersion{}, false, nil
	}
	return pythonVersionFromMatch(
		pythonExecutableVersionRe.FindStringSubmatch(filepath.Base(exe)),
	)
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

func memrayRuntimeKey(hostPythonPath string, version PythonVersion, versionKnown bool) string {
	if versionKnown {
		return version.RuntimeKey()
	}

	cleanPath := filepath.Clean(hostPythonPath)
	runtimeDir := filepath.Dir(cleanPath)
	if filepath.Base(cleanPath) == "python" &&
		filepath.Base(filepath.Dir(runtimeDir)) == "runtimes" {
		return filepath.Base(runtimeDir)
	}
	return "legacy"
}

func memrayRuntimeContentID(hostPythonPath string) (string, error) {
	digest := sha256.New()
	err := filepath.Walk(hostPythonPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file in memray runtime: %s", path)
		}

		rel, err := filepath.Rel(hostPythonPath, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(digest, rel)
		_, _ = digest.Write([]byte{0})

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		_, _ = digest.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func stagedMemrayRuntimeValid(root *os.Root, pythonPath, injectorName string) bool {
	file, err := root.OpenFile(filepath.Join(pythonPath, "memray", injectorName), os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := io.ReadAll(file)
	if err != nil || !bytes.Contains(data, []byte("memray_schedule_client_direct")) {
		return false
	}
	extensions, err := fs.Glob(root.FS(), filepath.Join(pythonPath, "memray", "_memray*.so"))
	return err == nil && len(extensions) != 0
}

// os.Root confines every lookup, including symlinks, to the opened directory.
// Only Go 1.24 methods are used; Rename and MkdirAll require newer Go versions.
func mkdirMemrayPath(root *os.Root, path string) error {
	dir := ""
	for _, component := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		dir = filepath.Join(dir, component)
		if err := root.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func copyMemrayRuntime(src string, destination *os.Root) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join("python", relPath)
		if info.IsDir() {
			return destination.Mkdir(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file in memray runtime: %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := destination.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		modeErr := output.Chmod(info.Mode().Perm())
		closeErr := output.Close()
		return errors.Join(copyErr, modeErr, closeErr)
	})
}

func stageMemrayRuntime(hostPythonPath string, targetRoot *os.Root, targetPython, injectorName string) error {
	if stagedMemrayRuntimeValid(targetRoot, targetPython, injectorName) {
		return nil
	}
	if _, err := targetRoot.Stat(targetPython); err == nil {
		return fmt.Errorf("staged memray runtime is incomplete: %s", targetPython)
	} else if !os.IsNotExist(err) {
		return err
	}

	parentPath := filepath.Dir(targetPython)
	if err := mkdirMemrayPath(targetRoot, parentPath); err != nil {
		return err
	}
	parent, err := targetRoot.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	stagingName := ".memray-stage-" + rand.Text()
	if err := parent.Mkdir(stagingName, 0o700); err != nil {
		return err
	}
	staging, err := parent.OpenRoot(stagingName)
	if err != nil {
		return err
	}
	defer staging.Close()
	defer func() {
		// Remove only this private staging tree, using the same pinned root.
		var paths []string
		_ = fs.WalkDir(staging.FS(), ".", func(path string, _ fs.DirEntry, err error) error {
			if err == nil && path != "." {
				paths = append(paths, path)
			}
			return err
		})
		for i := len(paths) - 1; i >= 0; i-- {
			_ = staging.Remove(paths[i])
		}
		_ = parent.Remove(stagingName)
	}()
	if err := copyMemrayRuntime(hostPythonPath, staging); err != nil {
		return err
	}
	if !stagedMemrayRuntimeValid(staging, "python", injectorName) {
		return fmt.Errorf("copied memray runtime is incomplete")
	}

	// Publish without re-resolving a target-controlled absolute pathname or
	// replacing another profiler's completed runtime.
	parentDir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer parentDir.Close()
	stagingDir, err := staging.Open(".")
	if err != nil {
		return err
	}
	defer stagingDir.Close()
	if err := unix.Renameat(int(stagingDir.Fd()), "python", int(parentDir.Fd()),
		filepath.Base(targetPython)); err != nil {
		if !stagedMemrayRuntimeValid(targetRoot, targetPython, injectorName) {
			return err
		}
	}
	if !stagedMemrayRuntimeValid(targetRoot, targetPython, injectorName) {
		return fmt.Errorf("staged Memray runtime changed while copying: %s", targetPython)
	}
	// A directory could have been renamed while copying. Do not report a
	// target-visible path unless it still reaches the pinned parent.
	actualParent, err := targetRoot.Stat(parentPath)
	if err != nil {
		return err
	}
	pinnedParent, err := parentDir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(actualParent, pinnedParent) {
		return fmt.Errorf("Memray staging directory changed while copying: %s", parentPath)
	}
	return nil
}

func targetHasDifferentMountNamespace(pid int) (bool, error) {
	agentNamespace, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false, fmt.Errorf("read agent mount namespace: %w", err)
	}
	targetNamespace, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return false, fmt.Errorf("read PID %d mount namespace: %w", pid, err)
	}
	return agentNamespace != targetNamespace, nil
}

// EnsureMemrayPython ensures the selected memray python site-packages directory
// is available to the target process. It returns the target-visible python
// path, which may be the original path when the target shares the agent's
// mount namespace.
func EnsureMemrayPython(
	pid int,
	hostPythonPath string,
	containerBase string,
	injectorName string,
	version PythonVersion,
	versionKnown bool,
) (string, error) {
	if containerBase == "" {
		containerBase = defaultMemrayBundleDir
	}
	if !filepath.IsAbs(containerBase) {
		return "", fmt.Errorf("memray container staging path must be absolute: %s", containerBase)
	}
	if _, err := os.Stat(filepath.Join(hostPythonPath, "memray")); err != nil {
		return "", fmt.Errorf("memray python directory missing at %s: %w", hostPythonPath, err)
	}

	differentMountNamespace, err := targetHasDifferentMountNamespace(pid)
	if err != nil {
		return "", err
	}
	if !differentMountNamespace {
		return hostPythonPath, nil
	}

	contentID, err := memrayRuntimeContentID(hostPythonPath)
	if err != nil {
		return "", fmt.Errorf("fingerprint memray runtime: %w", err)
	}
	containerPython := filepath.Join(
		containerBase,
		memrayRuntimeKey(hostPythonPath, version, versionKnown),
		contentID,
		"python",
	)
	targetRoot, err := os.OpenRoot(fmt.Sprintf("/proc/%d/root", pid))
	if err != nil {
		return "", err
	}
	defer targetRoot.Close()
	if err := stageMemrayRuntime(hostPythonPath, targetRoot,
		strings.TrimPrefix(containerPython, "/"), injectorName); err != nil {
		return "", fmt.Errorf("copy memray runtime to container: %w", err)
	}
	return containerPython, nil
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
