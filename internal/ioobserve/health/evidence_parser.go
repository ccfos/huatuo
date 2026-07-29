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

package health

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/pkg/types"
)

// Parsed health fields:
//
// NVMe:
//   - critical_warning: controller health-warning bitmap.
//   - media_errors: cumulative unrecovered media or data-integrity errors.
//   - error_count: distinguishes populated error-log entries from empty slots.
//   - status_code_type/status_code, sct/sc, or status_field: error class and code
//     across supported nvme-cli JSON layouts.
//   - sqid: filters common admin-command noise and is not retained as evidence.
//   - nsid: namespace containing the failed command.
//   - lba: logical block address associated with the error.
//
// SCSI:
//   - smartctl.exit_status: distinguishes command failures from health findings.
//   - smart_status.passed: overall SMART health result.
//   - smart_status.scsi.asc/ascq: device-reported information exception.
//   - temperature.current/drive_trip: current and trip temperatures.
//   - scsi_grown_defect_list: defects added after the device entered service.
//   - scsi_error_counter_log.read/write/verify: error groups by operation.
//   - gigabytes_processed: processed capacity used as error-count context.
//   - errors_corrected_by_eccdelayed: errors corrected by delayed ECC.
//   - errors_corrected_by_rereads_rewrites: errors corrected by retry or rewrite.
//   - total_uncorrected_errors: errors that the device could not recover.
//   - scsi_pending_defects.count: total pending defects.
//   - scsi_pending_defects.table[].lba: representative affected block addresses.

const maxEvidenceEntries = 8

func parseNVMeSMART(data []byte) (types.NVMeHealthEvidence, bool, bool) {
	root, err := decodeObject(data)
	if err != nil {
		return types.NVMeHealthEvidence{}, false, false
	}

	evidence := types.NVMeHealthEvidence{}
	complete := true
	if raw, ok := root["critical_warning"]; ok {
		value, valid := parseUint(raw, 8)
		if valid {
			criticalWarning := uint8(value)
			evidence.CriticalWarning = &criticalWarning
		} else {
			complete = false
		}
	}
	if raw, ok := root["media_errors"]; ok {
		value, valid := parseUint(raw, 64)
		if valid {
			evidence.MediaErrorsTotal = &value
		} else {
			complete = false
		}
	}

	valid := evidence.CriticalWarning != nil || evidence.MediaErrorsTotal != nil
	return evidence, valid, complete
}

func parseNVMeErrorLog(data []byte) ([]types.NVMeErrorLogEntry, bool, bool) {
	var rawEntries []json.RawMessage
	if err := decodeJSON(data, &rawEntries); err != nil {
		root, objectErr := decodeObject(data)
		if objectErr != nil {
			return nil, false, false
		}
		raw, ok := root["errors"]
		if !ok || decodeJSON(raw, &rawEntries) != nil {
			return nil, false, false
		}
	}
	if rawEntries == nil {
		return nil, false, false
	}

	entries := make([]types.NVMeErrorLogEntry, 0, min(len(rawEntries), maxEvidenceEntries))
	complete := true
	for _, raw := range rawEntries {
		entry, keep, valid := parseNVMeErrorEntry(raw)
		if !valid {
			complete = false
			continue
		}
		if keep && len(entries) < maxEvidenceEntries {
			entries = append(entries, entry)
		}
	}
	return entries, complete || len(entries) != 0, complete
}

func parseNVMeErrorEntry(
	data json.RawMessage,
) (types.NVMeErrorLogEntry, bool, bool) {
	entry := types.NVMeErrorLogEntry{}
	root, err := decodeObject(data)
	if err != nil {
		return entry, false, false
	}

	errorCount, ok := requiredUint(root, "error_count", 64)
	if !ok {
		return entry, false, false
	}
	if errorCount == 0 {
		return entry, false, true
	}

	statusCodeType, statusCode, ok := nvmeStatus(root)
	if !ok {
		return entry, false, false
	}

	// sqid is not retained evidence. Use it only when it positively identifies
	// common admin-command noise; a missing sqid must not discard an otherwise
	// complete public record.
	sqid, hasSQID := optionalUint(root, "sqid", 16)
	if hasSQID && sqid == 0 && statusCodeType == 0 &&
		(statusCode == 0x01 || statusCode == 0x02) {
		return entry, false, true
	}

	nsid, ok := requiredUint(root, "nsid", 32)
	if !ok {
		return entry, false, false
	}
	lba, ok := requiredUint(root, "lba", 64)
	if !ok {
		return entry, false, false
	}

	return types.NVMeErrorLogEntry{
		StatusCodeType: uint8(statusCodeType),
		StatusCode:     uint8(statusCode),
		NSID:           uint32(nsid),
		LBA:            lba,
	}, true, true
}

func nvmeStatus(root map[string]json.RawMessage) (uint64, uint64, bool) {
	statusCodeType, hasType := optionalUint(root, "status_code_type", 8)
	statusCode, hasCode := optionalUint(root, "status_code", 8)
	if hasType && hasCode {
		return statusCodeType, statusCode, true
	}
	statusCodeType, hasType = optionalUint(root, "sct", 8)
	statusCode, hasCode = optionalUint(root, "sc", 8)
	if hasType && hasCode {
		return statusCodeType, statusCode, true
	}

	statusField, ok := requiredUint(root, "status_field", 16)
	if !ok {
		return 0, 0, false
	}
	// nvme-cli removes the phase tag before placing status_field in JSON.
	return statusField >> 8 & 0x7, statusField & 0xff, true
}

func parseSCSIHealth(
	data []byte,
) (*types.SCSIHealthEvidence, bool, bool, *uint8) {
	root, err := decodeObject(data)
	if err != nil {
		return nil, false, false, nil
	}

	evidence := &types.SCSIHealthEvidence{}
	complete := true
	var exitStatus *uint8

	if raw, ok := root["smartctl"]; ok {
		smartctl, valid := rawObject(raw)
		if !valid {
			complete = false
		} else if rawExit, ok := smartctl["exit_status"]; ok {
			value, valid := parseUint(rawExit, 8)
			if valid {
				status := uint8(value)
				exitStatus = &status
			} else {
				complete = false
			}
		}
	}

	if raw, ok := root["smart_status"]; ok {
		status, valid := rawObject(raw)
		if !valid {
			complete = false
		} else {
			if rawPassed, ok := status["passed"]; ok {
				passed, valid := parseBool(rawPassed)
				if valid {
					evidence.SmartPassed = &passed
				} else {
					complete = false
				}
			}
			if rawSCSI, ok := status["scsi"]; ok {
				scsi, valid := rawObject(rawSCSI)
				if !valid {
					complete = false
				} else {
					asc, hasASC := optionalUint(scsi, "asc", 8)
					ascq, hasASCQ := optionalUint(scsi, "ascq", 8)
					if hasASC && hasASCQ {
						evidence.InformationException = &types.SCSIInformationException{
							ASC:  uint8(asc),
							ASCQ: uint8(ascq),
						}
					} else if hasInvalidKey(scsi, "asc", hasASC) ||
						hasInvalidKey(scsi, "ascq", hasASCQ) {
						complete = false
					}
				}
			}
		}
	}

	if raw, ok := root["temperature"]; ok {
		temperature, valid := rawObject(raw)
		if !valid {
			complete = false
		} else {
			current, hasCurrent := optionalInt(temperature, "current")
			trip, hasTrip := optionalInt(temperature, "drive_trip")
			if hasCurrent && hasTrip {
				evidence.Temperature = &types.SCSITemperature{
					CurrentCelsius: current,
					TripCelsius:    trip,
				}
			} else if hasInvalidKey(temperature, "current", hasCurrent) ||
				hasInvalidKey(temperature, "drive_trip", hasTrip) {
				complete = false
			}
		}
	}

	if raw, ok := root["scsi_grown_defect_list"]; ok {
		value, valid := parseUint(raw, 64)
		if valid {
			evidence.GrownDefectCount = &value
		} else {
			complete = false
		}
	}

	if raw, ok := root["scsi_error_counter_log"]; ok {
		groups, valid := rawObject(raw)
		if !valid {
			complete = false
		} else {
			evidence.Read, complete = parseSCSIErrorGroup(groups, "read", complete)
			evidence.Write, complete = parseSCSIErrorGroup(groups, "write", complete)
			evidence.Verify, complete = parseSCSIErrorGroup(groups, "verify", complete)
		}
	}

	if raw, ok := root["scsi_pending_defects"]; ok {
		pending, valid, pendingComplete := parseSCSIPending(raw)
		if valid {
			evidence.PendingDefects = pending
		}
		if !pendingComplete {
			complete = false
		}
	}

	trusted := hasSCSIEvidence(evidence)
	return evidence, trusted, complete, exitStatus
}

func parseSCSIErrorGroup(
	groups map[string]json.RawMessage,
	name string,
	complete bool,
) (*types.SCSIErrorStats, bool) {
	raw, ok := groups[name]
	if !ok {
		return nil, complete
	}
	group, valid := rawObject(raw)
	if !valid {
		return nil, false
	}

	stats := &types.SCSIErrorStats{}
	if rawValue, ok := group["gigabytes_processed"]; ok {
		value, valid := parseFloat(rawValue)
		if valid && value >= 0 {
			stats.GigabytesProcessed = &value
		} else {
			complete = false
		}
	}
	parseCounter := func(key string, destination **uint64) {
		rawValue, ok := group[key]
		if !ok {
			return
		}
		value, valid := parseUint(rawValue, 64)
		if !valid {
			complete = false
			return
		}
		*destination = &value
	}
	parseCounter("errors_corrected_by_eccdelayed", &stats.DelayedCorrections)
	parseCounter(
		"errors_corrected_by_rereads_rewrites",
		&stats.RereadRewriteCorrections,
	)
	parseCounter("total_uncorrected_errors", &stats.UncorrectedErrors)

	if stats.GigabytesProcessed == nil &&
		stats.DelayedCorrections == nil &&
		stats.RereadRewriteCorrections == nil &&
		stats.UncorrectedErrors == nil {
		return nil, complete
	}
	return stats, complete
}

func parseSCSIPending(
	data json.RawMessage,
) (*types.SCSIPendingDefects, bool, bool) {
	root, valid := rawObject(data)
	if !valid {
		return nil, false, false
	}
	count, ok := requiredUint(root, "count", 64)
	if !ok {
		return nil, false, false
	}

	lbas := make([]uint64, 0, maxEvidenceEntries)
	var sampleLBAs *[]uint64
	complete := true
	if rawTable, ok := root["table"]; ok {
		var table []json.RawMessage
		if decodeJSON(rawTable, &table) == nil {
			if table == nil {
				complete = false
			} else {
				for _, row := range table {
					if len(lbas) >= maxEvidenceEntries {
						break
					}
					if len(bytes.TrimSpace(row)) == 0 ||
						bytes.Equal(bytes.TrimSpace(row), []byte("null")) {
						continue
					}
					lba, valid := pendingLBA(row)
					if !valid {
						complete = false
						continue
					}
					lbas = append(lbas, lba)
				}
				sampleLBAs = &lbas
			}
		} else {
			var object map[string]json.RawMessage
			if decodeJSON(rawTable, &object) != nil || object == nil {
				complete = false
			} else {
				keys := make([]int, 0, len(object))
				rows := make(map[int]json.RawMessage, len(object))
				for key, row := range object {
					index, err := strconv.Atoi(key)
					if err != nil {
						complete = false
						continue
					}
					keys = append(keys, index)
					rows[index] = row
				}
				sort.Ints(keys)
				for _, key := range keys {
					if len(lbas) >= maxEvidenceEntries {
						break
					}
					lba, valid := pendingLBA(rows[key])
					if !valid {
						complete = false
						continue
					}
					lbas = append(lbas, lba)
				}
				sampleLBAs = &lbas
			}
		}
	} else if count == 0 {
		sampleLBAs = &lbas
	}

	return &types.SCSIPendingDefects{
		Count:      count,
		SampleLBAs: sampleLBAs,
	}, true, complete
}

func pendingLBA(data json.RawMessage) (uint64, bool) {
	row, valid := rawObject(data)
	if !valid {
		return 0, false
	}
	return requiredUint(row, "lba", 64)
}

func hasSCSIEvidence(evidence *types.SCSIHealthEvidence) bool {
	return evidence.SmartPassed != nil ||
		evidence.InformationException != nil ||
		evidence.Temperature != nil ||
		evidence.GrownDefectCount != nil ||
		evidence.Read != nil ||
		evidence.Write != nil ||
		evidence.Verify != nil ||
		evidence.PendingDefects != nil
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := decodeJSON(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func rawObject(data json.RawMessage) (map[string]json.RawMessage, bool) {
	object, err := decodeObject(data)
	return object, err == nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func requiredUint(
	object map[string]json.RawMessage,
	key string,
	bits int,
) (uint64, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	return parseUint(raw, bits)
}

func optionalUint(
	object map[string]json.RawMessage,
	key string,
	bits int,
) (uint64, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	return parseUint(raw, bits)
}

func parseUint(data json.RawMessage, bits int) (uint64, bool) {
	var value any
	if decodeJSON(data, &value) != nil {
		return 0, false
	}

	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.ReplaceAll(strings.TrimSpace(typed), ",", "")
	default:
		return 0, false
	}
	base := 10
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		base = 0
	}
	parsed, err := strconv.ParseUint(text, base, bits)
	return parsed, err == nil
}

func optionalInt(
	object map[string]json.RawMessage,
	key string,
) (int64, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	var value any
	if decodeJSON(raw, &value) != nil {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func parseFloat(data json.RawMessage) (float64, bool) {
	var value any
	if decodeJSON(data, &value) != nil {
		return 0, false
	}
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.TrimSpace(typed)
	default:
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func parseBool(data json.RawMessage) (bool, bool) {
	var value bool
	if decodeJSON(data, &value) != nil {
		return false, false
	}
	return value, true
}

func hasInvalidKey(
	object map[string]json.RawMessage,
	key string,
	valid bool,
) bool {
	_, present := object[key]
	return present && !valid
}
