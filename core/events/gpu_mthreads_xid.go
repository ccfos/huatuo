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

// Mthreads GPU XID error tracer.

package events

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

// EventMsg + XidEventData layout (kernel ABI, native-endian on amd64).
const (
	eventMsgSize = 256 // sizeof(EventMsg) = eventType(4) + length(4) + buffer[248]

	// XidEventData = uuid[16] + MtmlXidRecData(184 bytes). The 168-byte
	// MtmlXidRecData layout is documented below; uuid[16] is opaque
	// for the dedup key.
	//
	// MtmlXidRecData field offsets (relative to start of MtmlXidRecData,
	// i.e. payload[16] of the EventMsg).
	//
	//   off  size  field
	//   0    2     version (uint16 LE)
	//   2    2     rsvd
	//   4    4     xid_id (uint32 LE; high byte = module, low 24 = code)
	//   8    4     scopeSev (uint32 LE; only the low byte is used:
	//                bits 0..2 = scope, bits 3..5 = severity. 0x11
	//                → scope=1, sev=2 matches dmesg "SEV=fatal SCP=gpu"
	//                for the `5 2` XID injection; 0x09 → scope=1, sev=1
	//                matches "loss interrupt" warn-level.)
	//   12   4     pad (compiler-inserted for u64 alignment)
	//   16   8     timestamp (uint64 LE; seconds since epoch)
	//   24   8     SBDF (uint64 LE; PCI segment:bus:device.function
	//                packed as (domain << 32) | (bus << 16) | (device << 8) |
	//                function. See mtgpu_event_report.c:event_report_get_pci_sbdf.)
	//   32   8     pid (uint64 LE; 0 if no owning process)
	//   40   128   additional[128] (NUL-padded C string)
	xidRecVersionOffset    = 0
	xidRecXidIDOffset      = 4
	xidRecScopeSevOffset   = 8
	xidRecTimestampOffset  = 16
	xidRecBDFOffset        = 24 // 8-byte SBDF
	xidRecPIDOffset        = 32
	xidRecAdditionalOffset = 40

	// Event type encodings.
	eventTypeVersionShift = 24
	kCurrentEventVersion  = 1
	kCurrentXidMsgVersion = 1
	xidEventTypeBit       = 0x1 // MTML_EVENT_TYPE_XID_ERROR

	// Severity codes (mtml_internal.h).
	severityNotify uint8 = 0
	severityWarn   uint8 = 1
	severityFatal  uint8 = 2

	// severityDisabled is the sentinel returned by severityThreshold
	// when the tracer is configured off (MthreadsXidLevel is empty
	// or unrecognized).
	severityDisabled uint8 = ^uint8(0)

	// pollInterval is how often the polling loop wakes to drain the
	// MUSA event_report buffers.
	pollInterval = 1 * time.Second

	// readBufferSize is the max bytes read per tick. Each EventMsg is
	// 256 bytes, so this covers up to 64 events queued at once to handle
	// the driver's maximum capacity of 50 XID records.
	readBufferSize = 16 * 1024 // 16 KiB = 64 * 256 bytes

	// seenMapMaxEntries caps the dedup map size. When the map is
	// full, it is rebuilt and the next event will be re-emitted if
	// the driver still has it queued. The bound is a memory-pressure
	// safety net, not a dedup window — there is no time-based
	// eviction. Since XID events are mostly exceptional/error events
	// and occur infrequently (typically less than 20 events), the
	// simple rebuild approach is acceptable. Additionally, we care more
	// about knowing what kinds of exceptions occurred rather than
	// precise counting, so occasional duplicate reporting after
	// map rebuild is acceptable.
	seenMapMaxEntries = 512
)

// MthreadsXidLevel selects the minimum XID severity that the tracer will
// report. Anything below the threshold is silently dropped.
type MthreadsXidLevel string

const (
	mthreadsXidLevelNotify  MthreadsXidLevel = "notify"
	mthreadsXidLevelWarning MthreadsXidLevel = "warning"
	mthreadsXidLevelFatal   MthreadsXidLevel = "fatal"
)

// MthreadsXidTracingData is the per-event XID payload.
type MthreadsXidTracingData struct {
	UUID  string `json:"uuid"`
	XIDID string `json:"xid_id"`
	// "notify" | "warning" | "fatal"
	Severity string `json:"severity"`
	// "process" | "gpu" | "host" | "system"
	Scope      string `json:"scope"`
	BDF        string `json:"bdf"`
	PID        uint64 `json:"pid"`
	Additional string `json:"additional,omitempty"`
}

type musaDevice struct {
	phyIdx uint32
}

func (d musaDevice) eventReportPath() string {
	return procfs.Path("driver", fmt.Sprintf("musa/gpu%02x/event_report", d.phyIdx))
}

type mthreadsXidCollector struct {
	devices []musaDevice

	// seen tracks (uuid,xid,pid,timestamp) tuples already processed so
	// the lseek(0)+read re-snapshot doesn't double-count old events.
	// The driver's event_report always returns the full buffer on every
	// read (no per-fd file position), so dedup is the only way to tell
	// a new event from a stale one.
	seen map[xidKey]struct{}
}

// xidKey is the dedup key for a single MtmlXidRecData record. It is
// sized so that (a) every field on the wire is preserved with no
// truncation, and (b) the runtime can memcmp-compare keys directly
// without hashing. Fields are only read through map lookup (c.seen[k])
// and map insert (c.seen[k] = …), so the Go field names are not
// accessed by name after construction.
type xidKey struct {
	uuid  [16]byte //nolint:unused // see struct doc
	xid   uint32   //nolint:unused // see struct doc; full 32-bit: high byte = module, low 24 = code
	pid   uint64   //nolint:unused // see struct doc
	tsSec uint64   //nolint:unused // see struct doc
}

var mthreadsXidCounter int64

func init() {
	tracing.RegisterEventTracing("mthreads_xid", newMthreadsXid)
	log.Debugf("mthreads_xid: registered event tracer")
}

func newMthreadsXid() (*tracing.EventTracingAttr, error) {
	scanPath := procfs.Path("driver", "musa")
	devices, err := scanMusaDevices(scanPath)
	if err != nil || len(devices) == 0 {
		// No MUSA hardware (or driver half-loaded) is normal on most
		// hosts; stay at Debug so non-MUSA deployments don't spam logs.
		log.Debugf("mthreads_xid: no MUSA devices under %s (err=%v, n=%d), tracer inactive",
			scanPath, err, len(devices))
		return nil, types.ErrNotSupported
	}
	paths := make([]string, 0, len(devices))
	for _, d := range devices {
		paths = append(paths, d.eventReportPath())
	}
	log.Debugf("mthreads_xid: found %d MUSA device(s): %v", len(devices), paths)
	return &tracing.EventTracingAttr{
		TracingData: &mthreadsXidCollector{
			devices: devices,
			seen:    make(map[xidKey]struct{}, 64),
		},
		Interval: 10,
		Flag:     tracing.FlagMetric | tracing.FlagTracing,
	}, nil
}

func (c *mthreadsXidCollector) Update() ([]*metric.Data, error) {
	currentValue := float64(atomic.LoadInt64(&mthreadsXidCounter))
	// Create new metric data instances to avoid race conditions with concurrent scrapes
	newData := []*metric.Data{
		metric.NewCounterData("total", currentValue, "mthreads_xid counter", nil),
	}
	return newData, nil
}

func (c *mthreadsXidCollector) Start(ctx context.Context) error {
	// open()ing O_NONBLOCK, lseek(0)+read()ing the full buffer on every tick,
	// and dedup'ing on (uuid,xid,pid,timestamp) to avoid re-reporting the same event.
	//
	// fds is local to this Start invocation: a single owner (the defer
	// below) controls the lifecycle, and a restart by eventRunner sees
	// a fresh slice rather than an alias of the previous one.
	fds, err := c.openAll()
	if err != nil {
		return err
	}
	defer closeAllFDs(fds)

	registeredAt := time.Now()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	buf := make([]byte, readBufferSize)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		// Re-read the configuration each tick to support runtime updates
		currentCfg := configSnapshot()
		threshold := severityThreshold(currentCfg.MthreadsGPU.MthreadsXidLevel)

		for _, fd := range fds {
			if err := c.drainDevice(fd, registeredAt, threshold, buf); err != nil {
				// Propagate the error so that the eventRunner can restart the collector
				return err
			}
		}
	}
}

// openAll opens one O_NONBLOCK|O_CLOEXEC fd per MUSA device.
func (c *mthreadsXidCollector) openAll() ([]int, error) {
	fds := make([]int, 0, len(c.devices))
	for _, dev := range c.devices {
		fd, err := unix.Open(dev.eventReportPath(), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			closeAllFDs(fds)
			return nil, fmt.Errorf("open %s: %w", dev.eventReportPath(), err)
		}
		fds = append(fds, fd)
	}
	return fds, nil
}

// closeAllFDs is best-effort: syscall.Close on a bad fd returns EBADF,
// which we deliberately swallow to keep shutdown idempotent.
func closeAllFDs(fds []int) {
	for _, fd := range fds {
		_ = syscall.Close(fd)
	}
}

// drainDevice lseek(0)s the device's event_report and parses every 256-byte
// EventMsg in the snapshot. New events pass through the seen-set dedup and
// severity filter.
func (c *mthreadsXidCollector) drainDevice(fd int, registeredAt time.Time, minSeverity uint8, buf []byte) error {
	// Skip seek/read/parse when disabled to avoid unnecessary I/O operations.
	// This balances dynamic config updates with resource conservation.
	if minSeverity == severityDisabled {
		log.Debugf("mthreads_xid: config set event report disabled.")
		return nil
	}

	if _, err := unix.Seek(fd, 0, 0); err != nil {
		// an error here means the fd is in a bad state and
		// the next read will tell us the same story.
		log.Warnf("mthreads_xid: lseek(0) failed on fd=%d: %v", fd, err)
		return err
	}
	n, err := syscall.Read(fd, buf)
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	// Return the error if it's not EAGAIN, so that Start can handle the failure
	if err != nil {
		log.Warnf("mthreads_xid: read on fd=%d returned err=%v n=%d", fd, err, n)
		return err
	}
	// If n <= 0 but err is nil, return an error indicating unexpected condition
	if n <= 0 {
		err := fmt.Errorf("unexpected read result: n=%d, err=nil", n)
		log.Warnf("mthreads_xid: read on fd=%d returned unexpected result: %v", fd, err)
		return err
	}

	for off := 0; off+eventMsgSize <= n; off += eventMsgSize {
		c.parseEvent(buf[off:off+eventMsgSize], registeredAt, minSeverity)
	}
	return nil
}

// parseEvent processes one 256-byte EventMsg.
func (c *mthreadsXidCollector) parseEvent(msg []byte, registeredAt time.Time, minSeverity uint8) {
	if len(msg) < eventMsgSize {
		return
	}
	// Parse EventMsg header (native-endian uint32 on amd64).
	eventType := uint32(msg[0]) | uint32(msg[1])<<8 | uint32(msg[2])<<16 | uint32(msg[3])<<24
	if eventType>>eventTypeVersionShift != kCurrentEventVersion {
		return
	}
	if eventType&xidEventTypeBit == 0 {
		return
	}

	// XidEventData = uuid[16] + MtmlXidRecData(168). Skip the 8-byte
	// EventMsg header (eventType + length) to reach the XidEventData.
	payload := msg[8:]
	if len(payload) < 16+168 {
		return
	}
	uuid := payload[:16]
	rec := payload[16:]

	recVersion := uint16(rec[xidRecVersionOffset]) | uint16(rec[xidRecVersionOffset+1])<<8
	if recVersion != kCurrentXidMsgVersion {
		return
	}

	// Scope/severity bitfield at offset 8: only the low byte is meaningful —
	// bits 0-2 = scope, bits 3-5 = severity.
	severity := (rec[xidRecScopeSevOffset] >> 3) & 0x7
	if severity < minSeverity {
		return
	}
	scope := rec[xidRecScopeSevOffset] & 0x7

	// Filter pre-registration XIDs (mtml §3 (i): data.timeStamp >= timeStamp_/1e3).
	tsSec := uint64(readLEUint32(rec[xidRecTimestampOffset : xidRecTimestampOffset+4]))
	if tsSec < uint64(registeredAt.Unix()) {
		return
	}

	xidID := readLEUint32(rec[xidRecXidIDOffset : xidRecXidIDOffset+4])
	bdfRaw := readLEUint64(rec[xidRecBDFOffset : xidRecBDFOffset+8])
	pid := uint64(readLEUint32(rec[xidRecPIDOffset : xidRecPIDOffset+4]))
	// The driver pads the additional field with leading NUL bytes before
	// the user-supplied text. Trim both leading and trailing NULs so the
	// JSON output is human-readable; embedded NULs in the middle of the
	// string are preserved (Trim strips from both ends, not the interior).
	additional := strings.Trim(string(rec[xidRecAdditionalOffset:xidRecAdditionalOffset+128]), "\x00")

	var uuidArr [16]byte
	copy(uuidArr[:], uuid)
	key := xidKey{uuid: uuidArr, xid: xidID, pid: pid, tsSec: tsSec}
	if _, ok := c.seen[key]; ok {
		return // already reported on a previous tick
	}

	bdf := formatBDF(bdfRaw)
	xidIDStr := fmt.Sprintf("0x%x", xidID)
	severityStr := severityString(severity)
	scopeStr := scopeString(scope)

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:        "mthreads_xid",
		ObservedTimestamp: timeutil.Now(),
		TracerData: &MthreadsXidTracingData{
			UUID:       formatUUID(uuid),
			XIDID:      xidIDStr,
			Scope:      scopeStr,
			Severity:   severityStr,
			BDF:        bdf,
			PID:        pid,
			Additional: additional,
		},
	}); err != nil {
		log.Warnf("failed to save mthreads_xid tracing data: %v", err)
		return
	}

	// Mark as seen only after successful persistence
	c.seen[key] = struct{}{}

	// Bound the dedup map against a flooding driver. Rebuild AFTER
	// recording the current event so its key is not wiped by the
	// rebuild itself.
	if len(c.seen) > seenMapMaxEntries {
		c.seen = make(map[xidKey]struct{}, 64)
	}

	// Increment counter only after successful persistence
	atomic.AddInt64(&mthreadsXidCounter, 1)
}

// scanMusaDevices enumerates GPU entries under root (a /proc/driver/musa-style
// directory).
func scanMusaDevices(root string) ([]musaDevice, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]musaDevice, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "gpu") || len(name) < 4 {
			continue
		}
		hexPart := name[3:]
		if !isAllHex(hexPart) {
			continue
		}
		idx64, err := strconv.ParseUint(hexPart, 16, 32)
		if err != nil {
			continue
		}
		// Confirm by reading devname; skip if the entry is not a real MUSA GPU.
		devnamePath := filepath.Join(root, name, "devname")
		devname, err := os.ReadFile(devnamePath)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(string(devname)), "mtgpu.") {
			continue
		}
		out = append(out, musaDevice{phyIdx: uint32(idx64)})
	}
	return out, nil
}

func isAllHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func severityThreshold(level MthreadsXidLevel) uint8 {
	switch level {
	case mthreadsXidLevelFatal:
		return severityFatal
	case mthreadsXidLevelWarning:
		return severityWarn
	case mthreadsXidLevelNotify:
		return severityNotify
	default:
		// Empty or unrecognized level: disable the tracer.
		return severityDisabled
	}
}

// severityString maps the 3-bit severity field to display string.
func severityString(s uint8) string {
	switch s {
	case severityNotify:
		return "notify"
	case severityWarn:
		return "warning"
	case severityFatal:
		return "fatal"
	default:
		return strconv.Itoa(int(s))
	}
}

// scopeString maps the 3-bit scope field to display string.
func scopeString(s uint8) string {
	switch s {
	case 0:
		return "process"
	case 1:
		return "gpu"
	case 2:
		return "host"
	case 3:
		return "system"
	default:
		return strconv.Itoa(int(s))
	}
}

func readLEUint32(b []byte) uint32 {
	return uint32(b[0]) |
		uint32(b[1])<<8 |
		uint32(b[2])<<16 |
		uint32(b[3])<<24
}

func readLEUint64(b []byte) uint64 {
	return uint64(b[0]) |
		uint64(b[1])<<8 |
		uint64(b[2])<<16 |
		uint64(b[3])<<24 |
		uint64(b[4])<<32 |
		uint64(b[5])<<40 |
		uint64(b[6])<<48 |
		uint64(b[7])<<56
}

// formatUUID renders 16 raw bytes as the libmtml-style ASCII UUID
// "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" (4 dashes at offsets 8, 13, 18, 23).
func formatUUID(b []byte) string {
	hexStr := hex.EncodeToString(b)
	if len(hexStr) != 32 {
		return hexStr
	}
	var sb strings.Builder
	sb.Grow(36)
	sb.WriteString(hexStr[0:8])
	sb.WriteByte('-')
	sb.WriteString(hexStr[8:12])
	sb.WriteByte('-')
	sb.WriteString(hexStr[12:16])
	sb.WriteByte('-')
	sb.WriteString(hexStr[16:20])
	sb.WriteByte('-')
	sb.WriteString(hexStr[20:32])
	return sb.String()
}

// formatBDF renders the mtgpu 8-byte SBDF field as a PCI address
// string in the "DDDD:BB:DD.F" form.
func formatBDF(sbdf uint64) string {
	domain := uint16((sbdf >> 32) & 0xFFFF)
	bus := uint16((sbdf >> 16) & 0xFFFF)
	device := uint8((sbdf >> 8) & 0xFF)
	function := uint8(sbdf & 0xFF)
	return fmt.Sprintf("%04x:%02x:%02x.%x", domain, bus, device, function)
}
