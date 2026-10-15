package kafka

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrConsumerClosed = errors.New("billing/kafka: consumer has been terminated")
)

// ConsumerConfig tunes Kafka consumer throughput and backpressure thresholds.
type ConsumerConfig struct {
	Brokers            []string
	Topic              string
	GroupID            string
	WorkerPoolSize     int
	QueueCapacity      int
	BatchSize          int
	BackpressureLimit  float64 // 0.85 = pause ingestion when queue is 85% full
	PollIntervalMillis int
}

// IngestionHandler processes a batch of validated telemetry events.
type IngestionHandler interface {
	HandleEvents(ctx context.Context, events []*DatasetConsumptionEvent) error
}

// StreamConsumer manages resilient event stream consumption with backpressure regulation.
type StreamConsumer struct {
	cfg     ConsumerConfig
	handler IngestionHandler
	eventCh chan *DatasetConsumptionEvent
	stopCh  chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	paused  bool
}

// NewStreamConsumer instantiates the streaming consumer.
func NewStreamConsumer(cfg ConsumerConfig, handler IngestionHandler) *StreamConsumer {
	if cfg.WorkerPoolSize <= 0 {
		cfg.WorkerPoolSize = 8
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 10000
	}
	if cfg.BackpressureLimit <= 0 || cfg.BackpressureLimit > 1.0 {
		cfg.BackpressureLimit = 0.85
	}

	return &StreamConsumer{
		cfg:     cfg,
		handler: handler,
		eventCh: make(chan *DatasetConsumptionEvent, cfg.QueueCapacity),
		stopCh:  make(chan struct{}),
	}
}

// Start launches worker goroutines and backpressure monitor.
func (c *StreamConsumer) Start(ctx context.Context) {
	for i := 0; i < c.cfg.WorkerPoolSize; i++ {
		c.wg.Add(1)
		go c.workerLoop(ctx, i)
	}
}

// Enqueue passes an incoming event into the consumer's backpressure-monitored channel.
// Returns an error if backpressure is saturated or consumer stopped.
func (c *StreamConsumer) Enqueue(event *DatasetConsumptionEvent) error {
	select {
	case <-c.stopCh:
		return ErrConsumerClosed
	default:
	}

	// Backpressure check: queue capacity ratio
	queueLen := len(c.eventCh)
	ratio := float64(queueLen) / float64(c.cfg.QueueCapacity)

	if ratio >= c.cfg.BackpressureLimit {
		c.setPaused(true)
		// Non-blocking try with brief backoff
		select {
		case c.eventCh <- event:
			return nil
		case <-time.After(50 * time.Millisecond):
			return fmt.Errorf("billing/kafka: backpressure limit exceeded (saturation %.2f%%)", ratio*100)
		}
	} else if ratio < 0.50 {
		c.setPaused(false)
	}

	c.eventCh <- event
	return nil
}

func (c *StreamConsumer) setPaused(p bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused = p
}

// IsPaused returns current backpressure status.
func (c *StreamConsumer) IsPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

func (c *StreamConsumer) workerLoop(ctx context.Context, workerID int) {
	defer c.wg.Done()

	batch := make([]*DatasetConsumptionEvent, 0, c.cfg.BatchSize)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	flush := func() {
		if len(batch) > 0 {
			_ = c.handler.HandleEvents(ctx, batch)
			batch = batch[:0]
		}
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-c.stopCh:
			flush()
			return
		case ev, ok := <-c.eventCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= c.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// Stop initiates graceful drain and worker teardown.
func (c *StreamConsumer) Stop() {
	close(c.stopCh)
	close(c.eventCh)
	c.wg.Wait()
}
