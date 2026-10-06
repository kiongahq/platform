package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type KafkaRecord struct {
	Topic     string          `json:"topic"`
	Key       json.RawMessage `json:"key"`
	Value     json.RawMessage `json:"value"`
	Partition int             `json:"partition"`
	Offset    int64           `json:"offset"`
}

type KafkaOffset struct {
	Topic     string `json:"topic"`
	Partition int    `json:"partition"`
	Offset    int64  `json:"offset"`
}

type KafkaConsumer struct {
	restURL  string
	baseURI  string
	group    string
	instance string
	client   *http.Client
	token    string
}

func NewKafkaConsumer(restURL, group, instance string, token ...string) *KafkaConsumer {
	value := ""
	if len(token) > 0 {
		value = token[0]
	}
	return &KafkaConsumer{restURL: strings.TrimRight(restURL, "/"), group: group, instance: instance, token: value, client: &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *KafkaConsumer) Connect(ctx context.Context, topics []string) error {
	payload := map[string]any{"name": c.instance, "format": "json", "auto.offset.reset": "earliest", "auto.commit.enable": "false"}
	var response struct {
		BaseURI string `json:"base_uri"`
	}
	if err := c.request(ctx, http.MethodPost, c.restURL+"/consumers/"+c.group, payload, &response); err != nil {
		return err
	}
	c.baseURI = response.BaseURI
	if c.baseURI == "" {
		return fmt.Errorf("Kafka REST consumer did not return base_uri")
	}
	return c.request(ctx, http.MethodPost, c.baseURI+"/subscription", map[string]any{"topics": topics}, nil)
}

func (c *KafkaConsumer) Poll(ctx context.Context) ([]KafkaRecord, error) {
	if c.baseURI == "" {
		return nil, fmt.Errorf("consumer is not connected")
	}
	var records []KafkaRecord
	err := c.request(ctx, http.MethodGet, c.baseURI+"/records", nil, &records)
	return records, err
}

// Commit advances each partition to the first offset after the successfully
// processed records. Callers must only invoke it after the complete poll batch has
// been dispatched; Kafka will redeliver the batch after a restart if this request
// does not succeed.
func (c *KafkaConsumer) Commit(ctx context.Context, records []KafkaRecord) error {
	if c.baseURI == "" {
		return fmt.Errorf("consumer is not connected")
	}
	offsets, err := nextOffsets(records)
	if err != nil {
		return err
	}
	if len(offsets) == 0 {
		return nil
	}
	return c.request(ctx, http.MethodPost, c.baseURI+"/offsets", map[string]any{"offsets": offsets}, nil)
}

func nextOffsets(records []KafkaRecord) ([]KafkaOffset, error) {
	type partition struct {
		topic string
		id    int
	}
	highest := make(map[partition]int64)
	for _, record := range records {
		if strings.TrimSpace(record.Topic) == "" {
			return nil, fmt.Errorf("Kafka record topic is required for offset commit")
		}
		if record.Partition < 0 || record.Offset < 0 {
			return nil, fmt.Errorf("Kafka record %s has invalid partition or offset", record.Topic)
		}
		key := partition{topic: record.Topic, id: record.Partition}
		if current, ok := highest[key]; !ok || record.Offset > current {
			highest[key] = record.Offset
		}
	}
	offsets := make([]KafkaOffset, 0, len(highest))
	for key, offset := range highest {
		offsets = append(offsets, KafkaOffset{Topic: key.topic, Partition: key.id, Offset: offset + 1})
	}
	sort.Slice(offsets, func(i, j int) bool {
		if offsets[i].Topic == offsets[j].Topic {
			return offsets[i].Partition < offsets[j].Partition
		}
		return offsets[i].Topic < offsets[j].Topic
	})
	return offsets, nil
}

func (c *KafkaConsumer) Close(ctx context.Context) error {
	if c.baseURI == "" {
		return nil
	}
	return c.request(ctx, http.MethodDelete, c.baseURI, nil, nil)
}

func (c *KafkaConsumer) request(ctx context.Context, method, endpoint string, input, output any) error {
	if c.token != "" {
		base, _ := url.Parse(c.restURL)
		destination, err := url.Parse(endpoint)
		if err != nil || base == nil || destination.Host != base.Host || destination.Scheme != base.Scheme {
			return fmt.Errorf("Kafka consumer endpoint must retain the configured origin")
		}
	}
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/vnd.kafka.v2+json")
	request.Header.Set("Accept", "application/vnd.kafka.json.v2+json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Kafka REST returned %d", response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}
