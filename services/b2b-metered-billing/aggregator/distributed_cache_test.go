package aggregator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"datavendor-payment-core/services/b2b-metered-billing/kafka"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------
// 1. Ring Buffer Unit Tests
// ---------------------------------------------------------------------

func TestRingBuffer_BasicOperations(t *testing.T) {
	rb := NewTelemetryRingBuffer(5, OverflowPolicyReject)
	if rb.Cap() != 5 {
		t.Fatalf("expected capacity 5, got %d", rb.Cap())
	}
	if rb.Len() != 0 {
		t.Fatalf("expected initial len 0, got %d", rb.Len())
	}

	tp := TopicPartition{Topic: "telemetry", Partition: 0}
	for i := int64(1); i <= 3; i++ {
		ev := &kafka.DatasetConsumptionEvent{
			EventID:          uuid.New(),
			TenantID:         uuid.New(),
			DatasetID:        "ds-alpha",
			BytesTransferred: i * 1024,
		}
		item := &RingBufferItem{
			Event:          ev,
			TopicPartition: tp,
			Offset:         i,
			EnqueuedAt:     time.Now(),
		}
		if err := rb.Push(item); err != nil {
			t.Fatalf("unexpected error pushing item %d: %v", i, err)
		}
	}

	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}

	// Drain 2 items
	batch := rb.Drain(2)
	if len(batch) != 2 {
		t.Fatalf("expected 2 drained items, got %d", len(batch))
	}
	if batch[0].Offset != 1 || batch[1].Offset != 2 {
		t.Fatalf("unexpected FIFO order: offsets %d, %d", batch[0].Offset, batch[1].Offset)
	}
	if rb.Len() != 1 {
		t.Fatalf("expected remaining len 1, got %d", rb.Len())
	}

	// Drain remaining
	all := rb.DrainAll()
	if len(all) != 1 || all[0].Offset != 3 {
		t.Fatalf("expected 1 remaining item with offset 3, got %v", all)
	}
	if rb.Len() != 0 {
		t.Fatalf("expected empty buffer, got len %d", rb.Len())
	}

	pushed, drained, dropped := rb.Stats()
	if pushed != 3 || drained != 3 || dropped != 0 {
		t.Fatalf("unexpected stats: pushed=%d, drained=%d, dropped=%d", pushed, drained, dropped)
	}
}

func TestRingBuffer_OverflowPolicyReject(t *testing.T) {
	rb := NewTelemetryRingBuffer(2, OverflowPolicyReject)
	tp := TopicPartition{Topic: "telemetry", Partition: 0}

	item1 := &RingBufferItem{TopicPartition: tp, Offset: 1}
	item2 := &RingBufferItem{TopicPartition: tp, Offset: 2}
	item3 := &RingBufferItem{TopicPartition: tp, Offset: 3}

	if err := rb.Push(item1); err != nil {
		t.Fatalf("push item1 failed: %v", err)
	}
	if err := rb.Push(item2); err != nil {
		t.Fatalf("push item2 failed: %v", err)
	}

	err := rb.Push(item3)
	if !errors.Is(err, ErrRingBufferFull) {
		t.Fatalf("expected ErrRingBufferFull, got %v", err)
	}

	if rb.Len() != 2 {
		t.Fatalf("expected len 2, got %d", rb.Len())
	}
}

func TestRingBuffer_OverflowPolicyDropOldest(t *testing.T) {
	rb := NewTelemetryRingBuffer(3, OverflowPolicyDropOldest)
	tp := TopicPartition{Topic: "telemetry", Partition: 0}

	for i := int64(1); i <= 5; i++ {
		item := &RingBufferItem{TopicPartition: tp, Offset: i}
		if err := rb.Push(item); err != nil {
			t.Fatalf("push item %d failed: %v", i, err)
		}
	}

	if rb.Len() != 3 {
		t.Fatalf("expected len 3 after overflow drops, got %d", rb.Len())
	}

	pushed, drained, dropped := rb.Stats()
	if pushed != 5 || dropped != 2 || drained != 0 {
		t.Fatalf("unexpected stats: pushed=%d, dropped=%d, drained=%d", pushed, dropped, drained)
	}

	// Should contain offsets 3, 4, 5
	items := rb.DrainAll()
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	for idx, expectedOffset := range []int64{3, 4, 5} {
		if items[idx].Offset != expectedOffset {
			t.Fatalf("index %d: expected offset %d, got %d", idx, expectedOffset, items[idx].Offset)
		}
	}
}

func TestRingBuffer_ConcurrentPushDrain(t *testing.T) {
	rb := NewTelemetryRingBuffer(5000, OverflowPolicyReject)
	const numProducers = 10
	const itemsPerProducer = 200
	var wg sync.WaitGroup

	tp := TopicPartition{Topic: "telemetry", Partition: 1}

	// Launch producers
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		producerID := p
		go func() {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				ev := &kafka.DatasetConsumptionEvent{
					EventID:          uuid.New(),
					TenantID:         uuid.New(),
					DatasetID:        "ds-concurrent",
					BytesTransferred: 100,
				}
				item := &RingBufferItem{
					Event:          ev,
					TopicPartition: tp,
					Offset:         int64(producerID*itemsPerProducer + i),
				}
				for {
					if err := rb.Push(item); err == nil {
						break
					}
					time.Sleep(100 * time.Microsecond)
				}
			}
		}()
	}

	// Launch concurrent drainer
	var drainedCount int64
	doneCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-doneCh:
				batch := rb.DrainAll()
				drainedCount += int64(len(batch))
				return
			default:
				batch := rb.Drain(50)
				drainedCount += int64(len(batch))
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()

	wg.Wait()
	close(doneCh)
	time.Sleep(5 * time.Millisecond)

	expectedTotal := int64(numProducers * itemsPerProducer)
	if drainedCount != expectedTotal {
		t.Fatalf("expected %d total drained items, got %d", expectedTotal, drainedCount)
	}
}

// ---------------------------------------------------------------------
// 2. Sliding Window Accumulator Unit Tests
// ---------------------------------------------------------------------

func TestSlidingWindow_BucketingAndExpiry(t *testing.T) {
	tenantID := uuid.New()
	datasetID := "realtime-financial-ticks"
	windowDur := 1 * time.Second
	bucketDur := 200 * time.Millisecond

	sw := NewSlidingWindow(tenantID, datasetID, windowDur, bucketDur)

	baseTime := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	// Record event at baseTime
	ev1 := &kafka.DatasetConsumptionEvent{
		TenantID:            tenantID,
		DatasetID:           datasetID,
		BytesTransferred:    5000,
		QueryCount:          10,
		ComputeMilliseconds: 25,
		Timestamp:           baseTime,
	}
	sw.Record(ev1)

	// Record event at baseTime + 400ms
	ev2 := &kafka.DatasetConsumptionEvent{
		TenantID:            tenantID,
		DatasetID:           datasetID,
		BytesTransferred:    3000,
		QueryCount:          5,
		ComputeMilliseconds: 15,
		Timestamp:           baseTime.Add(400 * time.Millisecond),
	}
	sw.Record(ev2)

	// Check metrics evaluated at baseTime + 500ms
	m := sw.GetMetrics(baseTime.Add(500 * time.Millisecond))
	if m.TotalBytes != 8000 {
		t.Fatalf("expected 8000 total bytes, got %d", m.TotalBytes)
	}
	if m.TotalQueries != 15 {
		t.Fatalf("expected 15 queries, got %d", m.TotalQueries)
	}
	if m.TotalComputeMs != 40 {
		t.Fatalf("expected 40 compute ms, got %d", m.TotalComputeMs)
	}
	if m.EventCount != 2 {
		t.Fatalf("expected event count 2, got %d", m.EventCount)
	}

	// Advance time past the 1s window (baseTime + 1300ms)
	// ev1 (at baseTime) bucket should now be expired
	sw.PruneExpired(baseTime.Add(1300 * time.Millisecond))
	mAfter := sw.GetMetrics(baseTime.Add(1300 * time.Millisecond))

	if mAfter.TotalBytes != 3000 {
		t.Fatalf("expected 3000 total bytes after ev1 expired, got %d", mAfter.TotalBytes)
	}
	if mAfter.EventCount != 1 {
		t.Fatalf("expected 1 event after expiry, got %d", mAfter.EventCount)
	}
}

func TestSlidingWindowAccumulator_MultiTenantIsolation(t *testing.T) {
	swa := NewSlidingWindowAccumulator(10*time.Minute, 1*time.Minute)

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Ingest for Tenant A, Dataset 1
	swa.Record(&kafka.DatasetConsumptionEvent{
		TenantID:         tenantA,
		DatasetID:        "nasdaq-l2",
		BytesTransferred: 10000,
		QueryCount:       100,
		Timestamp:        time.Now().UTC(),
	})

	// Ingest for Tenant A, Dataset 2
	swa.Record(&kafka.DatasetConsumptionEvent{
		TenantID:         tenantA,
		DatasetID:        "fx-spot",
		BytesTransferred: 5000,
		QueryCount:       50,
		Timestamp:        time.Now().UTC(),
	})

	// Ingest for Tenant B, Dataset 1
	swa.Record(&kafka.DatasetConsumptionEvent{
		TenantID:         tenantB,
		DatasetID:        "nasdaq-l2",
		BytesTransferred: 20000,
		QueryCount:       200,
		Timestamp:        time.Now().UTC(),
	})

	// Assert Tenant A metrics
	mA1 := swa.GetMetrics(tenantA, "nasdaq-l2")
	if mA1.TotalBytes != 10000 || mA1.TotalQueries != 100 {
		t.Fatalf("unexpected tenantA ds1 metrics: %+v", mA1)
	}

	mA2 := swa.GetMetrics(tenantA, "fx-spot")
	if mA2.TotalBytes != 5000 || mA2.TotalQueries != 50 {
		t.Fatalf("unexpected tenantA ds2 metrics: %+v", mA2)
	}

	// Total bytes for Tenant A across all datasets
	totA := swa.GetTenantTotalBytes(tenantA)
	if totA != 15000 {
		t.Fatalf("expected 15000 total bytes for tenant A, got %d", totA)
	}

	// Assert Tenant B metrics
	mB1 := swa.GetMetrics(tenantB, "nasdaq-l2")
	if mB1.TotalBytes != 20000 || mB1.TotalQueries != 200 {
		t.Fatalf("unexpected tenantB ds1 metrics: %+v", mB1)
	}
	totB := swa.GetTenantTotalBytes(tenantB)
	if totB != 20000 {
		t.Fatalf("expected 20000 total bytes for tenant B, got %d", totB)
	}
}

func TestSlidingWindow_DeltaFlush(t *testing.T) {
	swa := NewSlidingWindowAccumulator(5*time.Minute, 30*time.Second)
	tenantID := uuid.New()

	swa.Record(&kafka.DatasetConsumptionEvent{
		TenantID:            tenantID,
		DatasetID:           "weather-radar",
		BytesTransferred:    4096,
		QueryCount:          4,
		ComputeMilliseconds: 12,
		Timestamp:           time.Now().UTC(),
	})

	deltas := swa.CollectDeltas()
	if len(deltas) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(deltas))
	}
	d := deltas[0]
	if d.DeltaBytes != 4096 || d.DeltaQueries != 4 || d.DeltaComputeMs != 12 || d.DeltaEventCount != 1 {
		t.Fatalf("unexpected delta contents: %+v", d)
	}

	// Second collection without new events should yield 0 deltas
	deltas2 := swa.CollectDeltas()
	if len(deltas2) != 0 {
		t.Fatalf("expected 0 deltas on second collection, got %d", len(deltas2))
	}
}

// ---------------------------------------------------------------------
// 3. Redis Cluster Sync Abstraction Unit Tests
// ---------------------------------------------------------------------

func TestInMemoryRedisCluster_Operations(t *testing.T) {
	ctx := context.Background()
	client := NewInMemoryRedisClusterClient()

	// Test HIncrBy
	v, err := client.HIncrBy(ctx, "hash:test", "counter", 10)
	if err != nil || v != 10 {
		t.Fatalf("HIncrBy failed: v=%d, err=%v", v, err)
	}
	v, err = client.HIncrBy(ctx, "hash:test", "counter", 5)
	if err != nil || v != 15 {
		t.Fatalf("HIncrBy increment failed: v=%d, err=%v", v, err)
	}

	// Test HIncrByMulti
	err = client.HIncrByMulti(ctx, "hash:test", map[string]int64{
		"bytes":   1024,
		"queries": 2,
	})
	if err != nil {
		t.Fatalf("HIncrByMulti failed: %v", err)
	}

	data, err := client.HGetAll(ctx, "hash:test")
	if err != nil {
		t.Fatalf("HGetAll failed: %v", err)
	}
	if data["counter"] != "15" || data["bytes"] != "1024" || data["queries"] != "2" {
		t.Fatalf("unexpected hash data: %v", data)
	}

	// Test SetNX (Locking)
	ok, err := client.SetNX(ctx, "lock:partition:0", "node-1", 500*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("SetNX failed to acquire lock: ok=%v, err=%v", ok, err)
	}
	// Second attempt should fail
	ok, err = client.SetNX(ctx, "lock:partition:0", "node-2", 500*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("SetNX second attempt should fail: ok=%v, err=%v", ok, err)
	}

	// Release lock via Del
	delCount, err := client.Del(ctx, "lock:partition:0")
	if err != nil || delCount != 1 {
		t.Fatalf("Del failed: delCount=%d, err=%v", delCount, err)
	}

	// Reacquire lock
	ok, err = client.SetNX(ctx, "lock:partition:0", "node-2", 500*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("SetNX failed after Del: ok=%v, err=%v", ok, err)
	}
}

func TestRedisSyncManager_SyncAndFetch(t *testing.T) {
	ctx := context.Background()
	client := NewInMemoryRedisClusterClient()
	rsm := NewRedisSyncManager(client, "test:metered:")

	tenantID := uuid.New()
	datasetID := "crypto-orderbook"

	deltas := []*WindowDelta{
		{
			TenantID:        tenantID,
			DatasetID:       datasetID,
			DeltaBytes:      50000,
			DeltaQueries:    20,
			DeltaComputeMs:  150,
			DeltaEventCount: 5,
		},
		{
			TenantID:        tenantID,
			DatasetID:       datasetID,
			DeltaBytes:      25000,
			DeltaQueries:    10,
			DeltaComputeMs:  75,
			DeltaEventCount: 3,
		},
	}

	if err := rsm.SyncDeltas(ctx, deltas); err != nil {
		t.Fatalf("SyncDeltas failed: %v", err)
	}

	usage, err := rsm.FetchClusterUsage(ctx, tenantID, datasetID)
	if err != nil {
		t.Fatalf("FetchClusterUsage failed: %v", err)
	}

	if usage.TotalBytes != 75000 {
		t.Fatalf("expected 75000 bytes, got %d", usage.TotalBytes)
	}
	if usage.TotalQueries != 30 {
		t.Fatalf("expected 30 queries, got %d", usage.TotalQueries)
	}
	if usage.TotalComputeMs != 225 {
		t.Fatalf("expected 225 compute ms, got %d", usage.TotalComputeMs)
	}
	if usage.EventCount != 8 {
		t.Fatalf("expected 8 events, got %d", usage.EventCount)
	}

	// Test Fault Injection
	client.SetFault(errors.New("simulated network partition"))
	err = rsm.SyncDeltas(ctx, deltas)
	if !errors.Is(err, ErrSyncFailed) {
		t.Fatalf("expected ErrSyncFailed upon Redis fault, got %v", err)
	}
	client.SetFault(nil) // clear fault
}

func TestRedisSyncManager_PartitionLocking(t *testing.T) {
	ctx := context.Background()
	client := NewInMemoryRedisClusterClient()
	rsm := NewRedisSyncManager(client, "test:metered:")

	tp := TopicPartition{Topic: "billing-events", Partition: 3}
	nodeA := "worker-node-a"
	nodeB := "worker-node-b"

	// Node A acquires
	acquired, err := rsm.AcquirePartitionLock(ctx, tp, nodeA, 2*time.Second)
	if err != nil || !acquired {
		t.Fatalf("nodeA failed to acquire lock: acquired=%v, err=%v", acquired, err)
	}

	// Node B attempts to acquire
	acquired, err = rsm.AcquirePartitionLock(ctx, tp, nodeB, 2*time.Second)
	if err != nil || acquired {
		t.Fatalf("nodeB should not acquire locked partition: acquired=%v", acquired)
	}

	// Node B attempts to release Node A's lock (should not release)
	_ = rsm.ReleasePartitionLock(ctx, tp, nodeB)
	acquired, _ = rsm.AcquirePartitionLock(ctx, tp, nodeB, 2*time.Second)
	if acquired {
		t.Fatalf("nodeB should not acquire lock released by unauthorized node")
	}

	// Node A releases
	if err := rsm.ReleasePartitionLock(ctx, tp, nodeA); err != nil {
		t.Fatalf("nodeA failed to release lock: %v", err)
	}

	// Node B now acquires
	acquired, err = rsm.AcquirePartitionLock(ctx, tp, nodeB, 2*time.Second)
	if err != nil || !acquired {
		t.Fatalf("nodeB failed to acquire lock after release: %v", err)
	}
}

// ---------------------------------------------------------------------
// 4. Partition Assignment & Exactly-Once Coordination Tests
// ---------------------------------------------------------------------

func TestPartitionCoordinator_LifecycleAndIdempotency(t *testing.T) {
	redisClient := NewInMemoryRedisClusterClient()
	rsm := NewRedisSyncManager(redisClient, "test:coord:")
	committer := NewMockKafkaOffsetCommitter()

	pc := NewPartitionCoordinator("test-node-1", rsm, committer, 5*time.Minute)

	tp0 := TopicPartition{Topic: "telemetry", Partition: 0}
	tp1 := TopicPartition{Topic: "telemetry", Partition: 1}

	// Initial assignment
	pc.OnPartitionsAssigned([]TopicPartition{tp0, tp1})

	if !pc.IsAssigned(tp0) || !pc.IsAssigned(tp1) {
		t.Fatalf("expected partitions to be assigned")
	}

	tpUnassigned := TopicPartition{Topic: "telemetry", Partition: 99}
	if pc.IsAssigned(tpUnassigned) {
		t.Fatalf("unassigned partition reported as assigned")
	}

	// Ingest event with idempotency key
	ev1 := &kafka.DatasetConsumptionEvent{
		EventID:          uuid.New(),
		TenantID:         uuid.New(),
		DatasetID:        "ds-1",
		BytesTransferred: 1024,
		IdempotencyKey:   "idemp-key-001",
	}

	if err := pc.ValidateAndTrackEvent(tp0, 100, ev1); err != nil {
		t.Fatalf("failed to validate and track event: %v", err)
	}

	// Ingest duplicate event with same idempotency key
	err := pc.ValidateAndTrackEvent(tp0, 101, ev1)
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("expected ErrDuplicateEvent, got %v", err)
	}

	// Event on unassigned partition
	err = pc.ValidateAndTrackEvent(tpUnassigned, 50, ev1)
	if !errors.Is(err, ErrPartitionNotAssigned) {
		t.Fatalf("expected ErrPartitionNotAssigned, got %v", err)
	}

	meta, ok := pc.GetPartitionMetadata(tp0)
	if !ok || meta.HighWatermark != 100 || meta.ProcessedCount != 1 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestPartitionCoordinator_ExactlyOnceCommit(t *testing.T) {
	ctx := context.Background()
	redisClient := NewInMemoryRedisClusterClient()
	rsm := NewRedisSyncManager(redisClient, "test:commit:")
	committer := NewMockKafkaOffsetCommitter()

	pc := NewPartitionCoordinator("node-commit-test", rsm, committer, 5*time.Minute)

	tp := TopicPartition{Topic: "events-stream", Partition: 2}
	pc.OnPartitionsAssigned([]TopicPartition{tp})

	// Process events advancing offsets 100 -> 101 -> 102
	for i := int64(100); i <= 102; i++ {
		ev := &kafka.DatasetConsumptionEvent{
			EventID:          uuid.New(),
			TenantID:         uuid.New(),
			DatasetID:        "ds-events",
			IdempotencyKey:   fmt.Sprintf("key-%d", i),
			BytesTransferred: 500,
		}
		if err := pc.ValidateAndTrackEvent(tp, i, ev); err != nil {
			t.Fatalf("failed event tracking at offset %d: %v", i, err)
		}
	}

	// Commit partition
	committedOffset, err := pc.CommitPartition(ctx, tp)
	if err != nil {
		t.Fatalf("CommitPartition failed: %v", err)
	}
	if committedOffset != 102 {
		t.Fatalf("expected committed offset 102, got %d", committedOffset)
	}

	// Check Redis offset checkpoint
	redisOffset, err := rsm.GetCommittedOffset(ctx, tp)
	if err != nil || redisOffset != 102 {
		t.Fatalf("expected redis committed offset 102, got %d (err: %v)", redisOffset, err)
	}

	// Check Kafka committer
	kafkaOffset, exists := committer.GetCommitted(tp)
	if !exists || kafkaOffset != 102 {
		t.Fatalf("expected kafka committer offset 102, got %d (exists: %v)", kafkaOffset, exists)
	}

	// Repeated commit with no new events should return 102 without error
	repeatOffset, err := pc.CommitPartition(ctx, tp)
	if err != nil || repeatOffset != 102 {
		t.Fatalf("repeated commit failed: offset=%d, err=%v", repeatOffset, err)
	}
}

// ---------------------------------------------------------------------
// 5. Distributed Cache Coordinator End-to-End Pipeline
// ---------------------------------------------------------------------

func TestDistributedCacheCoordinator_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisClient := NewInMemoryRedisClusterClient()
	committer := NewMockKafkaOffsetCommitter()

	cfg := DistributedCacheConfig{
		NodeID:                "test-worker-01",
		RingBufferSize:        1000,
		RingBufferPolicy:      OverflowPolicyReject,
		FlushInterval:         20 * time.Millisecond,
		FlushBatchSize:        50,
		SlidingWindowDuration: 5 * time.Second,
		BucketGranularity:     100 * time.Millisecond,
		RedisSyncInterval:     50 * time.Millisecond,
		IdempotencyWindow:     5 * time.Minute,
		AutoCommitInterval:    100 * time.Millisecond,
	}

	coordinator := NewDistributedCacheCoordinator(cfg, redisClient, committer)

	if err := coordinator.Start(ctx); err != nil {
		t.Fatalf("failed to start coordinator: %v", err)
	}

	tp := TopicPartition{Topic: "b2b-telemetry", Partition: 0}
	_ = coordinator.OnPartitionsAssigned([]TopicPartition{tp})

	tenantID := uuid.New()
	datasetID := "satellite-imagery-tiles"

	// Ingest 10 events
	for i := int64(1); i <= 10; i++ {
		ev := &kafka.DatasetConsumptionEvent{
			EventID:             uuid.New(),
			TenantID:            tenantID,
			DatasetID:           datasetID,
			BytesTransferred:    1024 * i,
			QueryCount:          1,
			ComputeMilliseconds: 5,
			Timestamp:           time.Now().UTC(),
			IdempotencyKey:      fmt.Sprintf("sat-tile-%d", i),
		}
		if err := coordinator.Ingest(tp, i, ev); err != nil {
			t.Fatalf("failed to ingest event %d: %v", i, err)
		}
	}

	// Allow flushLoop and syncLoop to process
	time.Sleep(150 * time.Millisecond)

	// Check local sliding window usage
	windowMetrics := coordinator.GetWindowUsage(tenantID, datasetID)
	expectedBytes := int64(0)
	for i := int64(1); i <= 10; i++ {
		expectedBytes += 1024 * i
	}
	if windowMetrics.TotalBytes != expectedBytes {
		t.Fatalf("expected %d sliding window bytes, got %d", expectedBytes, windowMetrics.TotalBytes)
	}
	if windowMetrics.EventCount != 10 {
		t.Fatalf("expected 10 events, got %d", windowMetrics.EventCount)
	}

	// Check cluster usage persisted in Redis
	clusterUsage, err := coordinator.GetClusterUsage(ctx, tenantID, datasetID)
	if err != nil {
		t.Fatalf("GetClusterUsage failed: %v", err)
	}
	if clusterUsage.TotalBytes != expectedBytes {
		t.Fatalf("expected %d cluster bytes in Redis, got %d", expectedBytes, clusterUsage.TotalBytes)
	}

	// Test Partition Revocation (flush & commit)
	if err := coordinator.OnPartitionsRevoked([]TopicPartition{tp}); err != nil {
		t.Fatalf("OnPartitionsRevoked failed: %v", err)
	}

	// Verify offset 10 was committed
	committedKafka, exists := committer.GetCommitted(tp)
	if !exists || committedKafka != 10 {
		t.Fatalf("expected committed kafka offset 10 upon revocation, got %d", committedKafka)
	}

	// Verify ring buffer stats
	pushed, drained, dropped, count := coordinator.GetRingBufferStats()
	if pushed != 10 || drained != 10 || dropped != 0 || count != 0 {
		t.Fatalf("unexpected buffer stats: pushed=%d, drained=%d, dropped=%d, count=%d",
			pushed, drained, dropped, count)
	}

	// Graceful shutdown
	if err := coordinator.Stop(); err != nil {
		t.Fatalf("coordinator.Stop failed: %v", err)
	}
}

// ---------------------------------------------------------------------
// 6. Concurrency & High-Throughput Stress Tests
// ---------------------------------------------------------------------

func TestDistributedCacheCoordinator_HighConcurrencyStress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisClient := NewInMemoryRedisClusterClient()
	committer := NewMockKafkaOffsetCommitter()

	cfg := DistributedCacheConfig{
		NodeID:                "stress-worker-01",
		RingBufferSize:        20000,
		RingBufferPolicy:      OverflowPolicyReject,
		FlushInterval:         10 * time.Millisecond,
		FlushBatchSize:        100,
		SlidingWindowDuration: 10 * time.Minute,
		BucketGranularity:     1 * time.Second,
		RedisSyncInterval:     25 * time.Millisecond,
		IdempotencyWindow:     5 * time.Minute,
		AutoCommitInterval:    50 * time.Millisecond,
	}

	coordinator := NewDistributedCacheCoordinator(cfg, redisClient, committer)
	_ = coordinator.Start(ctx)

	const numPartitions = 4
	partitions := make([]TopicPartition, numPartitions)
	for p := 0; p < numPartitions; p++ {
		partitions[p] = TopicPartition{Topic: "stress-telemetry", Partition: int32(p)}
	}
	_ = coordinator.OnPartitionsAssigned(partitions)

	const numWorkers = 20
	const eventsPerWorker = 250
	var wg sync.WaitGroup

	tenants := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	datasets := []string{"dataset-A", "dataset-B", "dataset-C"}

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		workerID := w
		go func() {
			defer wg.Done()
			tp := partitions[workerID%numPartitions]
			tID := tenants[workerID%len(tenants)]
			dID := datasets[workerID%len(datasets)]

			for i := 0; i < eventsPerWorker; i++ {
				offset := int64(workerID*eventsPerWorker + i)
				ev := &kafka.DatasetConsumptionEvent{
					EventID:             uuid.New(),
					TenantID:            tID,
					DatasetID:           dID,
					BytesTransferred:    100,
					QueryCount:          1,
					ComputeMilliseconds: 2,
					Timestamp:           time.Now().UTC(),
					IdempotencyKey:      fmt.Sprintf("stress-%d-%d", workerID, i),
				}

				for {
					if err := coordinator.Ingest(tp, offset, ev); err == nil {
						break
					}
					time.Sleep(50 * time.Microsecond)
				}
			}
		}()
	}

	// Concurrent readers querying metrics
	stopReaders := make(chan struct{})
	var readerWg sync.WaitGroup
	for r := 0; r < 5; r++ {
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					for _, tID := range tenants {
						for _, dID := range datasets {
							_ = coordinator.GetWindowUsage(tID, dID)
						}
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()
	close(stopReaders)
	readerWg.Wait()

	// Stop coordinator to perform final drain and sync
	_ = coordinator.Stop()

	expectedTotalEvents := int64(numWorkers * eventsPerWorker)
	pushed, drained, _, _ := coordinator.GetRingBufferStats()
	if int64(pushed) != expectedTotalEvents {
		t.Fatalf("expected %d pushed events, got %d", expectedTotalEvents, pushed)
	}
	if int64(drained) != expectedTotalEvents {
		t.Fatalf("expected %d drained events, got %d", expectedTotalEvents, drained)
	}
}
