package debugbundle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/debugbundle/debugbundle-go/v3/transport"
)

func (client *Client) Flush(ctx context.Context) error {
	if !client.sendMu.TryLock() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	completed := make(chan struct{})
	go func() {
		defer client.sendMu.Unlock()
		defer close(completed)
		client.flushNow(ctx)
	}()
	deadline := time.NewTimer(client.config.requestTimeout)
	defer deadline.Stop()
	select {
	case <-completed:
	case <-ctx.Done():
	case <-deadline.C:
	}
	return nil
}

func (client *Client) flushNow(ctx context.Context) {
	client.mu.Lock()
	if client.closed || client.transport == nil {
		client.mu.Unlock()
		return
	}
	client.flushActive = true
	defer func() {
		client.mu.Lock()
		client.flushActive = false
		if !client.closed && (len(client.buffer) > 0 || client.pressureDrops > 0) {
			client.scheduleFlushLocked(false)
		}
		client.mu.Unlock()
	}()
	now := time.Now().UTC()
	if contention := client.contentionDrops.Swap(0); contention > 0 {
		client.recordPressureDropLocked(int(min(contention, int64(int(^uint(0)>>1)))))
	}
	if !client.retryUntil.IsZero() && now.Before(client.retryUntil) {
		client.status = StatusDegraded
		client.mu.Unlock()
		return
	}
	batch := append([]queuedEvent{}, client.buffer...)
	clear(client.buffer)
	client.buffer = client.buffer[:0]
	client.pendingLowPriority = 0
	client.inFlightCount = len(batch)
	client.inFlightBytes = client.bufferBytes
	client.bufferBytes = 0
	aggregates := client.suppression.PendingAggregates(now)
	pressureCount := 0
	pressureFirstAt := client.pressureFirstSeen
	pressureLastAt := client.pressureLastSeen
	if client.pressureDrops > 0 && (client.lastPressureReport.IsZero() || now.Sub(client.lastPressureReport) >= time.Minute) {
		pressureCount = client.pressureDrops
		client.pressureDrops = 0
		aggregates = append([]suppressionAggregate{{
			Fingerprint: "sdk:queue-pressure", Suppressed: pressureCount,
			FirstSeenAt: pressureFirstAt, LastSeenAt: pressureLastAt,
			WindowMillis: time.Minute.Milliseconds(),
		}}, aggregates...)
		client.pressureFirstSeen = time.Time{}
		client.pressureLastSeen = time.Time{}
	}
	client.stopFlushTimerLocked()
	client.mu.Unlock()

	for _, aggregate := range aggregates {
		event := client.newEventEnvelope("error_suppressed", now, map[string]any{
			"fingerprint":      aggregate.Fingerprint,
			"suppressed_count": aggregate.Suppressed,
			"first_seen":       aggregate.FirstSeenAt.Format(time.RFC3339Nano),
			"last_seen":        aggregate.LastSeenAt.Format(time.RFC3339Nano),
			"window_seconds":   maxInt64(1, aggregate.WindowMillis/1000),
		})
		protected := client.protectEvent(event)
		if protected == nil {
			continue
		}
		encoded, err := json.Marshal(protected)
		if err == nil {
			client.mu.Lock()
			if client.inFlightCount+len(client.buffer) < maxPendingEvents &&
				client.inFlightBytes+client.bufferBytes+len(encoded) <= maxPendingBytes {
				batch = append(batch, queuedEvent{encoded: encoded, highPriority: false})
				client.inFlightCount++
				client.inFlightBytes += len(encoded)
				if aggregate.Fingerprint == "sdk:queue-pressure" {
					client.lastPressureReport = now
				}
			} else {
				if aggregate.Fingerprint == "sdk:queue-pressure" {
					client.recordPressureDropLocked(pressureCount)
					client.pressureFirstSeen = pressureFirstAt
					client.pressureLastSeen = pressureLastAt
				} else {
					client.recordPressureDropLocked(1)
				}
			}
			client.mu.Unlock()
		}
	}
	if len(batch) == 0 {
		return
	}
	finalized := make([]queuedEvent, 0, len(batch))
	for index, event := range batch {
		prepared, keep := client.finalizeQueuedEvent(event)
		client.mu.Lock()
		// Each output must own its bytes before the next application callback
		// can wait. Never keep the pre-hook original when a valid replacement cannot fit.
		client.inFlightCount--
		client.inFlightBytes -= len(event.encoded)
		batch[index] = queuedEvent{}
		if keep {
			for client.inFlightCount+len(client.buffer) >= maxPendingEvents ||
				client.inFlightBytes+client.bufferBytes+len(prepared.encoded) > maxPendingBytes {
				if prepared.highPriority && client.evictLowPriorityLocked() {
					continue
				}
				keep = false
				client.recordPressureDropLocked(1)
				break
			}
			if keep {
				finalized = append(finalized, prepared)
				client.inFlightCount++
				client.inFlightBytes += len(prepared.encoded)
			}
		}
		client.mu.Unlock()
	}
	batch = finalized
	if len(batch) == 0 {
		return
	}

	wireEvents := make([]json.RawMessage, len(batch))
	for index, event := range batch {
		wireEvents[index] = event.encoded
	}
	sendContext, cancelSend := context.WithTimeout(ctx, client.config.requestTimeout)
	defer cancelSend()
	response, err := sendWithoutPanic(client.transport, sendContext, transport.Request{
		ProjectToken: client.config.projectToken,
		Events:       wireEvents,
	})
	client.mu.Lock()
	defer client.mu.Unlock()
	client.inFlightCount = 0
	client.inFlightBytes = 0
	if err != nil {
		client.restoreLocked(batch)
		client.failures++
		client.status = StatusDisconnected
		return
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		client.restoreLocked(batch)
		client.failures++
		client.status = StatusDegraded
		retryAfter := response.RetryAfter
		if retryAfter <= 0 {
			retryAfter = defaultRetryBackoff(client.failures)
		}
		client.retryUntil = time.Now().Add(retryAfter)
		return
	}
	if response.StatusCode >= http.StatusBadRequest {
		client.status = StatusHealthy
		client.failures = 0
		return
	}
	acknowledgement := decideIngestionAcknowledgement(response.Body, len(batch))
	if acknowledgement.kind == "protocol_failure" {
		client.restoreLocked(batch)
		client.failures++
		client.status = StatusDegraded
		retryAfter := response.RetryAfter
		if retryAfter <= 0 {
			retryAfter = defaultRetryBackoff(client.failures)
		}
		client.retryUntil = time.Now().Add(retryAfter)
		return
	}
	if acknowledgement.kind == "acknowledged" {
		retryableEvents := make([]queuedEvent, 0, len(acknowledgement.retryableIndices))
		for _, index := range acknowledgement.retryableIndices {
			if index >= 0 && index < len(batch) {
				retryableEvents = append(retryableEvents, batch[index])
			}
		}
		client.restoreLocked(retryableEvents)
		if acknowledgement.accepted > 0 {
			successAt := time.Now().UTC()
			client.lastEventAt = &successAt
		}
		if len(retryableEvents) > 0 {
			client.failures++
			client.status = StatusDegraded
			retryAfter := response.RetryAfter
			if retryAfter <= 0 {
				retryAfter = defaultRetryBackoff(client.failures)
			}
			client.retryUntil = time.Now().Add(retryAfter)
			return
		}
		client.retryUntil = time.Time{}
		if acknowledgement.accepted == 0 {
			client.failures = 3
			client.status = StatusDisconnected
			return
		}
		client.failures = 0
		client.status = StatusHealthy
		return
	}
	client.status = StatusHealthy
	client.failures = 0
	successAt := time.Now().UTC()
	client.lastEventAt = &successAt
	client.retryUntil = time.Time{}
	return
}

func sendWithoutPanic(sender transport.Sender, ctx context.Context, request transport.Request) (response transport.Response, err error) {
	defer func() {
		if recover() != nil {
			// Preserve the ordinary retry path without rendering a caller-owned panic value.
			response = transport.Response{}
			err = errors.New("sdk transport callback failed")
		}
	}()
	return sender.Send(ctx, request)
}

func (client *Client) mayPrepareCapture(highPriority bool) bool {
	if !client.mu.TryLock() {
		client.contentionDrops.Add(1)
		return false
	}
	defer client.mu.Unlock()
	if client.closed || client.transport == nil {
		return false
	}
	if client.inFlightCount+len(client.buffer) < maxPendingEvents &&
		client.inFlightBytes+client.bufferBytes < maxPendingBytes {
		return true
	}
	if highPriority && client.pendingLowPriority > 0 {
		return true
	}
	client.recordPressureDropLocked(1)
	return false
}

func (client *Client) offerEncodedLocked(event queuedEvent) bool {
	bytes := len(event.encoded)
	if bytes > maxPendingBytes {
		client.recordPressureDropLocked(1)
		return false
	}
	for client.inFlightCount+len(client.buffer) >= maxPendingEvents ||
		client.inFlightBytes+client.bufferBytes+bytes > maxPendingBytes {
		if !event.highPriority || !client.evictLowPriorityLocked() {
			client.recordPressureDropLocked(1)
			return false
		}
	}
	client.buffer = append(client.buffer, event)
	if !event.highPriority {
		client.pendingLowPriority++
	}
	client.bufferBytes += bytes
	client.scheduleFlushLocked(len(client.buffer) >= client.config.batchSize)
	return true
}

func (client *Client) recordPressureDropLocked(count int) {
	if count <= 0 {
		return
	}
	now := time.Now().UTC()
	if client.pressureDrops == 0 {
		client.pressureFirstSeen = now
	}
	client.pressureLastSeen = now
	maximum := int(^uint(0) >> 1)
	if count > maximum-client.pressureDrops {
		client.pressureDrops = maximum
	} else {
		client.pressureDrops += count
	}
}

func (client *Client) evictLowPriorityLocked() bool {
	for index, event := range client.buffer {
		if event.highPriority {
			continue
		}
		client.bufferBytes -= len(event.encoded)
		client.buffer = append(client.buffer[:index], client.buffer[index+1:]...)
		client.pendingLowPriority--
		client.recordPressureDropLocked(1)
		return true
	}
	return false
}

func (client *Client) restoreLocked(events []queuedEvent) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		for len(client.buffer) >= maxPendingEvents || client.bufferBytes+len(event.encoded) > maxPendingBytes {
			if !event.highPriority || !client.evictLowPriorityLocked() {
				client.recordPressureDropLocked(1)
				goto nextEvent
			}
		}
		client.buffer = append(client.buffer, queuedEvent{})
		copy(client.buffer[1:], client.buffer[:len(client.buffer)-1])
		client.buffer[0] = event
		if !event.highPriority {
			client.pendingLowPriority++
		}
		client.bufferBytes += len(event.encoded)
	nextEvent:
	}
}

func (client *Client) scheduleFlushLocked(immediate bool) {
	if client.flushActive {
		return
	}
	if client.flushTimer != nil {
		if immediate {
			client.flushTimer.Stop()
			client.flushTimer = nil
		} else {
			return
		}
	}
	delay := client.config.flushInterval
	if immediate {
		delay = 0
	}
	if retryDelay := time.Until(client.retryUntil); retryDelay > delay {
		delay = retryDelay
	}
	if len(client.buffer) == 0 && client.pressureDrops > 0 && !client.lastPressureReport.IsZero() {
		if reportDelay := time.Until(client.lastPressureReport.Add(time.Minute)); reportDelay > delay {
			delay = reportDelay
		}
	}
	client.flushTimer = time.AfterFunc(delay, func() {
		_ = client.Flush(context.Background())
	})
}

func (client *Client) stopFlushTimerLocked() {
	if client.flushTimer != nil {
		client.flushTimer.Stop()
		client.flushTimer = nil
	}
}
