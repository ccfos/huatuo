// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package dcmi

import "fmt"

const (
	Success Return = iota // 0: Success
)

// String returns the string representation of a Return.
func (r Return) String() string {
	if desc, ok := dcmiErrMap[int32(r)]; ok {
		return desc
	}
	return fmt.Sprintf("unknown error code: %d", int32(r))
}

// dcmiErrMap maps DCMI return codes to human-readable descriptions.
var dcmiErrMap = map[int32]string{
	-8001:  "The input parameter is incorrect",
	-8002:  "Permission error",
	-8003:  "The memory interface operation failed",
	-8004:  "The security function failed to be executed",
	-8005:  "Internal errors",
	-8006:  "Response timed out",
	-8007:  "Invalid deviceID",
	-8008:  "The device does not exist",
	-8009:  "ioctl returns failed",
	-8010:  "The message failed to be sent",
	-8011:  "Message reception failed",
	-8012:  "Not ready yet, please try again",
	-8013:  "This API is not supported in containers",
	-8014:  "The file operation failed",
	-8015:  "Reset failed",
	-8016:  "Reset cancels",
	-8017:  "Upgrading",
	-8020:  "Device resources are occupied",
	-8022:  "Partition consistency check, inconsistent partitions were found",
	-8023:  "The configuration information does not exist",
	-8255:  "Device ID/function is not supported",
	-99997: "dcmi shutdown failed",
	-99998: "The called function is missing, please upgrade the driver",
	-99999: "dcmi libdcmi.so failed to load",
}
