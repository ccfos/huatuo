// Copyright 2025 The HuaTuo Authors
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

// Package listing provides shared helpers for job list endpoints:
// sort key parsing, in-memory pagination, and request constants.
package listing

import (
	"errors"
	"sort"

	"huatuo-bamai/internal/job"
)

// StatusStopped is the only status value accepted by PATCH endpoints.
const StatusStopped = "stopped"

// SortJobs sorts jobs in place. Supported keys: start_time, end_time, host, container.
// A leading "-" means descending. Empty key defaults to "-start_time" (newest first).
func SortJobs(jobs []*job.Job, sortKey string) error {
	if sortKey == "" {
		sortKey = "-start_time"
	}
	desc := false
	field := sortKey
	if sortKey[0] == '-' {
		desc = true
		field = sortKey[1:]
	}

	var less func(i, j int) bool
	switch field {
	case "start_time":
		less = func(i, j int) bool { return jobs[i].StartTime.Before(jobs[j].StartTime) }
	case "end_time":
		less = func(i, j int) bool { return jobs[i].EndTime.Before(jobs[j].EndTime) }
	case "host":
		less = func(i, j int) bool { return jobs[i].Host < jobs[j].Host }
	case "container":
		less = func(i, j int) bool { return jobs[i].Container < jobs[j].Container }
	default:
		return errors.New("invalid sort field: " + field)
	}

	if desc {
		sort.Slice(jobs, func(i, j int) bool { return less(j, i) })
	} else {
		sort.Slice(jobs, less)
	}
	return nil
}

// Paginate slices jobs by offset and limit.
func Paginate(jobs []*job.Job, offset, limit int) []*job.Job {
	if offset >= len(jobs) {
		return nil
	}
	end := offset + limit
	if end > len(jobs) {
		end = len(jobs)
	}
	return jobs[offset:end]
}
