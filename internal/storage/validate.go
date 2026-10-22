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

package storage

import (
	"fmt"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

// Record queries require a positive total limit even when batch size uses its default.
func validateQuery(q driver.Query) error {
	if q.Limit <= 0 {
		return fmt.Errorf("%w: query limit must be positive", driver.ErrInvalidQuery)
	}
	if q.Offset < 0 {
		return fmt.Errorf("%w: query offset must be non-negative", driver.ErrInvalidQuery)
	}
	if q.BatchSize < 0 {
		return fmt.Errorf("%w: batch size must be non-negative", driver.ErrInvalidQuery)
	}

	return nil
}

func validateSaveOptions(options driver.SaveOptions) error {
	switch options.Mode {
	case driver.SaveModeUpsert, driver.SaveModeCreateOnly:
		if len(options.Conditions) != 0 {
			return fmt.Errorf(
				"%w: save conditions require conditional mode",
				driver.ErrInvalidQuery,
			)
		}
	case driver.SaveModeConditional:
		if len(options.Conditions) == 0 {
			return fmt.Errorf(
				"%w: conditional save requires at least one condition",
				driver.ErrInvalidQuery,
			)
		}
	default:
		return fmt.Errorf("%w: unsupported save mode %d", driver.ErrInvalidQuery, options.Mode)
	}
	return nil
}
