package integrations

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type workerConsumer struct {
	commitCalls int
	failures    int
	events      *[]string
}

func (c *workerConsumer) Commit(_ context.Context, _ []KafkaRecord) error {
	c.commitCalls++
	*c.events = append(*c.events, "commit")
	if c.failures > 0 {
		c.failures--
		return errors.New("commit unavailable")
	}
	return nil
}

type workerDispatcher struct {
	calls      map[int64]int
	failOffset int64
	failures   int
	alwaysFail bool
	events     *[]string
}

func (d *workerDispatcher) Dispatch(_ context.Context, record KafkaRecord) error {
	d.calls[record.Offset]++
	*d.events = append(*d.events, "dispatch:"+recordLabel(record.Offset))
	if record.Offset == d.failOffset && (d.alwaysFail || d.failures > 0) {
		if d.failures > 0 {
			d.failures--
		}
		return errors.New("dispatch unavailable")
	}
	return nil
}

func TestLifecycleWorkerCommitsOnlyAfterWholeBatchDispatches(t *testing.T) {
	events := []string{}
	consumer := &workerConsumer{events: &events}
	dispatcher := &workerDispatcher{calls: map[int64]int{}, failOffset: 2, failures: 2, events: &events}
	worker, delays := testLifecycleWorker(consumer, dispatcher, 3)
	records := lifecycleRecords()

	if err := worker.ProcessBatch(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"dispatch:1", "dispatch:2", "dispatch:2", "dispatch:2", "dispatch:3", "commit"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	if consumer.commitCalls != 1 {
		t.Fatalf("commit calls = %d, want 1", consumer.commitCalls)
	}
	if want := []time.Duration{time.Millisecond, 2 * time.Millisecond}; !reflect.DeepEqual(*delays, want) {
		t.Fatalf("retry delays = %#v, want %#v", *delays, want)
	}
}

func TestLifecycleWorkerDoesNotCommitFailedBatch(t *testing.T) {
	events := []string{}
	consumer := &workerConsumer{events: &events}
	dispatcher := &workerDispatcher{calls: map[int64]int{}, failOffset: 2, alwaysFail: true, events: &events}
	worker, delays := testLifecycleWorker(consumer, dispatcher, 5)

	err := worker.ProcessBatch(context.Background(), lifecycleRecords())
	if err == nil || !strings.Contains(err.Error(), "failed after 5 attempts") {
		t.Fatalf("expected bounded dispatch failure, got %v", err)
	}
	if consumer.commitCalls != 0 {
		t.Fatalf("failed batch must not commit, got %d calls", consumer.commitCalls)
	}
	if dispatcher.calls[1] != 1 || dispatcher.calls[2] != 5 || dispatcher.calls[3] != 0 {
		t.Fatalf("unexpected dispatch calls: %#v", dispatcher.calls)
	}
	if want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}; !reflect.DeepEqual(*delays, want) {
		t.Fatalf("bounded/capped retry delays = %#v, want %#v", *delays, want)
	}
}

func TestLifecycleWorkerRetriesCommitWithoutRedispatch(t *testing.T) {
	events := []string{}
	consumer := &workerConsumer{failures: 2, events: &events}
	dispatcher := &workerDispatcher{calls: map[int64]int{}, events: &events}
	worker, delays := testLifecycleWorker(consumer, dispatcher, 3)

	if err := worker.ProcessBatch(context.Background(), lifecycleRecords()); err != nil {
		t.Fatal(err)
	}
	if consumer.commitCalls != 3 {
		t.Fatalf("commit calls = %d, want 3", consumer.commitCalls)
	}
	for _, offset := range []int64{1, 2, 3} {
		if dispatcher.calls[offset] != 1 {
			t.Fatalf("offset %d redispatched during commit retry: %#v", offset, dispatcher.calls)
		}
	}
	if want := []time.Duration{time.Millisecond, 2 * time.Millisecond}; !reflect.DeepEqual(*delays, want) {
		t.Fatalf("retry delays = %#v, want %#v", *delays, want)
	}
}

func TestLifecycleWorkerBoundsCommitFailure(t *testing.T) {
	events := []string{}
	consumer := &workerConsumer{failures: 4, events: &events}
	dispatcher := &workerDispatcher{calls: map[int64]int{}, events: &events}
	worker, delays := testLifecycleWorker(consumer, dispatcher, 3)

	err := worker.ProcessBatch(context.Background(), lifecycleRecords())
	if err == nil || !strings.Contains(err.Error(), "commit Kafka lifecycle batch failed after 3 attempts") {
		t.Fatalf("expected bounded commit failure, got %v", err)
	}
	if consumer.commitCalls != 3 || len(*delays) != 2 {
		t.Fatalf("commit retry was not bounded: calls=%d delays=%#v", consumer.commitCalls, *delays)
	}
}

func testLifecycleWorker(consumer lifecycleConsumer, dispatcher lifecycleDispatcher, attempts int) (*LifecycleWorker, *[]time.Duration) {
	worker := NewLifecycleWorker(consumer, dispatcher)
	worker.attempts = attempts
	worker.backoff = time.Millisecond
	worker.maxBackoff = 4 * time.Millisecond
	delays := []time.Duration{}
	worker.wait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	return worker, &delays
}

func lifecycleRecords() []KafkaRecord {
	return []KafkaRecord{
		{Topic: "agents", Partition: 0, Offset: 1},
		{Topic: "agents", Partition: 0, Offset: 2},
		{Topic: "agents", Partition: 0, Offset: 3},
	}
}

func recordLabel(offset int64) string {
	switch offset {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	default:
		return "other"
	}
}
