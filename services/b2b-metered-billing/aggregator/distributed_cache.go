package aggregator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"datavendor-payment-core/services/b2b-metered-billing/kafka"
	"github.com/google/uuid"
)

// Standard error definitions for distributed aggregation and cache coordination.
var (
	ErrRingBufferFull        = errors.New("billing/cache: local memory ring-buffer capacity exceeded")
	ErrPartitionNotAssigned  = errors.New("billing/cache: topic partition is not currently assigned to this worker node")
	ErrDuplicateEvent        = errors.New("billing/cache: duplicate consumption event detected; idempotency violation")
	ErrCoordinatorStopped    = errors.New("billing/cache: distributed cache coordinator is stopped")
	ErrSyncFailed            = errors.New("billing/cache: redis cluster synchronization failed")
	ErrInvalidOffset         = errors.New("billing/cache: invalid partition offset progression")
	ErrPartitionLockAcquire  = errors.New("billing/cache: failed to acquire distributed partition lock")
)

// ---------------------------------------------------------------------
// 1. Ring-Buffer & Flush Types
// ---------------------------------------------------------------------

// OverflowPolicy defines the eviction or rejection policy when the ring-buffer reaches capacity.
type OverflowPolicy int

const (
	// OverflowPolicyReject causes Push to return ErrRingBufferFull immediately.
	OverflowPolicyReject OverflowPolicy = iota
	// OverflowPolicyDropOldest overwrites the oldest element in the ring-buffer.
	OverflowPolicyDropOldest
)

// RingBufferItem wraps a validated consumption event with partition assignment metadata.
type RingBufferItem struct {
	Event          *kafka.DatasetConsumptionEvent
	TopicPartition TopicPartition
	Offset         int64
	EnqueuedAt     time.Time
}

// TelemetryRingBuffer is a circular, thread-safe memory buffer designed for sub-millisecond,
// high-throughput ingestion of streaming consumption events prior to sliding-window accumulation.
type TelemetryRingBuffer struct {
	mu       sync.RWMutex
	items    []*RingBufferItem
	capacity int
	head     int // points to the oldest item
	tail     int // points to the next insertion slot
	count    int
	policy   OverflowPolicy

	// Atomic telemetry statistics
	pushedCount  uint64
	drainedCount uint64
	droppedCount uint64
}

// NewTelemetryRingBuffer instantiates a ring buffer with the specified capacity and policy.
func NewTelemetryRingBuffer(capacity int, policy OverflowPolicy) *TelemetryRingBuffer {
	if capacity <= 0 {
		capacity = 16384
	}
	return &TelemetryRingBuffer{
		items:    make([]*RingBufferItem, capacity),
		capacity: capacity,
		policy:   policy,
	}
}

// Push adds a single item to the ring buffer.
func (rb *TelemetryRingBuffer) Push(item *RingBufferItem) error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count == rb.capacity {
		if rb.policy == OverflowPolicyReject {
			return ErrRingBufferFull
		}

		// OverflowPolicyDropOldest: advance head to discard the oldest item
		rb.head = (rb.head + 1) % rb.capacity
		rb.count--
		atomic.AddUint64(&rb.droppedCount, 1)
	}

	rb.items[rb.tail] = item
	rb.tail = (rb.tail + 1) % rb.capacity
	rb.count++
	atomic.AddUint64(&rb.pushedCount, 1)

	return nil
}

// PushBatch attempts to insert a batch of items. Returns number of items successfully queued.
func (rb *TelemetryRingBuffer) PushBatch(items []*RingBufferItem) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	pushed := 0
	for _, item := range items {
		if rb.count == rb.capacity {
			if rb.policy == OverflowPolicyReject {
				return pushed, ErrRingBufferFull
			}
			rb.head = (rb.head + 1) % rb.capacity
			rb.count--
			atomic.AddUint64(&rb.droppedCount, 1)
		}

		rb.items[rb.tail] = item
		rb.tail = (rb.tail + 1) % rb.capacity
		rb.count++
		atomic.AddUint64(&rb.pushedCount, 1)
		pushed++
	}

	return pushed, nil
}

// Drain extracts up to maxBatch items from the ring buffer in FIFO order.
func (rb *TelemetryRingBuffer) Drain(maxBatch int) []*RingBufferItem {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count == 0 {
		return nil
	}

	n := rb.count
	if maxBatch > 0 && maxBatch < n {
		n = maxBatch
	}

	batch := make([]*RingBufferItem, n)
	for i := 0; i < n; i++ {
		batch[i] = rb.items[rb.head]
		rb.items[rb.head] = nil // avoid memory retention
		rb.head = (rb.head + 1) % rb.capacity
	}

	rb.count -= n
	atomic.AddUint64(&rb.drainedCount, uint64(n))
	return batch
}

// DrainAll extracts all items currently buffered.
func (rb *TelemetryRingBuffer) DrainAll() []*RingBufferItem {
	return rb.Drain(0)
}

// Len returns the current number of buffered items.
func (rb *TelemetryRingBuffer) Len() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// Cap returns the maximum capacity of the ring buffer.
func (rb *TelemetryRingBuffer) Cap() int {
	return rb.capacity
}

// Stats returns cumulative counts for pushed, drained, and dropped items.
func (rb *TelemetryRingBuffer) Stats() (pushed, drained, dropped uint64) {
	return atomic.LoadUint64(&rb.pushedCount),
		atomic.LoadUint64(&rb.drainedCount),
		atomic.LoadUint64(&rb.droppedCount)
}

// ---------------------------------------------------------------------
// 2. Distributed Sliding-Window Telemetry Accumulator
// ---------------------------------------------------------------------

// WindowBucket holds discrete aggregated consumption metrics within a sub-second/multi-second bucket.
type WindowBucket struct {
	StartTime      time.Time
	EndTime        time.Time
	TotalBytes     int64
	TotalQueries   int64
	TotalComputeMs int64
	EventCount     int64
}

// SlidingWindowMetrics represents aggregated telemetry over the entire sliding window horizon.
type SlidingWindowMetrics struct {
	TenantID         uuid.UUID `json:"tenant_id"`
	DatasetID        string    `json:"dataset_id"`
	TotalBytes       int64     `json:"total_bytes"`
	TotalQueries     int64     `json:"total_queries"`
	TotalComputeMs   int64     `json:"total_compute_ms"`
	EventCount       int64     `json:"event_count"`
	WindowStart      time.Time `json:"window_start"`
	WindowEnd        time.Time `json:"window_end"`
	BytesPerSecond   float64   `json:"bytes_per_second"`
	QueriesPerSecond float64   `json:"queries_per_second"`
	ActiveBuckets    int       `json:"active_buckets"`
}

// WindowDelta stores incremental changes accumulated locally before being flushed to Redis.
type WindowDelta struct {
	TenantID         uuid.UUID
	DatasetID        string
	DeltaBytes       int64
	DeltaQueries     int64
	DeltaComputeMs   int64
	DeltaEventCount  int64
	LastEventTime    time.Time
}

// SlidingWindow maintains rolling time-bucketed aggregation for a single tenant + dataset pair.
type SlidingWindow struct {
	mu             sync.RWMutex
	tenantID       uuid.UUID
	datasetID      string
	windowDuration time.Duration
	bucketDuration time.Duration
	buckets        map[int64]*WindowBucket // epoch bucket index -> bucket
	unflushedDelta WindowDelta
}

// NewSlidingWindow instantiates a single sliding window.
func NewSlidingWindow(tenantID uuid.UUID, datasetID string, windowDuration, bucketDuration time.Duration) *SlidingWindow {
	if windowDuration <= 0 {
		windowDuration = 15 * time.Minute
	}
	if bucketDuration <= 0 {
		bucketDuration = 10 * time.Second
	}
	return &SlidingWindow{
		tenantID:       tenantID,
		datasetID:      datasetID,
		windowDuration: windowDuration,
		bucketDuration: bucketDuration,
		buckets:        make(map[int64]*WindowBucket),
		unflushedDelta: WindowDelta{
			TenantID:  tenantID,
			DatasetID: datasetID,
		},
	}
}

func (sw *SlidingWindow) bucketKey(t time.Time) int64 {
	return t.UnixNano() / int64(sw.bucketDuration)
}

// Record accumulates an event into the appropriate time bucket and updates the pending delta.
func (sw *SlidingWindow) Record(ev *kafka.DatasetConsumptionEvent) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	key := sw.bucketKey(ts)
	b, exists := sw.buckets[key]
	if !exists {
		bucketStart := ts.Truncate(sw.bucketDuration)
		b = &WindowBucket{
			StartTime: bucketStart,
			EndTime:   bucketStart.Add(sw.bucketDuration),
		}
		sw.buckets[key] = b
	}

	b.TotalBytes += ev.BytesTransferred
	b.TotalQueries += ev.QueryCount
	b.TotalComputeMs += ev.ComputeMilliseconds
	b.EventCount++

	// Record pending delta for distributed Redis cluster sync
	sw.unflushedDelta.DeltaBytes += ev.BytesTransferred
	sw.unflushedDelta.DeltaQueries += ev.QueryCount
	sw.unflushedDelta.DeltaComputeMs += ev.ComputeMilliseconds
	sw.unflushedDelta.DeltaEventCount++
	if ts.After(sw.unflushedDelta.LastEventTime) {
		sw.unflushedDelta.LastEventTime = ts
	}
}

// PruneExpired evicts buckets whose EndTime is older than (now - windowDuration).
func (sw *SlidingWindow) PruneExpired(now time.Time) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	cutoff := now.Add(-sw.windowDuration)
	for key, b := range sw.buckets {
		if b.EndTime.Before(cutoff) {
			delete(sw.buckets, key)
		}
	}
}

// GetMetrics aggregates active buckets within the sliding window horizon.
func (sw *SlidingWindow) GetMetrics(now time.Time) *SlidingWindowMetrics {
	sw.mu.RLock()
	defer sw.mu.RUnlock()

	cutoff := now.Add(-sw.windowDuration)
	m := &SlidingWindowMetrics{
		TenantID:    sw.tenantID,
		DatasetID:   sw.datasetID,
		WindowStart: cutoff,
		WindowEnd:   now,
	}

	for _, b := range sw.buckets {
		if b.EndTime.After(cutoff) {
			m.TotalBytes += b.TotalBytes
			m.TotalQueries += b.TotalQueries
			m.TotalComputeMs += b.TotalComputeMs
			m.EventCount += b.EventCount
			m.ActiveBuckets++
		}
	}

	secs := sw.windowDuration.Seconds()
	if secs > 0 {
		m.BytesPerSecond = float64(m.TotalBytes) / secs
		m.QueriesPerSecond = float64(m.TotalQueries) / secs
	}

	return m
}

// FlushDelta extracts and resets the unflushed incremental telemetry delta.
func (sw *SlidingWindow) FlushDelta() *WindowDelta {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if sw.unflushedDelta.DeltaEventCount == 0 {
		return nil
	}

	deltaCopy := sw.unflushedDelta
	sw.unflushedDelta = WindowDelta{
		TenantID:  sw.tenantID,
		DatasetID: sw.datasetID,
	}

	return &deltaCopy
}

// SlidingWindowAccumulator orchestrates sliding windows across all tenants and datasets.
type SlidingWindowAccumulator struct {
	mu             sync.RWMutex
	windowDuration time.Duration
	bucketDuration time.Duration
	windows        map[string]*SlidingWindow // compositeKey(tenantID, datasetID) -> window
}

// NewSlidingWindowAccumulator instantiates the global sliding window accumulator.
func NewSlidingWindowAccumulator(windowDuration, bucketDuration time.Duration) *SlidingWindowAccumulator {
	return &SlidingWindowAccumulator{
		windowDuration: windowDuration,
		bucketDuration: bucketDuration,
		windows:        make(map[string]*SlidingWindow),
	}
}

func (swa *SlidingWindowAccumulator) getOrCreateWindow(tenantID uuid.UUID, datasetID string) *SlidingWindow {
	key := compositeKey(tenantID, datasetID)
	swa.mu.RLock()
	w, exists := swa.windows[key]
	swa.mu.RUnlock()
	if exists {
		return w
	}

	swa.mu.Lock()
	defer swa.mu.Unlock()
	w, exists = swa.windows[key]
	if !exists {
		w = NewSlidingWindow(tenantID, datasetID, swa.windowDuration, swa.bucketDuration)
		swa.windows[key] = w
	}
	return w
}

// Record accumulates an event into the target tenant's dataset sliding window.
func (swa *SlidingWindowAccumulator) Record(ev *kafka.DatasetConsumptionEvent) {
	w := swa.getOrCreateWindow(ev.TenantID, ev.DatasetID)
	w.Record(ev)
}

// RecordBatch ingests a slice of events into the accumulator.
func (swa *SlidingWindowAccumulator) RecordBatch(events []*kafka.DatasetConsumptionEvent) {
	for _, ev := range events {
		swa.Record(ev)
	}
}

// GetMetrics retrieves current sliding window aggregates for a tenant and dataset.
func (swa *SlidingWindowAccumulator) GetMetrics(tenantID uuid.UUID, datasetID string) *SlidingWindowMetrics {
	key := compositeKey(tenantID, datasetID)
	swa.mu.RLock()
	w, exists := swa.windows[key]
	swa.mu.RUnlock()

	if !exists {
		now := time.Now().UTC()
		return &SlidingWindowMetrics{
			TenantID:    tenantID,
			DatasetID:   datasetID,
			WindowStart: now.Add(-swa.windowDuration),
			WindowEnd:   now,
		}
	}

	return w.GetMetrics(time.Now().UTC())
}

// GetAllMetrics retrieves metrics across all tracked windows.
func (swa *SlidingWindowAccumulator) GetAllMetrics() []*SlidingWindowMetrics {
	swa.mu.RLock()
	defer swa.mu.RUnlock()

	now := time.Now().UTC()
	metrics := make([]*SlidingWindowMetrics, 0, len(swa.windows))
	for _, w := range swa.windows {
		metrics = append(metrics, w.GetMetrics(now))
	}
	return metrics
}

// GetTenantTotalBytes sums sliding window bytes across all datasets for a given tenant.
func (swa *SlidingWindowAccumulator) GetTenantTotalBytes(tenantID uuid.UUID) int64 {
	swa.mu.RLock()
	defer swa.mu.RUnlock()

	now := time.Now().UTC()
	var total int64
	for _, w := range swa.windows {
		if w.tenantID == tenantID {
			m := w.GetMetrics(now)
			total += m.TotalBytes
		}
	}
	return total
}

// PruneExpired evicts expired buckets across all active sliding windows.
func (swa *SlidingWindowAccumulator) PruneExpired(now time.Time) {
	swa.mu.RLock()
	windowsCopy := make([]*SlidingWindow, 0, len(swa.windows))
	for _, w := range swa.windows {
		windowsCopy = append(windowsCopy, w)
	}
	swa.mu.RUnlock()

	for _, w := range windowsCopy {
		w.PruneExpired(now)
	}
}

// CollectDeltas drains all pending incremental deltas ready to be pushed to Redis.
func (swa *SlidingWindowAccumulator) CollectDeltas() []*WindowDelta {
	swa.mu.RLock()
	windowsCopy := make([]*SlidingWindow, 0, len(swa.windows))
	for _, w := range swa.windows {
		windowsCopy = append(windowsCopy, w)
	}
	swa.mu.RUnlock()

	deltas := make([]*WindowDelta, 0, len(windowsCopy))
	for _, w := range windowsCopy {
		if d := w.FlushDelta(); d != nil {
			deltas = append(deltas, d)
		}
	}
	return deltas
}

// ---------------------------------------------------------------------
// 3. Redis Cluster Synchronization Abstraction
// ---------------------------------------------------------------------

// RedisClusterClient abstracts a distributed Redis cluster client with atomic hash,
// key-value, distributed lock, and partition state operations.
type RedisClusterClient interface {
	HIncrBy(ctx context.Context, key, field string, incr int64) (int64, error)
	HIncrByMulti(ctx context.Context, key string, deltas map[string]int64) error
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	HSet(ctx context.Context, key string, values map[string]interface{}) error
	SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) (int64, error)
	Ping(ctx context.Context) error
}

// InMemoryRedisClusterClient is an enterprise thread-safe in-memory simulation of a Redis cluster
// supporting atomic updates, distributed locks, simulated network faults, and key inspections.
type InMemoryRedisClusterClient struct {
	mu           sync.RWMutex
	hashes       map[string]map[string]string
	strings      map[string]string
	expirations  map[string]time.Time
	faultInject  error
}

// NewInMemoryRedisClusterClient creates an initialized in-memory Redis cluster mock.
func NewInMemoryRedisClusterClient() *InMemoryRedisClusterClient {
	return &InMemoryRedisClusterClient{
		hashes:      make(map[string]map[string]string),
		strings:     make(map[string]string),
		expirations: make(map[string]time.Time),
	}
}

// SetFault allows unit tests to inject transient network or Redis cluster failures.
func (c *InMemoryRedisClusterClient) SetFault(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.faultInject = err
}

func (c *InMemoryRedisClusterClient) checkFault() error {
	if c.faultInject != nil {
		return c.faultInject
	}
	return nil
}

// HIncrBy atomically increments a hash field by incr.
func (c *InMemoryRedisClusterClient) HIncrBy(ctx context.Context, key, field string, incr int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkFault(); err != nil {
		return 0, err
	}

	h, exists := c.hashes[key]
	if !exists {
		h = make(map[string]string)
		c.hashes[key] = h
	}

	var cur int64
	if val, ok := h[field]; ok {
		parsed, _ := strconv.ParseInt(val, 10, 64)
		cur = parsed
	}

	newVal := cur + incr
	h[field] = strconv.FormatInt(newVal, 10)
	return newVal, nil
}

// HIncrByMulti atomically updates multiple hash fields in a single pseudo-pipeline.
func (c *InMemoryRedisClusterClient) HIncrByMulti(ctx context.Context, key string, deltas map[string]int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkFault(); err != nil {
		return err
	}

	h, exists := c.hashes[key]
	if !exists {
		h = make(map[string]string)
		c.hashes[key] = h
	}

	for field, incr := range deltas {
		var cur int64
		if val, ok := h[field]; ok {
			parsed, _ := strconv.ParseInt(val, 10, 64)
			cur = parsed
		}
		newVal := cur + incr
		h[field] = strconv.FormatInt(newVal, 10)
	}

	return nil
}

// HGetAll returns all fields and values of the specified hash.
func (c *InMemoryRedisClusterClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.checkFault(); err != nil {
		return nil, err
	}

	h, exists := c.hashes[key]
	if !exists {
		return make(map[string]string), nil
	}

	res := make(map[string]string, len(h))
	for k, v := range h {
		res[k] = v
	}
	return res, nil
}

// HSet sets field values in a hash.
func (c *InMemoryRedisClusterClient) HSet(ctx context.Context, key string, values map[string]interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkFault(); err != nil {
		return err
	}

	h, exists := c.hashes[key]
	if !exists {
		h = make(map[string]string)
		c.hashes[key] = h
	}

	for k, v := range values {
		h[k] = fmt.Sprintf("%v", v)
	}
	return nil
}

// SetNX implements atomic set-if-not-exists with TTL for distributed locking.
func (c *InMemoryRedisClusterClient) SetNX(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkFault(); err != nil {
		return false, err
	}

	now := time.Now()
	if exp, ok := c.expirations[key]; ok && exp.Before(now) {
		delete(c.strings, key)
		delete(c.expirations, key)
	}

	if _, exists := c.strings[key]; exists {
		return false, nil
	}

	c.strings[key] = fmt.Sprintf("%v", value)
	if ttl > 0 {
		c.expirations[key] = now.Add(ttl)
	}
	return true, nil
}

// Get retrieves a string key value.
func (c *InMemoryRedisClusterClient) Get(ctx context.Context, key string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if err := c.checkFault(); err != nil {
		return "", err
	}

	if exp, ok := c.expirations[key]; ok && exp.Before(time.Now()) {
		return "", errors.New("key expired")
	}

	val, exists := c.strings[key]
	if !exists {
		return "", errors.New("redis: nil")
	}
	return val, nil
}

// Set sets a key value with an optional TTL.
func (c *InMemoryRedisClusterClient) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkFault(); err != nil {
		return err
	}

	c.strings[key] = fmt.Sprintf("%v", value)
	if ttl > 0 {
		c.expirations[key] = time.Now().Add(ttl)
	} else {
		delete(c.expirations, key)
	}
	return nil
}

// Del deletes keys from memory.
func (c *InMemoryRedisClusterClient) Del(ctx context.Context, keys ...string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkFault(); err != nil {
		return 0, err
	}

	var count int64
	for _, k := range keys {
		if _, ok := c.hashes[k]; ok {
			delete(c.hashes, k)
			count++
		}
		if _, ok := c.strings[k]; ok {
			delete(c.strings, k)
			delete(c.expirations, k)
			count++
		}
	}
	return count, nil
}

// Ping checks client connectivity.
func (c *InMemoryRedisClusterClient) Ping(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.checkFault()
}

// RedisSyncManager handles synchronizing local aggregates and partition offsets with Redis cluster.
type RedisSyncManager struct {
	mu        sync.RWMutex
	client    RedisClusterClient
	keyPrefix string
}

// NewRedisSyncManager instantiates the Redis cluster synchronizer.
func NewRedisSyncManager(client RedisClusterClient, keyPrefix string) *RedisSyncManager {
	if keyPrefix == "" {
		keyPrefix = "metered:billing:"
	}
	return &RedisSyncManager{
		client:    client,
		keyPrefix: keyPrefix,
	}
}

func (rsm *RedisSyncManager) usageKey(tenantID uuid.UUID, datasetID string) string {
	return fmt.Sprintf("%susage:%s:%s", rsm.keyPrefix, tenantID.String(), datasetID)
}

func (rsm *RedisSyncManager) partitionOffsetKey(tp TopicPartition) string {
	return fmt.Sprintf("%soffset:%s:%d", rsm.keyPrefix, tp.Topic, tp.Partition)
}

func (rsm *RedisSyncManager) partitionLockKey(tp TopicPartition) string {
	return fmt.Sprintf("%slock:partition:%s:%d", rsm.keyPrefix, tp.Topic, tp.Partition)
}

// SyncDeltas publishes local memory deltas into the distributed Redis cluster via atomic HIncrByMulti.
func (rsm *RedisSyncManager) SyncDeltas(ctx context.Context, deltas []*WindowDelta) error {
	if len(deltas) == 0 {
		return nil
	}

	for _, d := range deltas {
		key := rsm.usageKey(d.TenantID, d.DatasetID)
		fields := map[string]int64{
			"total_bytes":       d.DeltaBytes,
			"total_queries":     d.DeltaQueries,
			"total_compute_ms":  d.DeltaComputeMs,
			"event_count":       d.DeltaEventCount,
		}

		if err := rsm.client.HIncrByMulti(ctx, key, fields); err != nil {
			return fmt.Errorf("%w: failed to sync delta for %s: %v", ErrSyncFailed, key, err)
		}

		_ = rsm.client.HSet(ctx, key, map[string]interface{}{
			"last_updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	}

	return nil
}

// FetchClusterUsage retrieves aggregated multi-node tenant consumption from the Redis cluster.
func (rsm *RedisSyncManager) FetchClusterUsage(ctx context.Context, tenantID uuid.UUID, datasetID string) (*TenantDatasetUsage, error) {
	key := rsm.usageKey(tenantID, datasetID)
	data, err := rsm.client.HGetAll(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSyncFailed, err)
	}

	usage := &TenantDatasetUsage{
		TenantID:  tenantID,
		DatasetID: datasetID,
	}

	if len(data) == 0 {
		return usage, nil
	}

	if val, ok := data["total_bytes"]; ok {
		usage.TotalBytes, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := data["total_queries"]; ok {
		usage.TotalQueries, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := data["total_compute_ms"]; ok {
		usage.TotalComputeMs, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := data["event_count"]; ok {
		usage.EventCount, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := data["last_updated_at"]; ok {
		t, _ := time.Parse(time.RFC3339Nano, val)
		usage.WindowEnd = t
	}

	return usage, nil
}

// PersistPartitionOffset coordinates distributed offset commits in Redis for exactly-once recovery.
func (rsm *RedisSyncManager) PersistPartitionOffset(ctx context.Context, tp TopicPartition, offset int64, nodeID string) error {
	key := rsm.partitionOffsetKey(tp)
	payload := map[string]interface{}{
		"committed_offset": offset,
		"committed_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"committed_by":     nodeID,
	}
	return rsm.client.HSet(ctx, key, payload)
}

// GetCommittedOffset queries the Redis cluster for a partition's latest committed offset.
func (rsm *RedisSyncManager) GetCommittedOffset(ctx context.Context, tp TopicPartition) (int64, error) {
	key := rsm.partitionOffsetKey(tp)
	data, err := rsm.client.HGetAll(ctx, key)
	if err != nil {
		return -1, err
	}
	if val, ok := data["committed_offset"]; ok {
		return strconv.ParseInt(val, 10, 64)
	}
	return -1, nil // Not committed yet
}

// AcquirePartitionLock claims a distributed lease for partition ownership during rebalancing.
func (rsm *RedisSyncManager) AcquirePartitionLock(ctx context.Context, tp TopicPartition, nodeID string, ttl time.Duration) (bool, error) {
	key := rsm.partitionLockKey(tp)
	return rsm.client.SetNX(ctx, key, nodeID, ttl)
}

// ReleasePartitionLock releases the distributed partition lease.
func (rsm *RedisSyncManager) ReleasePartitionLock(ctx context.Context, tp TopicPartition, nodeID string) error {
	key := rsm.partitionLockKey(tp)
	val, err := rsm.client.Get(ctx, key)
	if err != nil {
		return nil
	}
	if val == nodeID {
		_, err = rsm.client.Del(ctx, key)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------
// 4. Partition Assignment & Exactly-Once Commit Coordination
// ---------------------------------------------------------------------

// TopicPartition identifies a unique Kafka topic partition.
type TopicPartition struct {
	Topic     string `json:"topic"`
	Partition int32  `json:"partition"`
}

// String returns a canonical representation e.g. "telemetry-events-0".
func (tp TopicPartition) String() string {
	return fmt.Sprintf("%s-%d", tp.Topic, tp.Partition)
}

// PartitionState reflects worker assignment lifecycle.
type PartitionState string

const (
	PartitionStateAssigned PartitionState = "ASSIGNED"
	PartitionStateRevoking PartitionState = "REVOKING"
	PartitionStateRevoked  PartitionState = "REVOKED"
)

// PartitionMetadata tracks high watermarks, processed event counts, and offset checkpoints.
type PartitionMetadata struct {
	TopicPartition      TopicPartition
	State               PartitionState
	AssignedAt          time.Time
	RevokedAt           time.Time
	HighWatermark       int64
	LastCommittedOffset int64
	PendingCommitOffset int64
	ProcessedCount      int64
}

// KafkaOffsetCommitter defines the contract for committing consumer offsets back to Kafka.
type KafkaOffsetCommitter interface {
	CommitOffset(ctx context.Context, tp TopicPartition, offset int64) error
}

// MockKafkaOffsetCommitter provides an in-memory test double for Kafka offset commits.
type MockKafkaOffsetCommitter struct {
	mu        sync.RWMutex
	committed map[TopicPartition]int64
}

// NewMockKafkaOffsetCommitter instantiates a mock Kafka committer.
func NewMockKafkaOffsetCommitter() *MockKafkaOffsetCommitter {
	return &MockKafkaOffsetCommitter{
		committed: make(map[TopicPartition]int64),
	}
}

// CommitOffset records the committed offset.
func (m *MockKafkaOffsetCommitter) CommitOffset(ctx context.Context, tp TopicPartition, offset int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.committed[tp] = offset
	return nil
}

// GetCommitted returns the last committed offset for a partition.
func (m *MockKafkaOffsetCommitter) GetCommitted(tp TopicPartition) (int64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.committed[tp]
	return val, ok
}

// IdempotencyFilter prevents duplicate event processing across partition rebalances and retries.
type IdempotencyFilter struct {
	mu         sync.RWMutex
	seenKeys   map[string]time.Time
	windowTTL  time.Duration
	maxEntries int
}

// NewIdempotencyFilter instantiates a sliding idempotency filter.
func NewIdempotencyFilter(windowTTL time.Duration, maxEntries int) *IdempotencyFilter {
	if windowTTL <= 0 {
		windowTTL = 10 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 500000
	}
	return &IdempotencyFilter{
		seenKeys:   make(map[string]time.Time),
		windowTTL:  windowTTL,
		maxEntries: maxEntries,
	}
}

// CheckAndRecord tests if a key has already been seen. Returns true if key is NEW (accepted).
func (f *IdempotencyFilter) CheckAndRecord(key string, now time.Time) bool {
	if key == "" {
		return true // No idempotency key provided
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Evict if cache exceeds safety capacity
	if len(f.seenKeys) >= f.maxEntries {
		cutoff := now.Add(-f.windowTTL)
		for k, t := range f.seenKeys {
			if t.Before(cutoff) {
				delete(f.seenKeys, k)
			}
		}
	}

	if seenAt, exists := f.seenKeys[key]; exists {
		if now.Sub(seenAt) <= f.windowTTL {
			return false // Duplicate!
		}
	}

	f.seenKeys[key] = now
	return true
}

// Prune removes expired idempotency keys.
func (f *IdempotencyFilter) Prune(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cutoff := now.Add(-f.windowTTL)
	for k, t := range f.seenKeys {
		if t.Before(cutoff) {
			delete(f.seenKeys, k)
		}
	}
}

// PartitionCoordinator coordinates assigned partition metadata, watermark progressions,
// and exactly-once two-phase offset commits.
type PartitionCoordinator struct {
	mu          sync.RWMutex
	nodeID      string
	partitions  map[TopicPartition]*PartitionMetadata
	idempotency *IdempotencyFilter
	redisSync   *RedisSyncManager
	committer   KafkaOffsetCommitter
}

// NewPartitionCoordinator instantiates the partition and commit coordinator.
func NewPartitionCoordinator(
	nodeID string,
	redisSync *RedisSyncManager,
	committer KafkaOffsetCommitter,
	idempotencyTTL time.Duration,
) *PartitionCoordinator {
	return &PartitionCoordinator{
		nodeID:      nodeID,
		partitions:  make(map[TopicPartition]*PartitionMetadata),
		idempotency: NewIdempotencyFilter(idempotencyTTL, 500000),
		redisSync:   redisSync,
		committer:   committer,
	}
}

// OnPartitionsAssigned registers new partition assignments.
func (pc *PartitionCoordinator) OnPartitionsAssigned(partitions []TopicPartition) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	now := time.Now().UTC()
	for _, tp := range partitions {
		pc.partitions[tp] = &PartitionMetadata{
			TopicPartition:      tp,
			State:               PartitionStateAssigned,
			AssignedAt:          now,
			HighWatermark:       -1,
			LastCommittedOffset: -1,
			PendingCommitOffset: -1,
		}
	}
}

// OnPartitionsRevoking marks partitions as revoking before final flush.
func (pc *PartitionCoordinator) OnPartitionsRevoking(partitions []TopicPartition) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	for _, tp := range partitions {
		if meta, ok := pc.partitions[tp]; ok {
			meta.State = PartitionStateRevoking
		}
	}
}

// OnPartitionsRevoked completes revocation.
func (pc *PartitionCoordinator) OnPartitionsRevoked(partitions []TopicPartition) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	now := time.Now().UTC()
	for _, tp := range partitions {
		if meta, ok := pc.partitions[tp]; ok {
			meta.State = PartitionStateRevoked
			meta.RevokedAt = now
			delete(pc.partitions, tp)
		}
	}
}

// IsAssigned verifies whether a partition is currently assigned and active.
func (pc *PartitionCoordinator) IsAssigned(tp TopicPartition) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	meta, exists := pc.partitions[tp]
	return exists && meta.State == PartitionStateAssigned
}

// ValidateAndTrackEvent verifies idempotency and advances the high watermark.
func (pc *PartitionCoordinator) ValidateAndTrackEvent(tp TopicPartition, offset int64, event *kafka.DatasetConsumptionEvent) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	meta, exists := pc.partitions[tp]
	if !exists || meta.State != PartitionStateAssigned {
		return ErrPartitionNotAssigned
	}

	// Idempotency check: combine EventID and IdempotencyKey
	dedupKey := event.IdempotencyKey
	if dedupKey == "" && event.EventID != uuid.Nil {
		dedupKey = event.EventID.String()
	}

	if dedupKey != "" {
		if !pc.idempotency.CheckAndRecord(dedupKey, time.Now().UTC()) {
			return ErrDuplicateEvent
		}
	}

	if offset > meta.HighWatermark {
		meta.HighWatermark = offset
	}
	meta.PendingCommitOffset = offset
	meta.ProcessedCount++

	return nil
}

// GetPartitionMetadata copies current metadata for inspection.
func (pc *PartitionCoordinator) GetPartitionMetadata(tp TopicPartition) (*PartitionMetadata, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	meta, exists := pc.partitions[tp]
	if !exists {
		return nil, false
	}
	cp := *meta
	return &cp, true
}

// CommitPartition coordinates exactly-once commit:
// Phase 1: Persist partition offset checkpoint into Redis.
// Phase 2: Commit offset back to Kafka consumer group coordinator.
func (pc *PartitionCoordinator) CommitPartition(ctx context.Context, tp TopicPartition) (int64, error) {
	pc.mu.Lock()
	meta, exists := pc.partitions[tp]
	if !exists {
		pc.mu.Unlock()
		return -1, ErrPartitionNotAssigned
	}

	targetOffset := meta.PendingCommitOffset
	if targetOffset <= meta.LastCommittedOffset || targetOffset < 0 {
		offset := meta.LastCommittedOffset
		pc.mu.Unlock()
		return offset, nil // Nothing new to commit
	}
	pc.mu.Unlock()

	// Phase 1: Persist offset checkpoint to Redis cluster
	if pc.redisSync != nil {
		if err := pc.redisSync.PersistPartitionOffset(ctx, tp, targetOffset, pc.nodeID); err != nil {
			return -1, fmt.Errorf("%w: redis offset checkpoint failed for %s: %v", ErrSyncFailed, tp.String(), err)
		}
	}

	// Phase 2: Commit offset to Kafka
	if pc.committer != nil {
		if err := pc.committer.CommitOffset(ctx, tp, targetOffset); err != nil {
			return -1, fmt.Errorf("billing/kafka: commit failed for %s: %v", tp.String(), err)
		}
	}

	// Update local partition state
	pc.mu.Lock()
	if meta, exists = pc.partitions[tp]; exists {
		meta.LastCommittedOffset = targetOffset
	}
	pc.mu.Unlock()

	return targetOffset, nil
}

// CommitAllAssigned coordinates commits across all currently assigned partitions.
func (pc *PartitionCoordinator) CommitAllAssigned(ctx context.Context) (map[TopicPartition]int64, error) {
	pc.mu.RLock()
	assigned := make([]TopicPartition, 0, len(pc.partitions))
	for tp, meta := range pc.partitions {
		if meta.State == PartitionStateAssigned || meta.State == PartitionStateRevoking {
			assigned = append(assigned, tp)
		}
	}
	pc.mu.RUnlock()

	results := make(map[TopicPartition]int64)
	for _, tp := range assigned {
		committed, err := pc.CommitPartition(ctx, tp)
		if err != nil {
			return results, err
		}
		results[tp] = committed
	}

	return results, nil
}

// ---------------------------------------------------------------------
// 5. Distributed Cache Coordinator (Master Facade)
// ---------------------------------------------------------------------

// DistributedCacheConfig tunes buffer capacities, flush intervals, and sliding window horizons.
type DistributedCacheConfig struct {
	NodeID                string
	RingBufferSize        int
	RingBufferPolicy      OverflowPolicy
	FlushInterval         time.Duration
	FlushBatchSize        int
	SlidingWindowDuration time.Duration
	BucketGranularity     time.Duration
	RedisSyncInterval     time.Duration
	IdempotencyWindow     time.Duration
	AutoCommitInterval    time.Duration
}

// DefaultDistributedCacheConfig provides sensible enterprise defaults.
func DefaultDistributedCacheConfig() DistributedCacheConfig {
	return DistributedCacheConfig{
		NodeID:                "billing-aggregator-node-01",
		RingBufferSize:        32768,
		RingBufferPolicy:      OverflowPolicyReject,
		FlushInterval:         50 * time.Millisecond,
		FlushBatchSize:        500,
		SlidingWindowDuration: 15 * time.Minute,
		BucketGranularity:     10 * time.Second,
		RedisSyncInterval:     500 * time.Millisecond,
		IdempotencyWindow:     15 * time.Minute,
		AutoCommitInterval:    1 * time.Second,
	}
}

// DistributedCacheCoordinator orchestrates the ring-buffer, sliding-window accumulator,
// Redis cluster synchronization, and Kafka partition commit coordination into a single high-throughput engine.
type DistributedCacheCoordinator struct {
	mu             sync.RWMutex
	cfg            DistributedCacheConfig
	ringBuffer     *TelemetryRingBuffer
	accumulator    *SlidingWindowAccumulator
	redisClient    RedisClusterClient
	redisSync      *RedisSyncManager
	committer      KafkaOffsetCommitter
	partitionCoord *PartitionCoordinator

	stopCh    chan struct{}
	wg        sync.WaitGroup
	isStarted bool
	isClosed  bool
}

// NewDistributedCacheCoordinator instantiates the unified cache coordinator.
func NewDistributedCacheCoordinator(
	cfg DistributedCacheConfig,
	redisClient RedisClusterClient,
	committer KafkaOffsetCommitter,
) *DistributedCacheCoordinator {
	if cfg.NodeID == "" {
		cfg.NodeID = fmt.Sprintf("node-%d", time.Now().UnixNano())
	}
	if cfg.RingBufferSize <= 0 {
		cfg.RingBufferSize = 32768
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 50 * time.Millisecond
	}
	if cfg.FlushBatchSize <= 0 {
		cfg.FlushBatchSize = 500
	}
	if cfg.SlidingWindowDuration <= 0 {
		cfg.SlidingWindowDuration = 15 * time.Minute
	}
	if cfg.BucketGranularity <= 0 {
		cfg.BucketGranularity = 10 * time.Second
	}
	if cfg.RedisSyncInterval <= 0 {
		cfg.RedisSyncInterval = 500 * time.Millisecond
	}
	if cfg.IdempotencyWindow <= 0 {
		cfg.IdempotencyWindow = 15 * time.Minute
	}

	redisSync := NewRedisSyncManager(redisClient, "metered:billing:")
	partCoord := NewPartitionCoordinator(cfg.NodeID, redisSync, committer, cfg.IdempotencyWindow)

	return &DistributedCacheCoordinator{
		cfg:            cfg,
		ringBuffer:     NewTelemetryRingBuffer(cfg.RingBufferSize, cfg.RingBufferPolicy),
		accumulator:    NewSlidingWindowAccumulator(cfg.SlidingWindowDuration, cfg.BucketGranularity),
		redisClient:    redisClient,
		redisSync:      redisSync,
		committer:      committer,
		partitionCoord: partCoord,
		stopCh:         make(chan struct{}),
	}
}

// Start launches background asynchronous flush, Redis cluster sync, and partition commit loops.
func (c *DistributedCacheCoordinator) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.isStarted {
		c.mu.Unlock()
		return nil
	}
	c.isStarted = true
	c.mu.Unlock()

	c.wg.Add(3)
	go c.flushLoop(ctx)
	go c.syncLoop(ctx)
	go c.commitLoop(ctx)

	return nil
}

// Stop initiates a graceful drain of the ring buffer, synchronizes final metrics to Redis,
// commits pending partition offsets, and stops all background workers.
func (c *DistributedCacheCoordinator) Stop() error {
	c.mu.Lock()
	if !c.isStarted || c.isClosed {
		c.mu.Unlock()
		return nil
	}
	c.isClosed = true
	close(c.stopCh)
	c.mu.Unlock()

	c.wg.Wait()

	// Final drain and sync
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c.drainRingBufferToAccumulator()
	_ = c.SyncToRedis(ctx)
	_, _ = c.partitionCoord.CommitAllAssigned(ctx)

	return nil
}

// Ingest routes an incoming telemetry event through partition tracking, idempotency validation,
// and into the local memory ring buffer.
func (c *DistributedCacheCoordinator) Ingest(tp TopicPartition, offset int64, event *kafka.DatasetConsumptionEvent) error {
	c.mu.RLock()
	if c.isClosed {
		c.mu.RUnlock()
		return ErrCoordinatorStopped
	}
	c.mu.RUnlock()

	// 1. Partition assignment & idempotency validation
	if err := c.partitionCoord.ValidateAndTrackEvent(tp, offset, event); err != nil {
		return err
	}

	// 2. High-speed push into local memory ring buffer
	item := &RingBufferItem{
		Event:          event,
		TopicPartition: tp,
		Offset:         offset,
		EnqueuedAt:     time.Now().UTC(),
	}

	return c.ringBuffer.Push(item)
}

// IngestBatch routes a batch of events with contiguous offsets.
func (c *DistributedCacheCoordinator) IngestBatch(tp TopicPartition, startOffset int64, events []*kafka.DatasetConsumptionEvent) error {
	for i, ev := range events {
		offset := startOffset + int64(i)
		if err := c.Ingest(tp, offset, ev); err != nil {
			return err
		}
	}
	return nil
}

// drainRingBufferToAccumulator extracts queued events from ring buffer and updates sliding window state.
func (c *DistributedCacheCoordinator) drainRingBufferToAccumulator() int {
	items := c.ringBuffer.DrainAll()
	if len(items) == 0 {
		return 0
	}

	for _, item := range items {
		c.accumulator.Record(item.Event)
	}

	return len(items)
}

// flushLoop continuously drains ring buffer items into sliding windows based on batch threshold or ticker.
func (c *DistributedCacheCoordinator) flushLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.drainRingBufferToAccumulator()
			return
		case <-c.stopCh:
			c.drainRingBufferToAccumulator()
			return
		case <-ticker.C:
			c.drainRingBufferToAccumulator()
			c.accumulator.PruneExpired(time.Now().UTC())
		}
	}
}

// SyncToRedis synchronizes local accumulator deltas to the distributed Redis cluster.
func (c *DistributedCacheCoordinator) SyncToRedis(ctx context.Context) error {
	deltas := c.accumulator.CollectDeltas()
	if len(deltas) == 0 {
		return nil
	}
	return c.redisSync.SyncDeltas(ctx, deltas)
}

// syncLoop periodically pushes deltas to Redis cluster.
func (c *DistributedCacheCoordinator) syncLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cfg.RedisSyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = c.SyncToRedis(context.Background())
			return
		case <-c.stopCh:
			_ = c.SyncToRedis(context.Background())
			return
		case <-ticker.C:
			_ = c.SyncToRedis(ctx)
		}
	}
}

// commitLoop periodically commits partition offsets.
func (c *DistributedCacheCoordinator) commitLoop(ctx context.Context) {
	defer c.wg.Done()

	interval := c.cfg.AutoCommitInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_, _ = c.partitionCoord.CommitAllAssigned(context.Background())
			return
		case <-c.stopCh:
			_, _ = c.partitionCoord.CommitAllAssigned(context.Background())
			return
		case <-ticker.C:
			_, _ = c.partitionCoord.CommitAllAssigned(ctx)
		}
	}
}

// OnPartitionsAssigned handles Kafka consumer rebalance partition assignment.
func (c *DistributedCacheCoordinator) OnPartitionsAssigned(partitions []TopicPartition) error {
	c.partitionCoord.OnPartitionsAssigned(partitions)
	return nil
}

// OnPartitionsRevoked handles Kafka rebalance revocation:
// flushes buffer, commits accumulated state, and unregisters partitions.
func (c *DistributedCacheCoordinator) OnPartitionsRevoked(partitions []TopicPartition) error {
	c.partitionCoord.OnPartitionsRevoking(partitions)

	// Flush ring buffer items into accumulator
	c.drainRingBufferToAccumulator()

	// Sync local accumulator deltas to Redis
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_ = c.SyncToRedis(ctx)

	// Commit final offsets for the revoking partitions
	for _, tp := range partitions {
		_, _ = c.partitionCoord.CommitPartition(ctx, tp)
	}

	c.partitionCoord.OnPartitionsRevoked(partitions)
	return nil
}

// GetWindowUsage retrieves local sliding window metrics for a tenant dataset.
func (c *DistributedCacheCoordinator) GetWindowUsage(tenantID uuid.UUID, datasetID string) *SlidingWindowMetrics {
	return c.accumulator.GetMetrics(tenantID, datasetID)
}

// GetClusterUsage retrieves synchronized multi-node usage aggregated in Redis cluster.
func (c *DistributedCacheCoordinator) GetClusterUsage(ctx context.Context, tenantID uuid.UUID, datasetID string) (*TenantDatasetUsage, error) {
	return c.redisSync.FetchClusterUsage(ctx, tenantID, datasetID)
}

// GetPartitionMetadata retrieves metadata for an assigned partition.
func (c *DistributedCacheCoordinator) GetPartitionMetadata(tp TopicPartition) (*PartitionMetadata, bool) {
	return c.partitionCoord.GetPartitionMetadata(tp)
}

// GetRingBufferStats retrieves ring buffer throughput stats.
func (c *DistributedCacheCoordinator) GetRingBufferStats() (pushed, drained, dropped uint64, count int) {
	p, d, dr := c.ringBuffer.Stats()
	return p, d, dr, c.ringBuffer.Len()
}
