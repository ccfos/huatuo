// Copyright 2025, 2026 The HuaTuo Authors
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

//go:build !didi

package pod

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/types"

	mapset "github.com/deckarep/golang-set"
)

// XXX go:generate go run -mod=mod github.com/cilium/ebpf/cmd/bpf2go -target amd64 cgroupCssGather $BPF_DIR/cgroup_css_sync.c -- $BPF_INCLUDE
// use the huatuo bpf framework:
//
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cgroup_css_sync.c -o $BPF_DIR/cgroup_css_sync.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cgroup_css_events.c -o $BPF_DIR/cgroup_css_events.o

func parseContainerCSS(containerID string) (map[string]uint64, error) {
	msg := make(map[string]uint64)
	cssList := cgroupListCssDataByKnode(containerID)
	for _, css := range cssList {
		msg[css.SubSys] = css.CSS
	}

	return msg, nil
}

const (
	cgroupSubsysCount                 = 13
	kubeletContainerIDKnodeNameMaxlen = 85
	kubeletContainerIDKnodeNameMinlen = 64
)

var (
	// used to extract container id from cgroup name
	kubeletContainerIDRegexp  = regexp.MustCompile(`(?:cri-containerd-)?([0-9a-f]{64})(?:\.scope)?`)
	cgroupv1SubSysName        = []string{subsystem.SubsystemCPU, subsystem.SubsystemCPUAcct, subsystem.SubsystemCPUSet, subsystem.SubsystemMemory, subsystem.SubsystemBlkIO}
	cgroupv1NotifyFile        = "cgroup.clone_children"
	cgroupv2NotifyFile        = "memory.current"
	cgroupCssID2SubSysNameMap = map[int]string{}
	cgroupCssMetaDataMap      sync.Map

	// avoid GC
	cgroupCssBpfInternal   *bpf.BPF
	cgroupCssBpfCancelFunc context.CancelFunc
	cgroupCssBpfReader     bpf.PerfEventReader
)

type containerCssMetaData struct {
	CSS         uint64
	SubSys      string
	Cgroup      uint64
	CgroupRoot  int32
	CgroupLevel int32
	ContainerID string
}

type containerCssPerfEvent struct {
	Cgroup      uint64
	OpsType     uint64
	CgroupRoot  int32
	CgroupLevel int32
	CSS         [cgroupSubsysCount]uint64
	KnodeName   [kubeletContainerIDKnodeNameMaxlen + 2]byte
}

func cgroupListCssDataByKnode(containerID string) []*containerCssMetaData {
	res := []*containerCssMetaData{}
	cgroupCssMetaDataMap.Range(func(k, v any) bool {
		if m, ok := v.(*containerCssMetaData); ok {
			if m.ContainerID == containerID {
				res = append(res, m)
			}
		}
		return true
	})
	return res
}

func cgroupUpdateOrCreateCssData(data *containerCssPerfEvent) error {
	knodeName := bytesutil.ToStr(data.KnodeName[:])
	containerID := extractContainerID(knodeName)
	if containerID == "" {
		return fmt.Errorf("knode name is not containterID")
	}

	for index, css := range data.CSS {
		if css == 0 {
			continue
		}

		if sysName, ok := cgroupCssID2SubSysNameMap[index]; ok {
			m := &containerCssMetaData{
				CSS:         css,
				Cgroup:      data.Cgroup,
				CgroupRoot:  data.CgroupRoot,
				CgroupLevel: data.CgroupLevel,
				ContainerID: containerID,
				SubSys:      sysName,
			}
			log.Debugf("update container css data: %+v", m)
			cgroupCssMetaDataMap.Store(css, m)
		}
	}

	return nil
}

func cgroupDeleteCssData(data *containerCssPerfEvent) error {
	knodeName := bytesutil.ToStr(data.KnodeName[:])
	containerID := extractContainerID(knodeName)
	if containerID == "" {
		return fmt.Errorf("knode name is not containterID")
	}

	for index, css := range data.CSS {
		if css == 0 {
			continue
		}

		if _, ok := cgroupCssID2SubSysNameMap[index]; ok {
			m, loaded := cgroupCssMetaDataMap.LoadAndDelete(css)
			if loaded {
				log.Debugf("delete container css data: %+v", m)
			}
		}
	}

	return nil
}

func cgroupCssEventSyncHandler(ctx context.Context, reader bpf.PerfEventReader) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				var data containerCssPerfEvent
				if err := reader.ReadInto(&data); err != nil {
					if !errors.Is(err, types.ErrExitByCancelCtx) {
						log.Errorf("cgroup css sync read events: %v", err)
					}
					return
				}

				log.Debugf("sync container css data: %+v", data)

				switch data.OpsType {
				case 0: // mkdir cgroup, or cgroupv1/v2 read specific file to collect css
					_ = cgroupUpdateOrCreateCssData(&data)
				case 1: // rmdir cgroup
					_ = cgroupDeleteCssData(&data)
				default:
					log.Errorf("css event opstype not supported: %+v", data)
				}
			}
		}
	}()
}

func cgroupRootNotify(realRoot, name string) error {
	if err := filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path != realRoot {
				return nil // ignore error for container destroy, but not for root path
			}
			return err
		}
		// for containerd, the length of cgroup name is 85
		// for docker, it is 64
		if !d.IsDir() || len(d.Name()) < kubeletContainerIDKnodeNameMinlen {
			return nil
		}

		// Match container ID format only; skip pod-level .slice dirs
		// whose names can also be ≥64 chars (e.g. kubepods-burstable-podXXX.slice).
		if !kubeletContainerIDRegexp.MatchString(d.Name()) {
			return nil
		}

		notifyPath := filepath.Join(path, name)
		_, _ = os.ReadFile(notifyPath)

		log.Debugf("read cgroup path: %s", notifyPath)
		return filepath.SkipDir
	}); err != nil {
		var e *os.PathError
		if errors.As(err, &e) && errors.Is(e.Err, syscall.ENOENT) {
			return nil
		}

		return err
	}

	return nil
}

func cgroupCssNotifyFile() {
	switch cgroups.CgroupMode() {
	case cgroups.Legacy, cgroups.Hybrid:
		rootSet := mapset.NewSet()
		for _, subsys := range cgroupv1SubSysName {
			root := cgroups.RootFsFilePath(subsys)
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				continue
			}

			if rootSet.Contains(realRoot) {
				continue
			}

			rootSet.Add(realRoot)

			_ = cgroupRootNotify(realRoot, cgroupv1NotifyFile)
		}
	case cgroups.Unified:
		_ = cgroupRootNotify(cgroups.RootfsDefaultPath(), cgroupv2NotifyFile)
	}
}

func cgroupInitSubSysIDs() error {
	file, err := os.Open("/proc/cgroups")
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)

	// skip frst head
	scanner.Scan()

	ssid := 0
	for scanner.Scan() {
		arr := strings.SplitN(scanner.Text(), "\t", 2)
		cgroupCssID2SubSysNameMap[ssid] = arr[0]
		ssid++
	}

	return nil
}

func cgroupCssInitEventSync() error {
	cssBpf, err := bpf.LoadBpf("cgroup_css_events.o", nil)
	if err != nil {
		return fmt.Errorf("LoadBpf: %w", err)
	}
	cgroupCssBpfInternal = &cssBpf

	childCtx, cancel := context.WithCancel(context.Background())
	cgroupCssBpfCancelFunc = cancel

	reader, err := cssBpf.AttachAndEventPipe(childCtx, "cgroup_perf_events", 8192)
	if err != nil {
		cancel()
		return err
	}
	cgroupCssBpfReader = reader

	cgroupCssEventSyncHandler(childCtx, reader)
	return nil
}

func cgroupCssExistedSync() error {
	cssBpf, err := bpf.LoadBpf("cgroup_css_sync.o", nil)
	if err != nil {
		return fmt.Errorf("LoadBpf: %w", err)
	}
	defer cssBpf.Close()

	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cssBpf.AttachWithOptions([]bpf.AttachOption{
		{
			ProgramName: "bpf_cgroup_subsys_state_prog",
			Symbol:      "cgroup_clone_children_read",
		},
		{
			ProgramName: "bpf_cgroup_subsys_state_prog",
			Symbol:      "memory_current_read",
		},
	}); err != nil {
		return err
	}

	reader, err := cssBpf.EventPipeByName(childCtx, "cgroup_perf_events", 8192)
	if err != nil {
		return err
	}
	defer reader.Close()

	cgroupCssEventSyncHandler(childCtx, reader)
	time.Sleep(100 * time.Millisecond)

	cgroupCssNotifyFile()

	// wait sync
	time.Sleep(1 * time.Second)
	return nil
}

func containerCgroupCssInit() error {
	if err := cgroupInitSubSysIDs(); err != nil {
		return err
	}

	if err := cgroupCssExistedSync(); err != nil {
		return err
	}
	if err := cgroupCssInitEventSync(); err != nil {
		return err
	}

	return nil
}

func extractContainerID(fileName string) string {
	got := kubeletContainerIDRegexp.FindStringSubmatch(fileName)
	if len(got) > 0 {
		return got[1]
	}
	return ""
}

func containerCgroupCssRelease() {
	if cgroupCssBpfCancelFunc != nil {
		cgroupCssBpfCancelFunc()
		cgroupCssBpfCancelFunc = nil
	}
	if cgroupCssBpfReader != nil {
		cgroupCssBpfReader.Close()
		cgroupCssBpfReader = nil
	}
	if cgroupCssBpfInternal != nil {
		(*cgroupCssBpfInternal).Close()
		cgroupCssBpfInternal = nil
	}
}

// GetContainerCSSBySubsys retrieves the cgroup subsystem state (CSS) address for a specific
// container and subsystem. It first checks the local cache, and if not found, triggers
// a one-time BPF-based collection for that specific container.
// This function is compatible with the existing containerCgroupCssInit logic.
func GetContainerCSSBySubsys(containerID, subsysName string) (uint64, error) {
	if containerID == "" {
		return 0, nil
	}

	// Ensure subsystem IDs are initialized
	if len(cgroupCssID2SubSysNameMap) == 0 {
		if err := cgroupInitSubSysIDs(); err != nil {
			return 0, fmt.Errorf("init subsystem IDs: %w", err)
		}
	}

	// Check if CSS data already exists in cache
	cssList := cgroupListCssDataByKnode(containerID)
	for _, css := range cssList {
		if css.SubSys == subsysName {
			return css.CSS, nil
		}
	}

	// CSS not found in cache, trigger one-time collection
	if err := syncContainerCSS(containerID); err != nil {
		return 0, fmt.Errorf("sync container CSS: %w", err)
	}

	// Retry lookup after sync
	cssList = cgroupListCssDataByKnode(containerID)
	for _, css := range cssList {
		if css.SubSys == subsysName {
			return css.CSS, nil
		}
	}

	return 0, fmt.Errorf("container %q CSS for subsystem %q not found", containerID, subsysName)
}

// syncContainerCSS triggers a one-time BPF-based CSS collection for a specific container.
// It finds the container's cgroup path, reads a notification file to trigger the BPF program,
// and waits for the CSS data to be populated.
func syncContainerCSS(containerID string) error {
	// Find container cgroup path
	cgroupPath, err := findContainerCgroupPath(containerID)
	if err != nil {
		return fmt.Errorf("find container cgroup path: %w", err)
	}

	if cgroupPath == "" {
		return fmt.Errorf("container %q cgroup path not found", containerID)
	}

	// Load BPF for one-time sync (similar to cgroupCssExistedSync but targeted)
	if err := triggerContainerCSSSync(cgroupPath); err != nil {
		return fmt.Errorf("trigger CSS sync: %w", err)
	}

	return nil
}

// findContainerCgroupPath resolves the container's kernel cgroup membership on this host.
func findContainerCgroupPath(containerID string) (string, error) {
	paths, err := ContainerCgroupPathsByID(containerID)
	if err != nil {
		return "", err
	}

	switch cgroups.CgroupMode() {
	case cgroups.Legacy, cgroups.Hybrid:
		var resolveErrors []error
		for _, subsys := range cgroupv1SubSysName {
			membershipPath := paths.Controllers[subsys]
			if membershipPath == "" {
				continue
			}

			cgroupPath, err := resolveCgroupFilesystemPath(
				cgroups.RootFsFilePath(subsys),
				membershipPath,
				cgroupv1NotifyFile,
			)
			if err == nil {
				return cgroupPath, nil
			}
			resolveErrors = append(resolveErrors, fmt.Errorf("%s controller: %w", subsys, err))
		}

		if len(resolveErrors) == 0 {
			return "", fmt.Errorf("container %q has no supported cgroup v1 membership", containerID)
		}
		return "", fmt.Errorf(
			"container %q has no accessible cgroup v1 notification file: %w",
			containerID,
			errors.Join(resolveErrors...),
		)
	case cgroups.Unified:
		if paths.Unified == "" {
			return "", fmt.Errorf("container %q has no cgroup v2 membership", containerID)
		}

		return resolveCgroupFilesystemPath(
			cgroups.RootfsDefaultPath(),
			paths.Unified,
			cgroupv2NotifyFile,
		)
	default:
		return "", fmt.Errorf("unsupported cgroup mode %d", cgroups.CgroupMode())
	}
}

func resolveCgroupFilesystemPath(root, membershipPath, notifyFile string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve cgroup root %q: %w", root, err)
	}

	cgroupPath := filepath.Join(realRoot, strings.TrimPrefix(membershipPath, "/"))
	notifyPath := filepath.Join(cgroupPath, notifyFile)
	if _, err := os.Stat(notifyPath); err != nil {
		return "", fmt.Errorf("access cgroup notification file %q: %w", notifyPath, err)
	}

	return cgroupPath, nil
}

// triggerContainerCSSSync loads BPF and triggers CSS collection for a specific cgroup path.
func triggerContainerCSSSync(cgroupPath string) error {
	// Load BPF for CSS collection
	cssBpf, err := bpf.LoadBpf("cgroup_css_sync.o", nil)
	if err != nil {
		return fmt.Errorf("load BPF: %w", err)
	}
	defer cssBpf.Close()

	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Attach BPF programs
	if err := cssBpf.AttachWithOptions([]bpf.AttachOption{
		{
			ProgramName: "bpf_cgroup_subsys_state_prog",
			Symbol:      "cgroup_clone_children_read",
		},
		{
			ProgramName: "bpf_cgroup_subsys_state_prog",
			Symbol:      "memory_current_read",
		},
	}); err != nil {
		return fmt.Errorf("attach BPF: %w", err)
	}

	// Create event reader
	reader, err := cssBpf.EventPipeByName(childCtx, "cgroup_perf_events", 8192)
	if err != nil {
		return fmt.Errorf("create event pipe: %w", err)
	}
	defer reader.Close()

	// Start event handler
	cgroupCssEventSyncHandler(childCtx, reader)

	// Give BPF time to initialize
	time.Sleep(100 * time.Millisecond)

	// Trigger notification by reading the file
	var notifyFile string
	switch cgroups.CgroupMode() {
	case cgroups.Legacy, cgroups.Hybrid:
		notifyFile = cgroupv1NotifyFile
	case cgroups.Unified:
		notifyFile = cgroupv2NotifyFile
	}

	notifyPath := filepath.Join(cgroupPath, notifyFile)
	if _, err := os.ReadFile(notifyPath); err != nil {
		return fmt.Errorf("read cgroup notification file %q: %w", notifyPath, err)
	}

	log.Debugf("triggered CSS sync for cgroup path: %s", cgroupPath)

	// Wait for CSS data to be collected
	time.Sleep(500 * time.Millisecond)

	return nil
}
