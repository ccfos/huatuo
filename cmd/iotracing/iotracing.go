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

package main

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/procfsutil"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o iotracing.o

//go:embed iotracing.o
var iotracing []byte
var ioStat ioTracing

// IOStatusData contains IO status information.
type IOStatusData struct {
	ProcessData []ProcessData `json:"process_data"`
	IOStack     []IOStack     `json:"io_stack"`
}

// IOStack records io_schedule backtrace.
type IOStack struct {
	Pid               uint32       `json:"pid"`
	Comm              string       `json:"comm"`
	ContainerHostname string       `json:"container_hostname"`
	Latency           uint64       `json:"latency_us"`
	Stack             symbol.Stack `json:"stack"`
}

// ProcessData records process information.
type ProcessData struct {
	Pid               uint32   `json:"pid"`
	Comm              string   `json:"comm"`
	ContainerHostname string   `json:"container_hostname"`
	FsRead            uint64   `json:"fs_read"`
	FsWrite           uint64   `json:"fs_write"`
	DiskRead          uint64   `json:"disk_read"`
	DiskWrite         uint64   `json:"disk_write"`
	FileStat          []string `json:"file_stat"`
	FileCount         uint32   `json:"file_count"`
}

type ioTracing struct {
	ioData           IOStatusData
	config           ioStatConfig
	cssToContainerID map[uint64]string
	containers       map[string]*pod.Container
}

type ioStatConfig struct {
	periodSecond        uint64
	maxStackNumber      int
	ioScheduleThreshold uint64 // ms
	topProcessCount     int
	topFilesPerProcess  int
}

// LatencyInfo contains IO latency information.
type LatencyInfo struct {
	Count  uint64
	MaxD2C uint64
	SumD2C uint64
	MaxQ2C uint64
	SumQ2C uint64
}

// IOBpfData contains BPF data for io_source_map.
type IOBpfData struct {
	Tgid            uint32
	Pid             uint32
	Dev             uint32
	Flag            uint32
	FsWriteBytes    uint64
	FsReadBytes     uint64
	BlockWriteBytes uint64
	BlockReadBytes  uint64
	InodeNum        uint64
	Blkcg           uint64
	Latency         LatencyInfo
	Comm            [16]byte
	FileName        [64]byte
	Dentry1Name     [64]byte
	Dentry2Name     [64]byte
	Dentry3Name     [64]byte
}

// IODelayData contains IO schedule info from iodelay_perf_events.
type IODelayData struct {
	Stack     [symbol.KsymbolStackMinDepth]uint64
	TimeStamp uint64
	Cost      uint64
	StackSize uint32
	Pid       uint32
	Tid       uint32
	CPU       uint32
	Comm      [16]byte
}

type keyValue struct {
	pid    uint32
	ioSize uint64
}

// parseDeviceNumbers parses device string in format "major:minor"
// supports multiple devices separated by comma, e.g., "8:0,253:0"
// returns device numbers array in the format used by kernel: (major & 0xfff) << 20 | minor
func parseDeviceNumbers(deviceStr string) ([]uint32, error) {
	if deviceStr == "" {
		return nil, nil
	}

	// Split multiple devices by comma
	deviceSpecs := strings.Split(deviceStr, ",")
	var deviceNums []uint32

	for _, spec := range deviceSpecs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		// Parse device number
		var parts []string
		if strings.Contains(spec, ":") {
			parts = strings.Split(spec, ":")
		} else {
			return nil, fmt.Errorf("invalid device format: %s, expected major:minor or major,minor", spec)
		}

		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid device format: %s, expected major:minor or major,minor", spec)
		}

		major, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid major number: %s", parts[0])
		}

		minor, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid minor number: %s", parts[1])
		}

		// Convert to kernel device number format
		devNum := (uint32(major)&0xfff)<<20 | uint32(minor)
		deviceNums = append(deviceNums, devNum)
	}

	if len(deviceNums) > 16 {
		return nil, fmt.Errorf("too many devices specified (max 16), got %d", len(deviceNums))
	}

	return deviceNums, nil
}

// getContainerByPid retrieves the container ID for a given process ID.
func getContainerByPid(pid uint32) (string, error) {
	cgroupPath := fmt.Sprintf("/proc/%d/cgroup", pid)
	cgroupContext, err := os.ReadFile(cgroupPath)
	if err != nil {
		return "", fmt.Errorf("read /proc/%d/cgroup: %w", pid, err)
	}
	cgroupSlice := strings.Split(string(cgroupContext), "\n")
	for _, s := range cgroupSlice {
		if strings.Contains(s, "kubepods/burstable") {
			// 11:devices:/kubepods/burstable/pode611e7d6-0e77-11ee-a314-08c0eb65d6a2/7b48bb51fb200e35221bfdd256a96a49b16dbe0be9a1019f3b5e0709d9ddefe2
			// or
			// 11:cpuset:/docker/538594e684780c9adf15ae982a9f973accaae9c0556ab037a3cc85656b1cbac4
			id := strings.Split(s, "/")[4]
			if len(id) != 64 {
				continue
			}
			return id, nil
		} else if strings.Contains(s, "/docker/") {
			id := strings.Split(s, "/")[2]
			if len(id) != 64 {
				continue
			}
			return id, nil
		}
	}
	return "", nil
}

// getCmdline retrieves the command line for a given process ID.
func getCmdline(pid uint32) string {
	cmdlinePath := fmt.Sprintf("/proc/%d/cmdline", pid)
	cmdlineContext, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return ""
	}

	cmdline := strings.ReplaceAll(string(cmdlineContext), "\x00", " ")
	if len(cmdline) > 128 {
		return cmdline[0:127]
	}

	return cmdline
}

// parseIOData parses IO data for a given process ID and file table.
func parseIOData(pid uint32, fileTable *PriorityQueue) {
	var read, write, dread, dwrite uint64
	var filesInfo string
	var comm string
	var ProcessData ProcessData

	tableLength := fileTable.Len()
	for i := 0; i < tableLength; i++ {
		data := heap.Pop(fileTable).(*IODataStat).Data

		wbps := data.FsWriteBytes / ioStat.config.periodSecond
		rbps := data.FsReadBytes / ioStat.config.periodSecond
		dwbps := data.BlockWriteBytes / ioStat.config.periodSecond
		drbps := data.BlockReadBytes / ioStat.config.periodSecond
		read += rbps
		write += wbps
		dread += drbps
		dwrite += dwbps

		ProcessData.FileCount++
		if i > ioStat.config.topFilesPerProcess {
			continue
		}

		dir3 := bytesutil.CString(data.Dentry3Name[:])
		dir2 := bytesutil.CString(data.Dentry2Name[:])
		dir1 := bytesutil.CString(data.Dentry1Name[:])
		filename := bytesutil.CString(data.FileName[:])
		filepath := strings.TrimLeft(fmt.Sprintf("%s/%s/%s/%s", dir3, dir2, dir1, filename), "/")

		var q2c, d2c uint64
		if data.Latency.Count > 0 {
			q2c = data.Latency.SumQ2C / (data.Latency.Count * 1000) // us
			d2c = data.Latency.SumD2C / (data.Latency.Count * 1000)
		}

		if data.InodeNum == 0 {
			filepath = "[direct IO]"
		}
		// check 'iocb->ki_flags & IOCB_DIRECT' and '#define IOCB_DIRECT (1 << 2)'
		if data.Flag&0x4 == 0x4 {
			filepath += " [direct IO]"
		}

		filesInfo = fmt.Sprintf("[%d:%d], fs_read=%db/s, fs_write=%db/s, disk_read=%db/s, disk_write=%db/s, q2c=%dus, d2c=%dus, inode=%d, %s",
			data.Dev>>20&0xfff, data.Dev&0xfffff, rbps, wbps, drbps, dwbps, q2c, d2c, data.InodeNum, filepath)

		// if data.Tgid == 0, it means we only catch the io from the block layer,so this is no filepath.
		// so we need to show the container info
		if data.Blkcg != 0 && data.Tgid == 0 {
			if containerID, ok := ioStat.cssToContainerID[data.Blkcg]; ok {
				if c, ok := ioStat.containers[containerID]; ok {
					filesInfo += fmt.Sprintf(", container=%s", c.Name)
				} else {
					filesInfo += fmt.Sprintf(", containerID=%s", containerID)
				}
			}
		}
		ProcessData.FileStat = append(ProcessData.FileStat, filesInfo)

		if comm == "" {
			comm = bytesutil.CString(data.Comm[:])
		}
	}

	if len(ProcessData.FileStat) == 0 {
		return
	}

	cmdline := getCmdline(pid)
	if cmdline == "" {
		cmdline = comm
	}
	ProcessData.Comm = cmdline
	ProcessData.DiskRead = dread
	ProcessData.DiskWrite = dwrite
	ProcessData.FsRead = read
	ProcessData.FsWrite = write
	ProcessData.Pid = pid

	containerID, _ := getContainerByPid(pid)
	if containerID != "" {
		if c, ok := ioStat.containers[containerID]; ok {
			ProcessData.ContainerHostname = c.Hostname
		} else {
			ProcessData.ContainerHostname = containerID
		}
	}
	ioStat.ioData.ProcessData = append(ioStat.ioData.ProcessData, ProcessData)
}

// sortProcessIOSize sorts processes by their IO size in descending order.
func sortProcessIOSize(processIOSize map[uint32]uint64) []keyValue {
	var kvPairs []keyValue

	for k, v := range processIOSize {
		kvPairs = append(kvPairs, keyValue{k, v})
	}

	sort.Slice(kvPairs, func(i, j int) bool {
		return kvPairs[i].ioSize > kvPairs[j].ioSize
	})

	return kvPairs
}

// getContainerInfo retrieves container information from the server.
func getContainerInfo(serverAddr string) {
	containers, err := container.GetAllContainers(serverAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetAllContainers: %v\n", err)
		return
	}

	for _, container := range containers {
		ioStat.containers[container.ID] = container
		if css, ok := container.CSS["blkio"]; ok {
			ioStat.cssToContainerID[css] = container.ID
		}
	}
}

// checkKprobeFunctionExists checks if a kprobe function exists in the kernel.
func checkKprobeFunctionExists(functionName string) bool {
	file, err := os.Open("/sys/kernel/debug/tracing/available_filter_functions")
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		name := strings.Fields(line)[0]
		if name == functionName {
			return true
		}
	}
	return false
}

// shouldSkipFilesystemProgram determines if a filesystem program should be skipped.
func shouldSkipFilesystemProgram(programName string, supportExt4, supportXFS bool) bool {
	if !supportExt4 {
		switch programName {
		case "bpf_ext4_file_read_iter", "bpf_ext4_file_write_iter", "bpf_ext4_page_mkwrite":
			return true
		}
	}
	if !supportXFS {
		switch programName {
		case "bpf_xfs_file_read_iter", "bpf_xfs_file_write_iter", "bpf_xfs_filemap_page_mkwrite":
			return true
		}
	}
	return false
}

// attachAndEventPipe attaches BPF programs and creates an event pipe.
func attachAndEventPipe(ctx context.Context, b bpf.BPF) (bpf.PerfEventReader, error) {
	reader, err := b.EventPipeByName(ctx, "iodelay_perf_events", 8192)
	if err != nil {
		return nil, fmt.Errorf("get event pipe: %w", err)
	}
	var ok bool
	defer func() {
		if !ok {
			reader.Close()
		}
	}()

	var ap1 []bpf.AttachOption
	var ap2 []bpf.AttachOption

	var supportExt4, supportXFS bool
	if supportExt4, err = procfsutil.CheckFilesystemSupport("ext4"); err != nil {
		return nil, err
	}
	if supportXFS, err = procfsutil.CheckFilesystemSupport("xfs"); err != nil {
		return nil, err
	}

	infos, err := b.Info()
	if err != nil {
		return nil, err
	}

	/*
		The chosen attachment points are rq_qos_issue and rq_qos_done, which were introduced in the 4.19 kernel
		and became __rq_qos_issue and __rq_qos_done in the 5.0 kernel. The kernel of CentOS 8.0, based on the
		4.18 kernel, already supports __rq_qos_issue and __rq_qos_done, but they may not be invoked unless
		q->rq_qos is non-zero. q->rq_qos is set by default during queue creation through the following sequence:

			blk_register_queue -> wbt_enable_default(q) -> wbt_init(q) -> rq_qos_add(q, &rwb->rqos)

		This also depends on the kernel being configured with CONFIG_BLK_WBT_MQ=y and using block-mq.
		Of course, if other qos strategies are enabled, there is no need to worry about this.
	*/
	var ioStartSymbol, ioDoneSymbol string
	if checkKprobeFunctionExists("rq_qos_issue") {
		ioStartSymbol = "rq_qos_issue"
		ioDoneSymbol = "rq_qos_done"
	} else {
		ioStartSymbol = "__rq_qos_issue"
		ioDoneSymbol = "__rq_qos_done"
	}

	for _, i := range infos.ProgramsInfo {
		if shouldSkipFilesystemProgram(i.Name, supportExt4, supportXFS) {
			continue
		}

		switch i.Name {
		case "bpf_io_schedule":
			ap2 = append(ap2, bpf.AttachOption{ProgramName: i.Name, Symbol: "io_schedule"})
		case "bpf_io_schedule_timeout":
			ap2 = append(ap2, bpf.AttachOption{ProgramName: i.Name, Symbol: "io_schedule_timeout"})
		case "bpf_rq_qos_issue":
			ap1 = append(ap1, bpf.AttachOption{ProgramName: i.Name, Symbol: ioStartSymbol})
		case "bpf_rq_qos_done":
			ap1 = append(ap1, bpf.AttachOption{ProgramName: i.Name, Symbol: ioDoneSymbol})
		default:
			symbol := strings.Split(i.SectionName, "/")
			if len(symbol) != 2 {
				return nil, fmt.Errorf("invalid section name: %s", i.SectionName)
			}
			ap1 = append(ap1, bpf.AttachOption{ProgramName: i.Name, Symbol: symbol[1]})
		}
	}

	// Make sure we attach kretprobe of 'io_schedule' first, so we can obtain the stack
	// in kprobe successfully.
	ap1 = append(ap1, ap2...)
	if err := b.AttachWithOptions(ap1); err != nil {
		return nil, fmt.Errorf("attach with options: %w", err)
	}
	ok = true
	return reader, nil
}

// loadConfig loads configuration from command line arguments.
func loadConfig(ctx *cli.Context) error {
	ioStat.config.maxStackNumber = ctx.Int("max-stack-number")
	ioStat.config.topProcessCount = ctx.Int("top-process-count")
	ioStat.config.topFilesPerProcess = ctx.Int("top-files-per-process")
	ioStat.config.ioScheduleThreshold = uint64(ctx.Int("io-schedule-threshold"))
	ioStat.config.periodSecond = uint64(ctx.Int("dur"))
	if ioStat.config.periodSecond <= 0 {
		return fmt.Errorf("invalid period: %d", ioStat.config.periodSecond)
	}

	ioStat.ioData = IOStatusData{}
	ioStat.cssToContainerID = make(map[uint64]string)
	ioStat.containers = make(map[string]*pod.Container)
	return nil
}

// mainAction is the main entry point for the iotracing command.
func mainAction(ctx *cli.Context) error {
	if err := loadConfig(ctx); err != nil {
		return err
	}

	// Parse device filter if provided
	var consts map[string]any
	if deviceStr := ctx.String("device"); deviceStr != "" {
		deviceNums, err := parseDeviceNumbers(deviceStr)
		if err != nil {
			return fmt.Errorf("parse device numbers: %w", err)
		}

		// Prepare device array for BPF (pad with zeros)
		var deviceArray [16]uint32
		copy(deviceArray[:], deviceNums)

		consts = map[string]any{
			"FILTER_DEVS":      deviceArray,
			"FILTER_DEV_COUNT": uint32(len(deviceNums)),
		}
	}

	// init bpf
	if err := bpf.InitBpfManager(&bpf.Option{
		KeepaliveTimeout: int(ioStat.config.periodSecond),
	}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.CloseBpfManager()

	// load bpf
	b, err := bpf.LoadBpfFromBytes("iotracing.o", iotracing, consts)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	// set the time to receive kernel perf events
	timeCtx, cancel := context.WithTimeout(ctx.Context, time.Duration(ioStat.config.periodSecond)*time.Second)
	defer cancel()

	signalCtx, signalCancel := signal.NotifyContext(timeCtx, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	defer signalCancel()

	reader, err := attachAndEventPipe(signalCtx, b)
	if err != nil {
		return fmt.Errorf("get event pipe: %w", err)
	}
	defer reader.Close()

	getContainerInfo(ctx.String("server-address"))

	var event IODelayData
	var stackCollected int
	for {
		if err := reader.ReadInto(&event); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				break
			}
			return fmt.Errorf("read event: %w", err)
		}

		// event.Cost(ns), ioStat.config.ioScheduleThreshold(ms)
		// event.Cost/1000(us), 1000*ioStat.config.ioScheduleThreshold(us)
		if event.Cost/1000 > 1000*ioStat.config.ioScheduleThreshold {
			if stackCollected < ioStat.config.maxStackNumber {
				var containerHostname string
				if containerID, err := getContainerByPid(event.Pid); err == nil && containerID != "" {
					if container, ok := ioStat.containers[containerID]; ok {
						containerHostname = container.Hostname
					} else {
						containerHostname = containerID // if can't get the container name, we can still show the container ID.
					}
				}
				var stackInfo IOStack
				stackInfo.Comm = bytesutil.CString(event.Comm[:])
				stackInfo.ContainerHostname = containerHostname
				stackInfo.Pid = event.Pid
				stackInfo.Latency = event.Cost / 1000
				stackInfo.Stack = symbol.DumpKernelBackTrace(event.Stack[:], symbol.KsymbolStackMinDepth)
				ioStat.ioData.IOStack = append(ioStat.ioData.IOStack, stackInfo)
				stackCollected++
			}
		}
	}

	if err := b.Detach(); err != nil {
		return err
	}

	data, err := b.DumpMapByName("io_source_map")
	if err != nil {
		return err
	}

	processFileTable := make(map[uint32]*PriorityQueue)
	processIOSize := make(map[uint32]uint64)
	for _, ioData := range data {
		var ioDataStat IOBpfData
		buf := bytes.NewReader(ioData.Value)
		err = binary.Read(buf, binary.LittleEndian, &ioDataStat)
		if err != nil {
			fmt.Printf("iotracer error: %v\n", err)
			return err
		}
		devSize := ioDataStat.BlockWriteBytes + ioDataStat.BlockReadBytes
		if v, ok := processIOSize[ioDataStat.Pid]; !ok {
			processIOSize[ioDataStat.Pid] = devSize
			pq := make(PriorityQueue, 0)
			processFileTable[ioDataStat.Pid] = &pq
		} else {
			processIOSize[ioDataStat.Pid] = v + devSize
		}

		item := &IODataStat{&ioDataStat, devSize}
		pq := processFileTable[ioDataStat.Pid]
		heap.Push(pq, item)
	}

	// Sort by io amount per process, we only get the first few process data
	kvPairs := sortProcessIOSize(processIOSize)
	for i, kv := range kvPairs {
		if fileTable, ok := processFileTable[kv.pid]; ok {
			parseIOData(kv.pid, fileTable)
		}
		// Gets the top processes with the highest number of io requests
		if i > ioStat.config.topProcessCount {
			break
		}
	}

	if ctx.IsSet("json") {
		jsonData, err := json.Marshal(ioStat.ioData)
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	} else {
		printIOTracingData(ioStat.ioData)
	}

	return nil
}

// printIOTracingData prints IO tracing data in a formatted table.
func printIOTracingData(ioData IOStatusData) {
	fmt.Println("PID      COMMAND              FS_READ FS_WRITE DISK_READ DISK_WRITE FILES")
	fmt.Println("=======  ==================== ======= ======== ========= ========== =====")

	for _, p := range ioData.ProcessData {
		comm := p.Comm
		if len(comm) > 20 {
			comm = comm[:17] + "..."
		} else {
			comm = fmt.Sprintf("%-20s", comm)
		}

		fmt.Printf("%-7d  %s %7s %8s %9s %10s %5d\n",
			p.Pid,
			comm,
			formatBytes(p.FsRead),
			formatBytes(p.FsWrite),
			formatBytes(p.DiskRead),
			formatBytes(p.DiskWrite),
			p.FileCount)
	}
	fmt.Println()

	for _, p := range ioData.ProcessData {
		fmt.Println("===========================================================================")
		fmt.Printf("PID: %-7d TOTAL_IO: R=%s W=%s  FILES: %d\n",
			p.Pid,
			formatBytes(p.FsRead),
			formatBytes(p.FsWrite),
			p.FileCount)
		fmt.Printf("COMMAND: %-20s\n", p.Comm)

		fmt.Println("-----------------------------------")
		fmt.Println("DEVICE  FS_READ FS_WRITE DISK_READ DISK_WRITE   LATENCY(μs)      FILE/INODE")

		for _, fileStat := range p.FileStat {
			devicePart := ""
			if strings.HasPrefix(fileStat, "[") {
				deviceEndIndex := strings.Index(fileStat, "],")
				if deviceEndIndex > 0 {
					devicePart = fileStat[:deviceEndIndex+1]
					fileStat = fileStat[deviceEndIndex+2:]
				}
			}

			var fsRead, fsWrite, diskRead, diskWrite, q2c, d2c, inode, filepath string
			parts := strings.Split(fileStat, ", ")

			for _, part := range parts {
				kv := strings.SplitN(part, "=", 2)
				if len(kv) != 2 {
					continue
				}

				key, value := kv[0], kv[1]
				switch key {
				case " fs_read": // Note the space
					fsRead = value[:len(value)-3] // Remove "b/s"
				case "fs_write":
					fsWrite = value[:len(value)-3]
				case "disk_read":
					diskRead = value[:len(value)-3]
				case "disk_write":
					diskWrite = value[:len(value)-3]
				case "q2c":
					q2c = value[:len(value)-2] // Remove "us"
				case "d2c":
					d2c = value[:len(value)-2]
				case "inode":
					parts := strings.SplitN(value, ", ", 2)
					if len(parts) == 2 {
						inode = parts[0]
						filepath = parts[1]
					}
				}
			}

			// If the filepath cannot be extracted from inode, search separately
			if filepath == "" {
				for _, part := range parts {
					if !strings.Contains(part, "=") && part != "" {
						filepath = part
						break
					}
				}
			}

			if inode == "" {
				fmt.Printf("%-7s %7s %8s %9s %9s   q2c=%-4s d2c=%-4s %s\n",
					devicePart,
					fsRead+"B",
					fsWrite+"B",
					diskRead+"B",
					diskWrite+"B",
					q2c,
					d2c,
					filepath)
			} else {
				fmt.Printf("%-7s %7s %8s %9s %9s   q2c=%-4s d2c=%-4s %s (%s)\n",
					devicePart,
					fsRead+"B",
					fsWrite+"B",
					diskRead+"B",
					diskWrite+"B",
					q2c,
					d2c,
					filepath,
					inode)
			}
		}
		fmt.Println()
	}
}

// formatBytes formats byte values into human-readable format.
func formatBytes(nbytes uint64) string {
	if nbytes == 0 {
		return "0B"
	}

	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	value := float64(nbytes)

	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}

	if value < 10 && i > 0 {
		return fmt.Sprintf("%.1f%s", value, units[i])
	}
	return fmt.Sprintf("%.0f%s", value, units[i])
}

func main() {
	app := cli.NewApp()
	app.Action = mainAction
	app.Flags = []cli.Flag{
		&cli.IntFlag{
			Name:  "dur",
			Value: 8,
			Usage: "Tool duration(s), default is 8s",
		},
		&cli.StringFlag{
			Name:  "server-address",
			Value: "127.0.0.1:19704",
			Usage: "huatuo-bamai server address",
		},
		&cli.StringFlag{
			Name:  "device",
			Usage: "Filter by device(s) (format: major:minor, multiple devices separated by comma, e.g., 8:0 or 8:0,253:0)",
		},
		&cli.BoolFlag{
			Name:  "json",
			Usage: "Print in JSON format, if not set, print in text format",
		},
		&cli.IntFlag{
			Name:  "max-stack-number",
			Value: 10,
			Usage: "Maximum number of stack traces to display, default is 10",
		},
		&cli.IntFlag{
			Name:  "top-process-count",
			Value: 10,
			Usage: "Maximum number of top processes to display, default is 10",
		},
		&cli.IntFlag{
			Name:  "top-files-per-process",
			Value: 5,
			Usage: "Maximum number of top files per process to display, default is 5",
		},
		&cli.IntFlag{
			Name:  "period-second",
			Usage: "Period in seconds for collecting IO data, default is 10s",
		},
		&cli.IntFlag{
			Name:  "io-schedule-threshold",
			Value: 100,
			Usage: "IO schedule threshold in milliseconds, default is 100ms",
		},
	}
	app.Before = func(ctx *cli.Context) error {
		// disable log
		log.SetOutput(io.Discard)
		return nil
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Printf("iotracer error %v\n", err)
		os.Exit(1)
	}
}
