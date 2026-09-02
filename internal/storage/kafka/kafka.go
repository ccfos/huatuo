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

// Package kafka implements a write-only storage backend for exporting tracing
// documents to Apache Kafka.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"

	"huatuo-bamai/internal/storage/driver"
)

const contentTypeJSON = "application/json"

type message struct {
	key     []byte
	value   []byte
	headers []header
}

type header struct {
	key   string
	value []byte
}

type producer interface {
	Write(ctx context.Context, topic string, msg message) error
	Close()
}

type franzProducer struct {
	client *kgo.Client
}

func (p *franzProducer) Write(ctx context.Context, topic string, msg message) error {
	record := &kgo.Record{Topic: topic, Key: msg.key, Value: msg.value}
	for _, h := range msg.headers {
		record.Headers = append(record.Headers, kgo.RecordHeader{Key: h.key, Value: h.value})
	}
	return p.client.ProduceSync(ctx, record).FirstErr()
}

func (p *franzProducer) Close() {
	p.client.Close()
}

// Storage exports one JSON document per Kafka record. The record ID is used as
// the message key so consumers can preserve HUATUO document identity.
type Storage struct {
	producer   producer
	topic      string
	collection string
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	driver.RegisterBackend("kafka", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(cfg.KafkaBrokers, cfg.KafkaTopic, cfg.KafkaClientID)
	})
}

// NewBackend creates a Kafka event sink.
func NewBackend(brokers []string, topic, clientID string) (*Storage, error) {
	if len(brokers) == 0 {
		return nil, errors.New("Kafka backend: at least one broker is required")
	}
	for _, broker := range brokers {
		if strings.TrimSpace(broker) == "" {
			return nil, errors.New("Kafka backend: broker must not be empty")
		}
	}
	if strings.TrimSpace(topic) == "" {
		return nil, errors.New("Kafka backend: topic must not be empty")
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("Kafka backend: client ID must not be empty")
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	)
	if err != nil {
		return nil, fmt.Errorf("Kafka backend: create client: %w", err)
	}
	return newStorage(&franzProducer{client: client}, topic), nil
}

func newStorage(producer producer, topic string) *Storage {
	return &Storage{producer: producer, topic: topic}
}

func (s *Storage) Init(_ context.Context, collection string, _ []driver.Index) error {
	if strings.TrimSpace(collection) == "" {
		return errors.New("Kafka backend: collection must not be empty")
	}
	s.collection = collection
	return nil
}

func (s *Storage) Save(ctx context.Context, rec driver.Record) error {
	if rec.ID == "" {
		return fmt.Errorf("%w: empty id", driver.ErrInvalidField)
	}
	if s.collection == "" {
		return errors.New("Kafka backend: not initialized")
	}

	headers := []header{
		{key: "content-type", value: []byte(contentTypeJSON)},
		{key: "huatuo-collection", value: []byte(s.collection)},
	}
	fieldNames := make([]string, 0, len(rec.Fields))
	for name, value := range rec.Fields {
		if _, ok := value.(string); ok {
			fieldNames = append(fieldNames, name)
		}
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		headers = append(headers, header{
			key:   "huatuo-" + strings.ReplaceAll(name, "_", "-"),
			value: []byte(rec.Fields[name].(string)),
		})
	}

	err := s.producer.Write(driver.WithContext(ctx), s.topic, message{
		key:     []byte(rec.ID),
		value:   driver.CloneBytes(rec.Data),
		headers: headers,
	})
	if err != nil {
		return fmt.Errorf("Kafka backend: write topic %q id %q: %w", s.topic, rec.ID, err)
	}
	return nil
}

func (s *Storage) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupported
}

func (s *Storage) Delete(context.Context, string) error {
	return driver.ErrUnsupported
}

func (s *Storage) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, driver.ErrUnsupported
}

func (s *Storage) Count(context.Context, driver.Query) (int64, error) {
	return 0, driver.ErrUnsupported
}

func (s *Storage) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, driver.ErrUnsupported
}

func (s *Storage) Close(_ context.Context) error {
	if s.producer != nil {
		s.producer.Close()
	}
	return nil
}
