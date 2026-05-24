package debugbundle

import (
	"sync"
	"time"
)

const (
	suppressionWindow = 30 * time.Second
	loopWindow        = 2 * time.Second
	loopThreshold     = 10
	resetAfterSilence = 60 * time.Second
)

type suppressionAggregate struct {
	Fingerprint  string
	Suppressed   int
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	WindowMillis int64
	LoopMode     bool
}

type suppressionState struct {
	windowStarted   time.Time
	firstSeen       time.Time
	lastSeen        time.Time
	delivered       int
	suppressed      int
	loopMode        bool
	recentSeen      []time.Time
	lastAggregateAt time.Time
}

type suppressionTracker struct {
	mu     sync.Mutex
	states map[string]*suppressionState
}

func newSuppressionTracker() *suppressionTracker {
	return &suppressionTracker{states: map[string]*suppressionState{}}
}

func (tracker *suppressionTracker) ShouldCapture(fingerprint string, now time.Time) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	state := tracker.states[fingerprint]
	if state == nil || now.Sub(state.lastSeen) > resetAfterSilence {
		state = &suppressionState{
			windowStarted: now,
			firstSeen:     now,
			recentSeen:    make([]time.Time, 0, loopThreshold+1),
		}
		tracker.states[fingerprint] = state
	}

	if now.Sub(state.windowStarted) > suppressionWindow {
		state.windowStarted = now
		state.delivered = 0
		state.loopMode = false
		state.recentSeen = state.recentSeen[:0]
	}

	state.lastSeen = now
	state.recentSeen = append(state.recentSeen, now)
	state.recentSeen = pruneRecent(state.recentSeen, now.Add(-loopWindow))
	if len(state.recentSeen) > loopThreshold {
		state.loopMode = true
	}

	if !state.loopMode && state.delivered < 3 {
		state.delivered++
		return true
	}

	state.suppressed++
	return false
}

func (tracker *suppressionTracker) PendingAggregates(now time.Time) []suppressionAggregate {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	aggregates := make([]suppressionAggregate, 0)
	for fingerprint, state := range tracker.states {
		if now.Sub(state.lastSeen) > resetAfterSilence {
			delete(tracker.states, fingerprint)
			continue
		}
		if state.suppressed == 0 {
			continue
		}
		aggregates = append(aggregates, suppressionAggregate{
			Fingerprint:  fingerprint,
			Suppressed:   state.suppressed,
			FirstSeenAt:  state.firstSeen,
			LastSeenAt:   state.lastSeen,
			WindowMillis: suppressionWindow.Milliseconds(),
			LoopMode:     state.loopMode,
		})
		state.suppressed = 0
		state.lastAggregateAt = now
	}
	return aggregates
}

func pruneRecent(values []time.Time, cutoff time.Time) []time.Time {
	index := 0
	for index < len(values) && values[index].Before(cutoff) {
		index++
	}
	if index == 0 {
		return values
	}
	trimmed := make([]time.Time, len(values)-index)
	copy(trimmed, values[index:])
	return trimmed
}
