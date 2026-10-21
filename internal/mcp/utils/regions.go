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

package utils

import (
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

func RequiredRegions(arguments map[string]any, maximum int) ([]string, error) {
	var requested []string
	switch values := arguments[constant.ArgumentRegion].(type) {
	case []string:
		requested = values
	case []any:
		requested = make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("region must contain only strings")
			}
			requested[index] = text
		}
	case nil:
		return nil, fmt.Errorf("region is required")
	default:
		return nil, fmt.Errorf("region must be an array of strings")
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("region is required")
	}
	if maximum <= 0 {
		maximum = constant.MaxRegions
	}
	maximum = min(maximum, constant.MaxRegions)
	if len(requested) > maximum {
		return nil, fmt.Errorf("region must contain at most %d values", maximum)
	}
	regions := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, region := range requested {
		region = strings.TrimSpace(region)
		switch strings.ToLower(region) {
		case "", "all", "*", "_all":
			return nil, fmt.Errorf("region must contain only exact region names")
		}
		if len(region) > constant.MaxRegionNameLength {
			return nil, fmt.Errorf("region values must not exceed %d bytes", constant.MaxRegionNameLength)
		}
		if _, exists := seen[region]; exists {
			continue
		}
		seen[region] = struct{}{}
		regions = append(regions, region)
	}
	return regions, nil
}
