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

package kmsgutil

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/log"
)

// GetAllCPUsBT gets backtrace from all cpus
func GetAllCPUsBT() (string, error) {
	return GetSysrqMsg("l")
}

// GetBlockedProcessesBT gets backtrace of blocked processes
func GetBlockedProcessesBT() (string, error) {
	return GetSysrqMsg("w")
}

// setNonblock switches fd to non-blocking mode.
//
// /dev/kmsg blocks once the record buffer has been drained, so the read loop in
// GetSysrqMsg relies on EAGAIN to stop. A descriptor that cannot be switched
// would block there instead, so the failure has to reach the caller: discarding
// it returns an empty backtrace together with a nil error, and neither the
// caller nor the operator has anything to investigate.
func setNonblock(fd uintptr) error {
	flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		return fmt.Errorf("read kmsg descriptor flags: %w", err)
	}

	if _, err := unix.FcntlInt(fd, unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		return fmt.Errorf("set kmsg descriptor non-blocking: %w", err)
	}

	return nil
}

// GetSysrqMsg reads sysrq triggered demsg
func GetSysrqMsg(command string) (string, error) {
	const kmsgPath = "/dev/kmsg"
	const sysrqPath = "/proc/sysrq-trigger"

	kmsgFile, err := os.Open(kmsgPath)
	if err != nil {
		return "", err
	}
	defer kmsgFile.Close()

	_, err = kmsgFile.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}

	sysrqFile, err := os.OpenFile(sysrqPath, os.O_WRONLY, 0o200)
	if err != nil {
		return "", err
	}
	defer sysrqFile.Close()

	_, err = sysrqFile.WriteString(command)
	if err != nil {
		return "", err
	}

	fd := kmsgFile.Fd()
	if err := setNonblock(fd); err != nil {
		return "", err
	}

	var buffer strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := syscall.Read(int(fd), buf)
		if n > 0 {
			buffer.Write(buf[:n])
		}
		if err != nil {
			if err == syscall.EAGAIN {
				break
			}
			return "", err
		}
	}

	return formatKmsgs(buffer.String()), nil
}

// format kmsg to human-readable format
func formatKmsgs(kmsgs string) string {
	lines := strings.Split(kmsgs, "\n")
	var formattedMsgs strings.Builder
	for _, line := range lines {
		if line != "" {
			formattedLine, err := formatKmsgEntry(line)
			if err != nil {
				log.Errorf("Error formatting kmsg line: %v", err)
				continue
			}
			formattedMsgs.WriteString(formattedLine)
			formattedMsgs.WriteString("\n")
		}
	}
	return formattedMsgs.String()
}

// convert timestamp to human-readable format
func formatKmsgEntry(entry string) (string, error) {
	parts := strings.SplitN(entry, ";", 2)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid entry format")
	}

	subParts := strings.Split(parts[0], ",")
	if len(subParts) < 3 {
		return "", fmt.Errorf("invalid entry format")
	}

	timestampMicro, err := strconv.ParseInt(subParts[2], 10, 64)
	if err != nil {
		log.Errorf("invalid timestamp")
		return "", err
	}

	bootTime, err := getBootTime()
	if err != nil {
		log.Errorf("failed to get boot time: %v", err)
		return "", err
	}

	entryTime := bootTime.Add(time.Duration(timestampMicro) * time.Microsecond)
	formattedTime := entryTime.Format("2006-01-02 15:04:05")
	formattedEntry := fmt.Sprintf("%s %s", formattedTime, parts[1])

	return formattedEntry, nil
}

// Get the system uptime
func getBootTime() (time.Time, error) {
	info := &syscall.Sysinfo_t{}
	err := syscall.Sysinfo(info)
	if err != nil {
		return time.Time{}, err
	}

	uptime := time.Duration(info.Uptime) * time.Second

	bootTime := time.Now().Add(-uptime)
	return bootTime, nil
}
