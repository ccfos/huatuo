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

package job

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/profiling"
	tracingdomain "github.com/ccfos/huatuo/pkg/tracing"
)

const maxJobPageSize = 1000

func validateManagerConfig(config *ManagerConfig) error {
	if config == nil {
		return errors.New("create job manager: config is required")
	}
	if strings.TrimSpace(config.StoreDSN) == "" {
		return errors.New("create job manager: store dsn is required")
	}
	for _, policyConfig := range []struct {
		kind   Kind
		policy Policy
	}{
		{kind: KindProfiling, policy: config.ProfilingPolicy},
		{kind: KindTracing, policy: config.TracingPolicy},
	} {
		kind, policy := policyConfig.kind, policyConfig.policy
		if policy.MaxJobsPerHost == 0 && policy.MaxTotalJobs == 0 {
			return fmt.Errorf(
				"create job manager: policy for %s is required",
				kind,
			)
		}
		if policy.MaxJobsPerHost <= 0 || policy.MaxTotalJobs <= 0 {
			return fmt.Errorf(
				"create job manager: %s quotas must be greater than zero",
				kind,
			)
		}
	}
	for _, lifecycleConfig := range []struct {
		name  string
		value time.Duration
	}{
		{name: "status poll interval", value: config.StatusPollInterval},
		{name: "pending timeout", value: config.PendingTimeout},
		{name: "completion grace period", value: config.CompletionGracePeriod},
		{
			name:  "Node unavailable grace period",
			value: config.NodeUnavailableGracePeriod,
		},
		{name: "Job retention period", value: config.JobRetentionPeriod},
	} {
		if lifecycleConfig.value <= 0 {
			return fmt.Errorf(
				"create job manager: %s must be positive",
				lifecycleConfig.name,
			)
		}
	}
	return nil
}

func validateListPageQuery(query *Query) error {
	if query == nil || query.Limit <= 0 || query.Limit > maxJobPageSize {
		return fmt.Errorf(
			"%w: page limit must be between 1 and %d",
			ErrInvalidQuery,
			maxJobPageSize,
		)
	}
	if query.Offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", ErrInvalidQuery)
	}
	return nil
}

func (r *CreateRequest) validate() error {
	if r == nil {
		return errors.New("request is required")
	}
	if r.UserID == "" {
		return errors.New("job user ID is required")
	}
	if r.Hostname == "" {
		return errors.New("job hostname is required")
	}
	if r.Duration <= 0 || r.Duration%time.Second != 0 {
		return errors.New("job duration must be a whole positive number of seconds")
	}
	if err := observation.ValidateScope(r.Scope, r.ContainerID); err != nil {
		return fmt.Errorf("validate job scope: %w", err)
	}
	if err := r.Spec.validate(r.Spec.kind(), r.Scope); err != nil {
		return err
	}
	return nil
}

func (j *Job) validateStored() error {
	if j == nil {
		return errors.New("job is nil")
	}
	if j.ID == "" {
		return errors.New("job ID is required")
	}
	if j.revision <= 0 {
		return errors.New("job storage revision is required")
	}
	if j.UserID == "" {
		return errors.New("job user ID is required")
	}
	if j.Hostname == "" {
		return errors.New("job hostname is required")
	}
	if j.Duration <= 0 || j.Duration%time.Second != 0 {
		return errors.New("job duration must be a whole positive number of seconds")
	}
	if err := observation.ValidateScope(j.Scope, j.ContainerID); err != nil {
		return fmt.Errorf("validate job scope: %w", err)
	}
	if err := j.Spec.validate(j.Kind, j.Scope); err != nil {
		return err
	}
	if j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() {
		return errors.New("job created and updated timestamps are required")
	}
	return j.validateState()
}

func (j *Job) validateState() error {
	if !isValidStatus(j.Status) {
		return fmt.Errorf("unsupported job status %q", j.Status)
	}
	if (j.Status == StatusTerminal) != (j.Terminal != nil) {
		return errors.New("job terminal result must be present exactly when status is terminal")
	}
	if j.Terminal != nil {
		if !isValidOutcome(j.Terminal.Outcome) {
			return fmt.Errorf("unsupported job terminal outcome %q", j.Terminal.Outcome)
		}
		if j.Terminal.Outcome == OutcomeFailed && !isValidFailureReason(j.Terminal.Reason) {
			return fmt.Errorf("unsupported job failure reason %q", j.Terminal.Reason)
		}
	}
	if isTerminal(j.Status) != !j.EndedAt.IsZero() {
		return errors.New("job ended timestamp must be present exactly when status is terminal")
	}
	if !isValidStopReason(j.StopReason) {
		return fmt.Errorf("unsupported job stop reason %q", j.StopReason)
	}
	if j.Status == StatusRunning && (j.StartedAt.IsZero() || j.ExecutionDeadline.IsZero()) {
		return errors.New("running job requires started and execution deadline timestamps")
	}
	if j.Status == StatusStopping &&
		(j.StopReason == "" || j.StopDeadline.IsZero()) {
		return errors.New("stopping job requires a persisted stop intent and deadline")
	}
	return nil
}

func (s Spec) validate(kind Kind, scope observation.Scope) error {
	switch kind {
	case KindProfiling:
		if s.Profiling == nil || s.Tracing != nil {
			return errors.New("profiling job must contain only a profiling spec")
		}
		if err := s.Profiling.Validate(); err != nil {
			return fmt.Errorf("validate profiling job spec: %w", err)
		}
		if !profiling.SupportsScope(s.Profiling.Language, s.Profiling.Type, scope) {
			return fmt.Errorf("profiling job does not support scope %q", scope)
		}
	case KindTracing:
		if s.Tracing == nil || s.Profiling != nil {
			return errors.New("tracing job must contain only a tracing spec")
		}
		if err := s.Tracing.Validate(); err != nil {
			return fmt.Errorf("validate tracing job spec: %w", err)
		}
		if !tracingdomain.SupportsScope(s.Tracing.Type, scope) {
			return fmt.Errorf("tracing job does not support scope %q", scope)
		}
	default:
		return fmt.Errorf("unsupported job kind %q", kind)
	}
	return nil
}

func isValidStatus(status Status) bool {
	switch status {
	case StatusPending,
		StatusRunning,
		StatusStopping,
		StatusTerminal:
		return true
	default:
		return false
	}
}

func isValidOutcome(outcome Outcome) bool {
	return outcome == OutcomeCompleted || outcome == OutcomeFailed ||
		outcome == OutcomeStopped || outcome == OutcomeUnknown
}

func isValidFailureReason(reason FailureReason) bool {
	switch reason {
	case FailureReasonExecutionStartFailed,
		FailureReasonExecutionCapacityExceeded,
		FailureReasonStartTimeout,
		FailureReasonExecutionFailed,
		FailureReasonExecutionTimedOut,
		FailureReasonStopTimeout,
		FailureReasonNodeUnavailable,
		FailureReasonOperationLost,
		FailureReasonInvalidNodeRequest,
		FailureReasonProtocolError:
		return true
	default:
		return false
	}
}

func isValidStopReason(reason StopReason) bool {
	switch reason {
	case "", StopReasonUser, StopReasonStartTimeout, StopReasonExecutionTimeout:
		return true
	default:
		return false
	}
}
