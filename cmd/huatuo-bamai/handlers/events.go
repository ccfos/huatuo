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

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/internal/log"
	nodecloudevents "github.com/ccfos/huatuo/internal/nodeagent/cloudevents"
)

// WatchEvents opens a filtered CloudEvents subscription.
func (h *NodeAPIHandler) WatchEvents(
	ctx context.Context,
	request nodeapi.WatchEventsRequestObject,
) (nodeapi.WatchEventsResponseObject, error) {
	filters := cloudEventFilters(request.Body.Filters)
	subscription, err := h.cloudEvents.Subscribe(ctx, &filters)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("subscribe to cloud events: %w", err))
	}
	log.Infof("[eventwatch] connected: filters=%+v", filters)

	cacheControl := "no-cache"
	connection := "keep-alive"
	xAccelBuffering := "no"
	return nodeapi.WatchEvents200TextEventStreamResponse{
		Body: newWatchEventsBody(subscription, h.keepAliveInterval),
		Headers: nodeapi.WatchEvents200ResponseHeaders{
			CacheControl:    &cacheControl,
			Connection:      &connection,
			XAccelBuffering: &xAccelBuffering,
		},
	}, nil
}

type watchEventsBody struct {
	subscription *nodecloudevents.Subscription
	ticker       *time.Ticker
	pending      []byte
}

var _ io.ReadCloser = (*watchEventsBody)(nil)

func newWatchEventsBody(
	subscription *nodecloudevents.Subscription,
	keepAliveInterval time.Duration,
) *watchEventsBody {
	return &watchEventsBody{
		subscription: subscription,
		ticker:       time.NewTicker(keepAliveInterval),
	}
}

func (body *watchEventsBody) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}

	for len(body.pending) == 0 {
		select {
		case <-body.ticker.C:
			// Send an SSE comment line (RFC 8895 §6.8). Comment lines start
			// with ':' and are silently discarded by SSE clients at the
			// application layer, so they never surface as events. Their sole
			// purpose is to push bytes through the TCP connection so that
			// intermediate proxies and load balancers do not treat the idle
			// connection as stale and close it prematurely.
			// A single '\n' is used (not '\n\n') to avoid triggering a
			// spurious empty-event dispatch in the client's SSE parser.
			body.pending = []byte(": ping\n")
		case event, ok := <-body.subscription.Events():
			if !ok {
				return 0, io.EOF
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			body.pending = make([]byte, 0, len("data: ")+len(data)+2)
			body.pending = append(body.pending, "data: "...)
			body.pending = append(body.pending, data...)
			body.pending = append(body.pending, '\n', '\n')
		}
	}

	written := copy(destination, body.pending)
	body.pending = body.pending[written:]
	return written, nil
}

func (body *watchEventsBody) Close() error {
	body.ticker.Stop()
	body.subscription.Close()
	return nil
}

func cloudEventFilters(filters *nodeapi.WatchEventFilters) nodecloudevents.Filters {
	if filters == nil {
		return nodecloudevents.Filters{}
	}
	return nodecloudevents.Filters{
		TracerName:             optionalString(filters.TracerName),
		Hostname:               optionalString(filters.Hostname),
		ContainerHostname:      optionalString(filters.ContainerHostname),
		ContainerHostNamespace: optionalString(filters.ContainerHostNamespace),
		ContainerQoS:           optionalString(filters.ContainerQos),
		Region:                 optionalString(filters.Region),
	}
}
