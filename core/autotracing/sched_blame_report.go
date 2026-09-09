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

package autotracing

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	schedBlameExternalTracerName                 = "sched-blame-external"
	schedBlameExternalContentionRatioHistorySize = 600
	schedBlameExternalMinimumSamples             = 60
	schedBlameExternalPercentile                 = 99
	schedBlameTopExternalCompetitors             = 10
	schedBlameContainerIDPrefixLength            = 13
	schedBlamePercentScale                       = 100.0
)

type SchedBlameExternalData struct {
	TargetContainerID                  string  `json:"target_container_id"`
	TargetContainerName                string  `json:"target_container_name"`
	IntervalSecs                       int     `json:"interval_secs"`
	SliceDropPercent                   uint32  `json:"slice_drop_percent"`
	CurrentExternalContentionPercent   float64 `json:"current_external_contention_percent"`
	HistoricalP99Percent               float64 `json:"historical_p99_percent"`
	ExternalAnomalyK                   float64 `json:"external_anomaly_k"`
	ExternalContentionThresholdPercent float64 `json:"external_contention_threshold_percent"`
	TargetRuntimeNs                    uint64  `json:"target_runtime_ns"`
	InternalContentionNs               uint64  `json:"internal_contention_ns"`
	ExternalContentionNs               uint64  `json:"external_contention_ns"`
	ThrottledTimeNs                    uint64  `json:"throttled_time_ns"`
	// EstimatedTotalWaitNs is SchedBlame's unweighted analog of the custom
	// kernel's task-weighted hierarchy_wait_sum counter.
	EstimatedTotalWaitNs   uint64                             `json:"estimated_total_wait_ns"`
	EstimatedTotalDemandNs uint64                             `json:"estimated_total_demand_ns"`
	TopExternalCompetitors []schedBlameExternalCompetitorItem `json:"top_external_competitors"`
}

type schedBlameExternalCompetitorItem struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	ChargeNs      uint64 `json:"charge_ns"`
}

type schedBlameExternalContentionRatioHistory struct {
	values [schedBlameExternalContentionRatioHistorySize]float64
	next   int
	count  int
}

func (history *schedBlameExternalContentionRatioHistory) add(value float64) {
	history.values[history.next] = value
	history.next = (history.next + 1) % len(history.values)
	if history.count < len(history.values) {
		history.count++
	}
}

func (history *schedBlameExternalContentionRatioHistory) p99(
	scratch *[schedBlameExternalContentionRatioHistorySize]float64,
) (float64, bool) {
	if history.count < schedBlameExternalMinimumSamples {
		return 0, false
	}
	values := scratch[:history.count]
	copy(values, history.values[:history.count])
	sort.Float64s(values)
	rank := (schedBlameExternalPercentile*len(values) + 100 - 1) / 100
	return values[rank-1], true
}

type schedBlameExternalCompetitorCharge struct {
	cgid     uint64
	chargeNs uint64
}

type schedBlameExternalEvent struct {
	targetIndex                      uint8
	currentExternalContentionRatio   float64
	historicalP99                    float64
	externalAnomalyK                 float64
	externalContentionRatioThreshold float64
	targetRuntimeNs                  uint64
	internalContentionNs             uint64
	externalContentionNs             uint64
	throttledTimeNs                  uint64
	estimatedTotalWaitNs             uint64
	estimatedTotalDemandNs           uint64
	externalCompetitors              []schedBlameExternalCompetitorCharge
}

func schedBlameRatioToPercent(value float64) float64 {
	return value * schedBlamePercentScale
}

func schedBlameHighlightContainerMatches(selector, containerID, name string) bool {
	selector = strings.TrimSpace(selector)
	return selector != "" && (selector == name || selector == containerID ||
		strings.HasPrefix(containerID, selector))
}

func schedBlameIrmasLogFields(
	sample *schedBlameIrmasSample,
) string {
	if !sample.valid {
		return "irmas_waitrate_percent=N/A"
	}
	return fmt.Sprintf(
		"irmas_waitrate_percent=%.4f%%",
		schedBlameRatioToPercent(sample.waitrate),
	)
}

func schedBlameRatioLogValue(value float64, available bool) string {
	if !available {
		return "N/A"
	}
	return fmt.Sprintf("%.4f%%", schedBlameRatioToPercent(value))
}

func (state *schedBlameState) logExternalContentionRatio(
	config *schedBlameRuntimeConfig,
	targetIndex int,
	runtime, internal, external, throttled uint64,
	current, historicalP99, threshold float64,
	externalAnomalyK float64,
	historySamples int,
	sampleValid, ready, emitted bool,
	irmasSample *schedBlameIrmasSample,
) {
	if !config.highlightConfigured() ||
		targetIndex != schedBlameHighlightIndex {
		return
	}
	target := state.targets[targetIndex]
	cssID := "N/A"
	if target.cssKnown {
		cssID = fmt.Sprintf("%d", target.cssID)
	}
	irmasFields := schedBlameIrmasLogFields(irmasSample)
	estimatedTotalWait := internal + external + throttled
	estimatedTotalDemand := runtime + estimatedTotalWait
	log.Debugf(
		"sched-blame: external ratio highlight container=%s id=%s dense_id=%d css_id=%s %s sample_valid=%t history_samples=%d current_percent=%s p99_percent=%s k=%.3f threshold_percent=%s runtime_ns=%d internal_contention_ns=%d external_contention_ns=%d throttled_time_ns=%d estimated_total_wait_ns=%d estimated_total_demand_ns=%d ready=%t emitted=%t",
		target.name,
		schedBlameShortID(target.containerID),
		targetIndex,
		cssID,
		irmasFields,
		sampleValid,
		historySamples,
		schedBlameRatioLogValue(current, sampleValid),
		schedBlameRatioLogValue(historicalP99, ready),
		externalAnomalyK,
		schedBlameRatioLogValue(threshold, ready),
		runtime,
		internal,
		external,
		throttled,
		estimatedTotalWait,
		estimatedTotalDemand,
		ready,
		emitted,
	)
}

func (state *schedBlameState) topExternalCompetitors(
	targetIndex int,
) []schedBlameExternalCompetitorCharge {
	chargeByCgid := make(map[uint64]uint64)
	for competitorCSSID := 0; competitorCSSID < schedBlameMaxCSSIDs; competitorCSSID++ {
		if !state.cssKnown[competitorCSSID] {
			continue
		}
		chargeIndex := schedBlameExternalChargeIndex(
			uint16(competitorCSSID),
			targetIndex,
		)
		charge := state.externalChargeNsMatrix[chargeIndex]
		if charge == 0 {
			continue
		}
		cgid := state.cssCgids[competitorCSSID]
		if cgid == 0 {
			continue
		}
		chargeByCgid[cgid] += charge
	}
	competitors := make([]schedBlameExternalCompetitorCharge, 0,
		len(chargeByCgid))
	for cgid, charge := range chargeByCgid {
		competitors = append(competitors, schedBlameExternalCompetitorCharge{
			cgid:     cgid,
			chargeNs: charge,
		})
	}
	sort.Slice(competitors, func(i, j int) bool {
		if competitors[i].chargeNs != competitors[j].chargeNs {
			return competitors[i].chargeNs > competitors[j].chargeNs
		}
		return competitors[i].cgid < competitors[j].cgid
	})
	if len(competitors) > schedBlameTopExternalCompetitors {
		competitors = competitors[:schedBlameTopExternalCompetitors]
	}
	return competitors
}

func (state *schedBlameState) evaluateExternalContentionRatios() []schedBlameExternalEvent {
	config := schedBlameRuntimeConfigSnapshot()
	events, _ := state.evaluateExternalContentionRatiosWithDebug(
		time.Time{},
		nil,
		nil,
		nil,
		&config,
	)
	return events
}

func (state *schedBlameState) evaluateExternalContentionRatiosWithDebug(
	occurredAt time.Time,
	debugWriter *schedBlameExternalRatioDebugWriter,
	irmasSamples map[uint64]schedBlameIrmasSample,
	uploader *schedBlameUploader,
	config *schedBlameRuntimeConfig,
) ([]schedBlameExternalEvent, error) {
	var scratch [schedBlameExternalContentionRatioHistorySize]float64
	externalAnomalyK := config.externalAnomalyK
	externalEvents := make([]schedBlameExternalEvent, 0)
	var debugSamples []schedBlameExternalRatioDebugSample
	if debugWriter != nil {
		debugSamples = make([]schedBlameExternalRatioDebugSample, 0,
			state.activeTargetCount())
	}
	var liveCSS map[uint64]string
	var liveContainers map[string]*pod.Container
	for targetIndex := range state.targets {
		if !state.activeTargetBitmap.contains(targetIndex) {
			continue
		}
		target := state.targets[targetIndex]
		irmasSample := irmasSamples[target.cgid]
		runtime := state.runtimeNsByTarget[targetIndex]
		internal := state.internalContentionNsByTarget[targetIndex]
		external := state.externalContentionNsByTarget[targetIndex]
		throttled := state.throttledTimeNsByTarget[targetIndex]
		estimatedTotalWait := internal + external + throttled
		estimatedTotalDemand := runtime + estimatedTotalWait
		history := &state.externalContentionRatioHistory[targetIndex]
		historicalP99, ready := history.p99(&scratch)
		externalContentionRatioThreshold := historicalP99 * externalAnomalyK
		// A ratio of zero is valid as long as the target had demand.
		if estimatedTotalDemand == 0 {
			state.logExternalContentionRatio(
				config,
				targetIndex,
				runtime,
				internal,
				external,
				throttled,
				0,
				historicalP99,
				externalContentionRatioThreshold,
				externalAnomalyK,
				history.count,
				false,
				ready,
				false,
				&irmasSample,
			)
			if debugWriter != nil {
				debugSamples = append(debugSamples,
					state.newExternalRatioDebugSample(
						targetIndex,
						runtime,
						internal,
						external,
						throttled,
						0,
						historicalP99,
						externalContentionRatioThreshold,
						externalAnomalyK,
						history.count,
						false,
						ready,
						false,
						&irmasSample,
					),
				)
			}
			continue
		}
		currentExternalContentionRatio := float64(external) / float64(estimatedTotalDemand)
		breached := ready && historicalP99 > 0 &&
			currentExternalContentionRatio > externalContentionRatioThreshold
		emitted := false
		var externalEvent schedBlameExternalEvent
		if breached {
			externalEvent = schedBlameExternalEvent{
				targetIndex:                      uint8(targetIndex),
				currentExternalContentionRatio:   currentExternalContentionRatio,
				historicalP99:                    historicalP99,
				externalAnomalyK:                 externalAnomalyK,
				externalContentionRatioThreshold: externalContentionRatioThreshold,
				targetRuntimeNs:                  runtime,
				internalContentionNs:             internal,
				externalContentionNs:             external,
				throttledTimeNs:                  throttled,
				estimatedTotalWaitNs:             estimatedTotalWait,
				estimatedTotalDemandNs:           estimatedTotalDemand,
				externalCompetitors:              state.topExternalCompetitors(targetIndex),
			}
			externalEvents = append(externalEvents, externalEvent)
			if uploader != nil {
				if liveCSS == nil {
					liveCSS, liveContainers = schedBlameLiveContainers()
				}
				emitted = state.enqueueExternalEvent(
					&externalEvent,
					occurredAt,
					uploader,
					liveCSS,
					liveContainers,
					config.sliceDropPercent,
				)
			}
		}
		state.logExternalContentionRatio(
			config,
			targetIndex,
			runtime,
			internal,
			external,
			throttled,
			currentExternalContentionRatio,
			historicalP99,
			externalContentionRatioThreshold,
			externalAnomalyK,
			history.count,
			true,
			ready,
			emitted,
			&irmasSample,
		)
		if debugWriter != nil {
			debugSamples = append(debugSamples,
				state.newExternalRatioDebugSample(
					targetIndex,
					runtime,
					internal,
					external,
					throttled,
					currentExternalContentionRatio,
					historicalP99,
					externalContentionRatioThreshold,
					externalAnomalyK,
					history.count,
					true,
					ready,
					emitted,
					&irmasSample,
				),
			)
		}
		// The current second must not influence its own threshold.
		history.add(currentExternalContentionRatio)
	}
	var debugWriteErr error
	if debugWriter != nil && len(debugSamples) != 0 {
		debugWriteErr = debugWriter.writeBatch(schedBlameExternalRatioDebugBatch{
			timestamp: occurredAt,
			samples:   debugSamples,
		})
	}

	clear(state.runtimeNsByTarget[:])
	clear(state.internalContentionNsByTarget[:])
	clear(state.externalContentionNsByTarget[:])
	clear(state.throttledTimeNsByTarget[:])
	clear((*state.externalChargeNsMatrix)[:])
	return externalEvents, debugWriteErr
}

func schedBlameShortID(value string) string {
	if len(value) <= schedBlameContainerIDPrefixLength {
		return value
	}
	return value[:schedBlameContainerIDPrefixLength]
}

func schedBlameResolveCgid(
	cgid uint64,
	css map[uint64]string,
	containers map[string]*pod.Container,
) (string, string) {
	containerID, exists := css[cgid]
	if !exists {
		label := fmt.Sprintf("cgid-%016x", cgid)
		return label, label
	}
	name := containerID
	if container, exists := containers[containerID]; exists {
		name = container.Hostname
	}
	return schedBlameShortID(containerID), name
}

func schedBlameLiveContainers() (
	map[uint64]string,
	map[string]*pod.Container,
) {
	css, err := pod.GetCSSToContainerID("cpu")
	if err != nil {
		log.Warnf("sched-blame: GetCSSToContainerID: %v", err)
		css = make(map[uint64]string)
	}
	containers, err := pod.Containers()
	if err != nil {
		log.Warnf("sched-blame: Containers: %v", err)
		containers = make(map[string]*pod.Container)
	}
	return css, containers
}

type schedBlameUpload struct {
	request *tracing.WriteRequest
}

type schedBlameUploader struct {
	queue        chan schedBlameUpload
	done         chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	write        schedBlameEventWriter
	drainTimeout time.Duration
	dropped      atomic.Uint64
}

func newSchedBlameUploader(
	write schedBlameEventWriter,
) *schedBlameUploader {
	ctx, cancel := context.WithCancel(context.Background())
	uploader := &schedBlameUploader{
		queue: make(chan schedBlameUpload,
			schedBlameUploadQueueCapacity),
		done:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		write:        write,
		drainTimeout: schedBlameUploadDrainTimeout,
	}
	go uploader.run()
	return uploader
}

func (uploader *schedBlameUploader) run() {
	defer close(uploader.done)
	for upload := range uploader.queue {
		if err := uploader.write(uploader.ctx, upload.request); err != nil {
			if uploader.ctx.Err() != nil {
				uploader.dropped.Add(1)
			} else {
				log.Warnf("sched-blame: save external event: %v", err)
			}
		}
		if uploader.ctx.Err() != nil {
			for range uploader.queue {
				uploader.dropped.Add(1)
			}
			return
		}
	}
}

func (uploader *schedBlameUploader) enqueue(
	upload schedBlameUpload,
) bool {
	select {
	case uploader.queue <- upload:
		return true
	default:
		uploader.dropped.Add(1)
		return false
	}
}

func (uploader *schedBlameUploader) close() {
	close(uploader.queue)
	timer := time.NewTimer(uploader.drainTimeout)
	defer timer.Stop()
	select {
	case <-uploader.done:
		uploader.cancel()
	case <-timer.C:
		uploader.cancel()
		<-uploader.done
	}
}

func (uploader *schedBlameUploader) stats() (int, int, uint64) {
	return len(uploader.queue), cap(uploader.queue), uploader.dropped.Load()
}

func (state *schedBlameState) enqueueExternalEvent(
	externalEvent *schedBlameExternalEvent,
	occurredAt time.Time,
	uploader *schedBlameUploader,
	css map[uint64]string,
	containers map[string]*pod.Container,
	sliceDropPercent uint32,
) bool {
	targetIndex := int(externalEvent.targetIndex)
	if targetIndex >= schedBlameMaxTargets ||
		!state.activeTargetBitmap.contains(targetIndex) {
		return false
	}
	target := state.targets[targetIndex]

	items := make([]schedBlameExternalCompetitorItem, 0,
		len(externalEvent.externalCompetitors))
	for _, competitor := range externalEvent.externalCompetitors {
		id, name := schedBlameResolveCgid(
			competitor.cgid,
			css,
			containers,
		)
		items = append(items, schedBlameExternalCompetitorItem{
			ContainerID:   id,
			ContainerName: name,
			ChargeNs:      competitor.chargeNs,
		})
	}
	payload := &SchedBlameExternalData{
		TargetContainerID:   schedBlameShortID(target.containerID),
		TargetContainerName: target.name,
		IntervalSecs:        1,
		SliceDropPercent:    sliceDropPercent,
		CurrentExternalContentionPercent: schedBlameRatioToPercent(
			externalEvent.currentExternalContentionRatio,
		),
		HistoricalP99Percent: schedBlameRatioToPercent(
			externalEvent.historicalP99,
		),
		ExternalAnomalyK: externalEvent.externalAnomalyK,
		ExternalContentionThresholdPercent: schedBlameRatioToPercent(
			externalEvent.externalContentionRatioThreshold,
		),
		TargetRuntimeNs:        externalEvent.targetRuntimeNs,
		InternalContentionNs:   externalEvent.internalContentionNs,
		ExternalContentionNs:   externalEvent.externalContentionNs,
		ThrottledTimeNs:        externalEvent.throttledTimeNs,
		EstimatedTotalWaitNs:   externalEvent.estimatedTotalWaitNs,
		EstimatedTotalDemandNs: externalEvent.estimatedTotalDemandNs,
		TopExternalCompetitors: items,
	}
	return uploader.enqueue(schedBlameUpload{
		request: &tracing.WriteRequest{
			TracerName:        schedBlameExternalTracerName,
			ContainerID:       target.containerID,
			ObservedTimestamp: occurredAt,
			TracerData:        payload,
			TracerRunType:     types.TracerRunTypeEvent,
		},
	})
}
