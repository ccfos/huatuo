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

import (
	"context"
	"fmt"
)

// DcGetDeviceHealth returns the health status of a specific NPU device.
func (l *library) DcGetDeviceHealth(ctx context.Context, cardId, deviceId uint32) (uint32, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	var health uint32
	if err := checkReturnCode("dcmi_get_device_health", dcGetDeviceHealth(cardId, deviceId, &health)); err != nil {
		return 0, err
	}

	return health, nil
}

// DcGetCardList returns the list of card IDs present in the system.
func (l *library) DcGetCardList() (int32, []int32, error) {
	const maxCards = 64

	var cNum int32
	ids := make([]int32, maxCards)

	if err := checkReturnCode("dcmi_get_card_list", dcGetCardList(&cNum, &ids[0], maxCards)); err != nil {
		return 0, nil, err
	}

	if cNum <= 0 || cNum > maxCards {
		return 0, nil, fmt.Errorf("invalid card count: %d", cNum)
	}

	result := make([]int32, 0, cNum)
	for i := int32(0); i < cNum; i++ {
		if ids[i] < 0 {
			continue
		}
		result = append(result, ids[i])
	}

	return cNum, result, nil
}

// DcGetDeviceNumInCard returns the number of devices in the specified card.
func (l *library) DcGetDeviceNumInCard(cardId int32) (int32, error) {
	const maxDevicesPerCard = 4

	if cardId < 0 {
		return 0, fmt.Errorf("invalid card ID: %d", cardId)
	}

	var deviceNum int32
	if err := checkReturnCode("dcmi_get_device_num_in_card", dcGetDeviceNumInCard(cardId, &deviceNum)); err != nil {
		return 0, err
	}

	if deviceNum <= 0 || deviceNum > maxDevicesPerCard {
		return 0, fmt.Errorf("invalid device count in card %d: %d", cardId, deviceNum)
	}

	return deviceNum, nil
}

