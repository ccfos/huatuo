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

package procfs

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ArpCacheStats contains statistics for all the counters from `/proc/net/stat/arp_cache`
type ArpCacheStats struct {
	Stats map[string]uint64
}

// NetArpCache retrieves stats from `/proc/net/stat/arp_cache`,
//
// Not available in prometheus procfs:
// https://github.com/prometheus/procfs
func NetArpCache() (*ArpCacheStats, error) {
	file, err := os.Open(Path("net/stat/arp_cache"))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Scan()
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// First string is always a header for stats
	headers := strings.Fields(scanner.Text())

	// Fast path ...
	cache := &ArpCacheStats{Stats: make(map[string]uint64)}

	scanner.Scan()
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	fields := strings.Fields(scanner.Text())
	if len(fields) > len(headers) {
		// Counters are keyed by header position, so a wider row would either
		// panic on the header lookup or report unnamed counters. Reject the
		// row instead of guessing the layout.
		return nil, fmt.Errorf(
			"arp cache data row has %d fields, header has %d fields",
			len(fields),
			len(headers),
		)
	}

	for num, counter := range fields {
		value, err := strconv.ParseUint(counter, 16, 64)
		if err != nil {
			return nil, err
		}
		cache.Stats[headers[num]] = value
	}

	return cache, nil
}
