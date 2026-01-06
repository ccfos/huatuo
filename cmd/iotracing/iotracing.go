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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/procfsutil"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o iotracing.o

//go:embed iotracing.o
var iotracing []byte
var tracingCmd ioTracing

// IOStatusData contains IO status information.
type IOStatusData struct {
	ProcessData []ProcFileData `json:"process_io_data"`
	IOStack     []IOStack      `json:"timeout_io_stack"`
}

// IOStack records io_schedule backtrace.
type IOStack struct {
	Pid               uint32       `json:"pid"`
	Comm              string       `json:"comm"`
	ContainerHostname string       `json:"container_hostname"`
	Latency           uint64       `json:"latency_us"`
	Stack             symbol.Stack `json:"stack"`
}

// ProcFileData records process information.
type ProcFileData struct {
	Pid               uint32   `json:"pid"`
	Comm              string   `json:"comm"`
	ContainerHostname string   `json:"container_hostname"`
	FsRead            uint64   `json:"fs_read"`
	FsWrite           uint64   `json:"fs_write"`
	DiskRead          uint64   `json:"disk_read"`
	DiskWrite         uint64   `json:"disk_write"`
	FileStat          []string `json:"file_stat"`
	FileCount         uint64   `json:"file_count"`
}

type ioTracing struct {
	ioData  IOStatusData
	config  ioConfig
	filters map[string]any
}

type ioConfig struct {
	durationSecond     uint64
	scheduleThreshold  uint64 // ms
	maxFilesPerProcess uint64
	maxProcess         uint64
	maxStack           uint64
}

// LatencyInfo contains IO latency information.
type LatencyInfo struct {
	Count  uint64
	MaxD2C uint64
	SumD2C uint64
	MaxQ2C uint64
	SumQ2C uint64
}

// IOData contains BPF data for io_source_map.
type IOData struct {
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

func (data *IOData) FilePathName() string {
	fileName := strings.TrimLeft(fmt.Sprintf("%s/%s/%s/%s",
		bytesutil.ToString(data.Dentry3Name[:]),
		bytesutil.ToString(data.Dentry2Name[:]),
		bytesutil.ToString(data.Dentry1Name[:]),
		bytesutil.ToString(data.FileName[:])),
		"/")

	if data.InodeNum == 0 {
		fileName = "[direct IO]"
	}

	// check 'iocb->ki_flags & IOCB_DIRECT' and '#define IOCB_DIRECT (1 << 2)'
	if data.Flag&0x4 == 0x4 {
		fileName += " [direct IO]"
	}

	return fileName
}

// parseDeviceNumbers
//
// parses device string in format "major:minor", e.g., "8:0,253:0"
// returns device numbers array in the format used by kernel: (major & 0xfff) << 20 | minor
func parseDeviceNumbers(deviceStr string) ([]uint32, error) {
	var deviceNums []uint32

	deviceSpecs := strings.Split(deviceStr, ",")
	for _, spec := range deviceSpecs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		if !strings.Contains(spec, ":") {
			return nil, fmt.Errorf("invalid device format: %s", spec)
		}

		parts := strings.Split(spec, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid device format: %s", spec)
		}

		major, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid number: %s", parts[0])
		}

		minor, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid number: %s", parts[1])
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

// parseProcFileTable parses IO data for a given process ID and file table.
func parseProcFileTable(pid uint32, files *PriorityQueue) ProcFileData {
	var read, write, dread, dwrite uint64
	var fileStat []string
	var comm string

	tableLength := uint64(files.Len())
	for i := uint64(0); i < tableLength; i++ {
		data := heap.Pop(files).(*IODataStat).Data

		wbps := data.FsWriteBytes / tracingCmd.config.durationSecond
		rbps := data.FsReadBytes / tracingCmd.config.durationSecond
		dwbps := data.BlockWriteBytes / tracingCmd.config.durationSecond
		drbps := data.BlockReadBytes / tracingCmd.config.durationSecond

		read += rbps
		write += wbps
		dread += drbps
		dwrite += dwbps

		if i > tracingCmd.config.maxFilesPerProcess {
			continue
		}

		var q2c, d2c uint64
		if data.Latency.Count > 0 {
			q2c = data.Latency.SumQ2C / (data.Latency.Count * 1000) // us
			d2c = data.Latency.SumD2C / (data.Latency.Count * 1000)
		}

		stat := fmt.Sprintf("[%d:%d], fs_read=%db/s, fs_write=%db/s, disk_read=%db/s, disk_write=%db/s, q2c=%dus, d2c=%dus, inode=%d, %s",
			data.Dev>>20&0xfff, data.Dev&0xfffff, rbps, wbps, drbps, dwbps, q2c, d2c, data.InodeNum, data.FilePathName())

		// if data.Tgid == 0, it means we only catch the io from the block layer,so this is no filepath.
		// so we need to show the container info
		//
		// if data.Blkcg != 0 && data.Tgid == 0 {
		// 	if containerID, ok := tracingCmd.cssToContainerID[data.Blkcg]; ok {
		// 		if c, ok := tracingCmd.containers[containerID]; ok {
		// 			filesInfo += fmt.Sprintf(", container=%s", c.Name)
		// 		} else {
		// 			filesInfo += fmt.Sprintf(", containerID=%s", containerID)
		// 		}
		// 	}
		// }
		fileStat = append(fileStat, stat)

		if comm == "" {
			comm = bytesutil.ToString(data.Comm[:])
		}
	}

	cmdline, err := procfsutil.ProcNameByPid(pid)
	if err != nil {
		cmdline = comm
	}

	processData := ProcFileData{
		Comm:      cmdline,
		DiskRead:  dread,
		DiskWrite: dwrite,
		FsRead:    read,
		FsWrite:   write,
		Pid:       pid,
		FileCount: tableLength,
		FileStat:  fileStat,
	}

	processData.ContainerHostname, _ = procfsutil.HostnameByPid(pid)
	return processData
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

func shouldskipFsBpfOption(name string) bool {
	return strings.HasPrefix(name, "bpf_anyfs")
}

func fsBpfOption() []bpf.AttachOption {
	var opts []bpf.AttachOption

	if procfsutil.FsSupported("ext4") {
		opts = append(opts, []bpf.AttachOption{
			{
				ProgramName: "bpf_anyfs_file_read_iter",
				Symbol:      "ext4_file_read_iter",
			},
			{
				ProgramName: "bpf_anyfs_file_write_iter",
				Symbol:      "ext4_file_write_iter",
			},
			{
				ProgramName: "bpf_anyfs_filemap_page_mkwrite",
				Symbol:      "ext4_page_mkwrite",
			},
		}...)
	}

	if procfsutil.FsSupported("xfs") {
		opts = append(opts, []bpf.AttachOption{
			{
				ProgramName: "bpf_anyfs_file_read_iter",
				Symbol:      "xfs_file_read_iter",
			},
			{
				ProgramName: "bpf_anyfs_file_write_iter",
				Symbol:      "xfs_file_write_iter",
			},
			{
				ProgramName: "bpf_anyfs_filemap_page_mkwrite",
				Symbol:      "xfs_filemap_page_mkwrite",
			},
		}...)
	}

	return opts
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

	infos, _ := b.Info()

	/*
		The chosen attachment points are rq_qos_issue and rq_qos_done, which were introduced in the 4.19 kernel
		and became __rq_qos_issue and __rq_qos_done in the 5.0 kernel. The kernel of CentOS 8.0, based on the
		4.18 kernel, already supports __rq_qos_issue and __rq_qos_done, but they may not be invoked unless
		q->rq_qos is non-zero. q->rq_qos is set by default during queue creation through the following sequence:

			blk_register_queue -> wbt_enable_default(q) -> wbt_init(q) -> rq_qos_add(q, &rwb->rqos)

		This also depends on the kernel being configured with CONFIG_BLK_WBT_MQ=y and using block-mq.
		Of course, if other qos strategies are enabled, there is no need to worry about this.
	*/
	var requestQosIssue, requestQosDone string
	if checkKprobeFunctionExists("rq_qos_issue") {
		requestQosIssue = "rq_qos_issue"
		requestQosDone = "rq_qos_done"
	} else {
		requestQosIssue = "__rq_qos_issue"
		requestQosDone = "__rq_qos_done"
	}

	var defaultOption []bpf.AttachOption
	for _, i := range infos.ProgramsInfo {
		if shouldskipFsBpfOption(i.Name) {
			continue
		}

		switch i.Name {
		case "bpf_rq_qos_issue":
			defaultOption = append(defaultOption, bpf.AttachOption{
				ProgramName: i.Name,
				Symbol:      requestQosIssue,
			})
		case "bpf_rq_qos_done":
			defaultOption = append(defaultOption, bpf.AttachOption{
				ProgramName: i.Name,
				Symbol:      requestQosDone,
			})
		default:
			symbol := strings.Split(i.SectionName, "/")
			if len(symbol) != 2 {
				return nil, fmt.Errorf("invalid section name: %s", i.SectionName)
			}

			// Make sure we attach kretprobe of 'io_schedule' first, so we can obtain the stack
			// in kprobe successfully.
			switch symbol[0] {
			case "kretprobe":
				defaultOption = append([]bpf.AttachOption{
					{
						ProgramName: i.Name,
						Symbol:      symbol[1],
					},
				}, defaultOption...)
			default:
				defaultOption = append(defaultOption, bpf.AttachOption{
					ProgramName: i.Name,
					Symbol:      symbol[1],
				})
			}
		}
	}

	defaultOption = append(defaultOption, fsBpfOption()...)
	if err := b.AttachWithOptions(defaultOption); err != nil {
		return nil, fmt.Errorf("attach with options: %w", err)
	}

	ok = true
	return reader, nil
}

// parseCmdConfig loads configuration from command line arguments.
func parseCmdConfig(ctx *cli.Context) error {
	tracingCmd = ioTracing{
		config: ioConfig{
			maxStack:           ctx.Uint64("max-stack"),
			maxProcess:         ctx.Uint64("max-process"),
			maxFilesPerProcess: ctx.Uint64("max-files-per-process"),
			scheduleThreshold:  ctx.Uint64("schedule-threshold"),
			durationSecond:     ctx.Uint64("duration"),
		},
	}

	if tracingCmd.config.durationSecond == 0 {
		return fmt.Errorf("period is zero")
	}

	if tracingCmd.config.scheduleThreshold == 0 {
		return fmt.Errorf("schedule threshold is zero")
	}

	filters := make(map[string]any)

	filters["FILTER_EVENT_TIMEOUT"] = tracingCmd.config.scheduleThreshold * 1000 * 1000

	// if no devices, iotracing will trace all blkdev.
	if deviceStr := ctx.String("device"); deviceStr != "" {
		deviceNums, err := parseDeviceNumbers(deviceStr)
		if err != nil {
			return fmt.Errorf("parse device numbers: %w", err)
		}

		// Prepare device array for BPF (pad with zeros)
		var deviceArray [16]uint32
		copy(deviceArray[:], deviceNums)

		filters["FILTER_DEVS"] = deviceArray
		filters["FILTER_DEV_COUNT"] = uint32(len(deviceNums))
	}

	tracingCmd.filters = filters
	return nil
}

// mainAction is the main entry point for the iotracing command.
func mainAction(ctx *cli.Context) error {
	if err := parseCmdConfig(ctx); err != nil {
		return err
	}

	if err := bpf.InitBpfManager(&bpf.Option{
		KeepaliveTimeout: int(tracingCmd.config.durationSecond),
	}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.CloseBpfManager()

	// load bpf
	b, err := bpf.LoadBpfFromBytes("iotracing.o", iotracing, tracingCmd.filters)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	// set the time to receive kernel perf events
	timeCtx, cancel := context.WithTimeout(ctx.Context, time.Duration(tracingCmd.config.durationSecond)*time.Second)
	defer cancel()

	signalCtx, signalCancel := signal.NotifyContext(timeCtx, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	defer signalCancel()

	reader, err := attachAndEventPipe(signalCtx, b)
	if err != nil {
		return fmt.Errorf("get event pipe: %w", err)
	}
	defer reader.Close()

	var event IODelayData
	stackDepth := uint64(0)
	for {
		if err := reader.ReadInto(&event); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				break
			}
			return fmt.Errorf("read event: %w", err)
		}

		if stackDepth < tracingCmd.config.maxStack {
			hostname, _ := procfsutil.HostnameByPid(event.Pid)

			stack := IOStack{
				Comm:              bytesutil.ToString(event.Comm[:]),
				ContainerHostname: hostname,
				Pid:               event.Pid,
				Latency:           event.Cost / 1000,
				Stack:             symbol.DumpKernelBackTrace(event.Stack[:], symbol.KsymbolStackMinDepth),
			}

			tracingCmd.ioData.IOStack = append(tracingCmd.ioData.IOStack, stack)
			stackDepth++
		}
	}

	if err := b.Detach(); err != nil {
		return err
	}

	iodata, err := b.DumpMapByName("io_source_map")
	if err != nil {
		return err
	}

	sortTable := NewSortTable()
	fileTable := NewFileTable()

	for _, dataRaw := range iodata {
		var data IOData

		buf := bytes.NewReader(dataRaw.Value)
		if err := binary.Read(buf, binary.LittleEndian, &data); err != nil {
			return err
		}

		blkSize := data.BlockWriteBytes + data.BlockReadBytes

		sortTable.Update(data.Pid, blkSize)
		fileTable.Update(data.Pid, &IODataStat{&data, blkSize})
	}

	pids := sortTable.TopKeyN(int(tracingCmd.config.maxProcess))
	for _, pid := range pids {
		if files := fileTable.QueueByKey(pid); files != nil {
			tracingCmd.ioData.ProcessData = append(tracingCmd.ioData.ProcessData, parseProcFileTable(pid, files))
		}
	}

	if ctx.IsSet("json") {
		jsonData, err := json.Marshal(tracingCmd.ioData)
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	} else {
		printIOTracingData(tracingCmd.ioData)
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
		&cli.StringFlag{
			Name:  "device",
			Usage: "Filter by device(s) (format: major:minor, multiple devices separated by comma, e.g., 8:0 or 8:0,253:0)",
		},
		&cli.BoolFlag{
			Name:  "json",
			Usage: "Print in JSON format, if not set, print in text format",
		},
		&cli.IntFlag{
			Name:  "max-stack",
			Value: 10,
			Usage: "Maximum number of stack traces to display",
		},
		&cli.IntFlag{
			Name:  "max-process",
			Value: 10,
			Usage: "Maximum number of top processes to display",
		},
		&cli.IntFlag{
			Name:  "max-files-per-process",
			Value: 5,
			Usage: "Maximum number of top files per process to display",
		},
		&cli.Uint64Flag{
			Name:  "schedule-threshold",
			Value: 100,
			Usage: "IO schedule threshold in milliseconds",
		},
		&cli.Uint64Flag{
			Name:  "duration",
			Value: 8,
			Usage: "Tool duration(s)",
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
