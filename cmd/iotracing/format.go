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

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/types"
)

// writer is the single sink for an iotracing run's aggregated result.
type writer interface {
	Write(report *types.IOTracingReport) error
}

type textWriter struct{ w io.Writer }

func (s *textWriter) Write(report *types.IOTracingReport) error {
	printIOTracingReport(s.w, report)
	return nil
}

type jsonWriter struct{ w io.Writer }

func (s *jsonWriter) Write(report *types.IOTracingReport) error {
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}

	b = append(b, '\n')
	_, err = s.w.Write(b)

	return err
}

type socketWriter struct{ client *toolstream.Client }

func (s *socketWriter) Write(report *types.IOTracingReport) error {
	return s.client.Send(report)
}

// newWriter returns the appropriate writer based on flags. client may be nil.
func newWriter(outputFmt string, client *toolstream.Client) writer {
	switch {
	case client != nil:
		return &socketWriter{client: client}
	case outputFmt == "json":
		return &jsonWriter{w: os.Stdout}
	default:
		return &textWriter{w: os.Stdout}
	}
}

// printIOTracingReport renders the report as two tables: a per-process
// summary, then a per-file detail block for each process.
func printIOTracingReport(w io.Writer, report *types.IOTracingReport) {
	fmt.Fprintln(w, "PID      COMMAND              FS_READ FS_WRITE DISK_READ DISK_WRITE FILES")
	fmt.Fprintln(w, "=======  ==================== ======= ======== ========= ========== =====")

	for _, p := range report.Processes {
		comm := p.Comm
		if len(comm) > 20 {
			comm = comm[:17] + "..."
		} else {
			comm = fmt.Sprintf("%-20s", comm)
		}

		fmt.Fprintf(w, "%-7d  %s %7s %8s %9s %10s %5d\n",
			p.Pid,
			comm,
			formatBytes(p.TotalFsReadBps),
			formatBytes(p.TotalFsWriteBps),
			formatBytes(p.TotalDiskReadBps),
			formatBytes(p.TotalDiskWriteBps),
			p.TotalFileCount)
	}
	fmt.Fprintln(w)

	for _, p := range report.Processes {
		fmt.Fprintln(w, "===========================================================================")
		fmt.Fprintf(w, "PID: %-7d TOTAL_IO: R=%s W=%s  FILES: %d\n",
			p.Pid,
			formatBytes(p.TotalFsReadBps),
			formatBytes(p.TotalFsWriteBps),
			p.TotalFileCount)
		fmt.Fprintf(w, "COMMAND: %-20s\n", p.Comm)

		fmt.Fprintln(w, "-----------------------------------")
		fmt.Fprintln(w, "DEVICE  FS_READ FS_WRITE DISK_READ DISK_WRITE   LATENCY(μs)      FILE/INODE")

		for _, f := range p.TotalFiles {
			path := f.Path
			if f.IsDirect {
				if path == "" {
					path = "[direct IO]"
				} else {
					path += " [direct IO]"
				}
			}

			device := fmt.Sprintf("[%d:%d]", f.Major, f.Minor)
			fsRead := fmt.Sprintf("%dB", f.FsReadBps)
			fsWrite := fmt.Sprintf("%dB", f.FsWriteBps)
			diskRead := fmt.Sprintf("%dB", f.DiskReadBps)
			diskWrite := fmt.Sprintf("%dB", f.DiskWriteBps)

			if f.Inode == 0 {
				fmt.Fprintf(w, "%-7s %7s %8s %9s %9s   q2c=%-4d d2c=%-4d %s\n",
					device, fsRead, fsWrite, diskRead, diskWrite,
					f.Q2CUs, f.D2CUs, path)
			} else {
				fmt.Fprintf(w, "%-7s %7s %8s %9s %9s   q2c=%-4d d2c=%-4d %s (%d)\n",
					device, fsRead, fsWrite, diskRead, diskWrite,
					f.Q2CUs, f.D2CUs, path, f.Inode)
			}
		}
		fmt.Fprintln(w)
	}
}

// formatBytes formats a byte count into a short human-readable string.
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
