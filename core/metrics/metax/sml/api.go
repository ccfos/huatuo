// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The MetaX Authors
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

package sml

// API provides the public interface for MetaX GPU monitoring
var (
	GetSDKVersion                    = libsml.getSdkVersion
	GetNativeAndVFGPUCount           = libsml.getNativeAndVfGpuCount
	GetPFGPUCount                    = libsml.getPfGpuCount
	GetGPUInfo                       = libsml.getGpuInfo
	GetGPUVersion                    = libsml.getGpuVersion
	ListGPUBoardWayElectricInfos     = libsml.listGpuBoardWayElectricInfos
	GetGPUPcieLinkInfo               = libsml.getGpuPcieLinkInfo
	GetGPUPcieThroughputInfo         = libsml.getGpuPcieThroughputInfo
	ListGPUMetaXLinkLinkInfos        = libsml.listGpuMetaxlinkLinkInfos
	ListGPUMetaXLinkThroughputInfos  = libsml.listGpuMetaxlinkThroughputInfos
	ListGPUMetaXLinkTrafficStatInfos = libsml.listGpuMetaxlinkTrafficStatInfos
	ListGPUMetaXLinkAerErrorsInfos   = libsml.listGpuMetaxlinkAerErrorsInfos
	GetDieStatus                     = libsml.getDieStatus
	GetDieTemperature                = libsml.getDieTemperature
	GetDieUtilization                = libsml.getDieUtilization
	GetDieMemoryInfo                 = libsml.getDieMemoryInfo
	ListDieClocks                    = libsml.listDieClocks
	GetDieClocksThrottleStatus       = libsml.getDieClocksThrottleStatus
	GetDieDPMPerformanceLevel        = libsml.getDieDpmPerformanceLevel
	GetDieECCMemoryInfo              = libsml.getDieEccMemoryInfo
	GetErrorString                   = libsml.getErrorString
)
