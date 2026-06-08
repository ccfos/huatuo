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

// API provides the public interface for Ascend NPU monitoring.
var (
	DcGetDeviceHealth         = libdcmi.DcGetDeviceHealth
	DcGetCardList             = libdcmi.DcGetCardList
	DcGetDeviceNumInCard      = libdcmi.DcGetDeviceNumInCard
	DcGetDevicePowerInfo      = libdcmi.DcGetDevicePowerInfo
	DcGetDeviceTemperature    = libdcmi.DcGetDeviceTemperature
	DcGetDeviceVoltage        = libdcmi.DcGetDeviceVoltage
	DcGetDeviceUtilizationRate = libdcmi.DcGetDeviceUtilizationRate
	DcGetDeviceFrequency      = libdcmi.DcGetDeviceFrequency
	DcGetDeviceNetWorkHealth  = libdcmi.DcGetDeviceNetWorkHealth
	DcGetHbmInfo             = libdcmi.DcGetHbmInfo
	DcGetDeviceEccInfo       = libdcmi.DcGetDeviceEccInfo
	DcGetPCIeBusInfo         = libdcmi.DcGetPCIeBusInfo
	DcGetDeviceLogicID        = libdcmi.DcGetDeviceLogicID
	DcGetPhysicIDFromLogicID  = libdcmi.DcGetPhysicIDFromLogicID
)
