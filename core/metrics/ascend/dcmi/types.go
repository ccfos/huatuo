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

// Return is the DCMI return code type.
type Return int32

// DCMI API raw symbols — C function pointers registered at init time.
var (
	// Initialization
	dcmiInit func() Return

	// Device health: dcmi_get_device_health(card_id, device_id, *health)
	dcGetDeviceHealth func(uint32, uint32, *uint32) Return

	// Device enumeration
	// dcmi_get_card_list(*card_num, *card_list, list_len)
	dcGetCardList func(*int32, *int32, int32) Return
	// dcmi_get_device_num_in_card(card_id, *device_num)
	dcGetDeviceNumInCard func(int32, *int32) Return
)
