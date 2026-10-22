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

package dropwatch

import (
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

// Drop source names are shared by standalone and correlated event output.
const (
	SourceUnknown  = "unknown"
	SourceSoftware = "software"
	SourceHardware = "hardware"
)

// Metadata describes an observed drop independently of its packet or stack.
// Strings own their storage so callers may retain them after reusing a record.
type Metadata struct {
	Source      string
	Reason      string
	ReasonGroup string
}

// ResolveMetadata uses the ABI source discriminator, never a reason or stack,
// to distinguish software drops from devlink DROP traps. record must be non-nil.
func ResolveMetadata(record *abi.DropwatchPacketMeta, names ReasonNames) Metadata {
	metadata := Metadata{Source: SourceUnknown}
	switch abi.DropwatchDropSource(record.DropSource) {
	case abi.DropwatchDropSourceHardware:
		metadata.Source = SourceHardware
		metadata.Reason = bytesutil.ToStr(record.TrapName[:])
		metadata.ReasonGroup = bytesutil.ToStr(record.TrapGroupName[:])
	case abi.DropwatchDropSourceSoftware:
		metadata.Source = SourceSoftware
		metadata.Reason = names.Resolve(record.DropReason)
	default:
		metadata.Reason = names.Resolve(record.DropReason)
	}
	return metadata
}
