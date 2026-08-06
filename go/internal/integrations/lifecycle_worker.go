package integrations

import (
	"context"
	"fmt"
	"time"
)

const (
	defaultLifecycleAttempts = 5
	defaultLifecycleBackoff  = 250 * time.Millisecond
	maxLifecycleBackoff      = 2 * time.Second
)

type lifecycleConsumer interface {
	Commit(context.Context, []KafkaRecord) error
}

type lifecycleDispatcher interface {
	Dispatch(context.Context, KafkaRecord) error
}

// LifecycleWorker provides the at-least-once boundary between lifecycle
// dispatch and Kafka offset commits. A failed batch is never committed; after the
// bounded retries are exhausted the caller must stop consuming so a restart can
// resume from the last committed offsets.
type LifecycleWorker struct {
	consumer   lifecycleConsumer
	dispatcher lifecycleDispatcher
	attempts   int
	backoff    time.Duration
	maxBackoff time.Duration
	wait       func(context.Context, time.Duration) error
}

func NewLifecycleWorker(consumer lifecycleConsumer, dispatcher lifecycleDispatcher) *LifecycleWorker {
	return &LifecycleWorker{
		consumer:   consumer,
		dispatcher: dispatcher,
		attempts:   defaultLifecycleAttempts,
		backoff:    defaultLifecycleBackoff,
		maxBackoff: maxLifecycleBackoff,
		wait:       waitForRetry,
	}
}

// ProcessBatch dispatches records in poll order, retrying only the record that
// failed. It commits the partition offsets once, and only once, after every record
// in the batch has succeeded. A commit retry never redispatches the batch.
func (w *LifecycleWorker) ProcessBatch(ctx context.Context, records []KafkaRecord) error {
	if len(records) == 0 {
		return nil
	}
	for _, record := range records {
		operation := fmt.Sprintf("dispatch topic=%s partition=%d offset=%d", record.Topic, record.Partition, record.Offset)
		if err := w.retry(ctx, operation, func() error {
			return w.dispatcher.Dispatch(ctx, record)
		}); err != nil {
			return err
		}
	}
	if err := w.retry(ctx, "commit Kafka lifecycle batch", func() error {
		return w.consumer.Commit(ctx, records)
	}); err != nil {
		return err
	}
	return nil
}

func (w *LifecycleWorker) retry(ctx context.Context, operation string, action func() error) error {
	attempts := w.attempts
	if attempts < 1 {
		attempts = 1
	}
	backoff := w.backoff
	if backoff < 0 {
		backoff = 0
	}
	maximum := w.maxBackoff
	if maximum < backoff {
		maximum = backoff
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := action(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == attempts {
			break
		}
		if err := w.wait(ctx, backoff); err != nil {
			return err
		}
		if backoff < maximum {
			backoff *= 2
			if backoff > maximum {
				backoff = maximum
			}
		}
	}
	return fmt.Errorf("%s failed after %d attempts: %w", operation, attempts, lastErr)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
