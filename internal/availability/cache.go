package availability

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-webdav/caldav"
)

type CachedSource struct {
	client           *caldav.Client
	calendarPath     string
	ttl              time.Duration
	eventParseLoc    *time.Location
	skipTransparent  bool
	mu               sync.RWMutex
	fetchedAt        time.Time
	slots            []Slot
	fetchErr         error
}

// eventParseLoc is used for floating ICS times (no TZID / no Z). Default UTC matches SashoRistevski/rehearsal-calculator.
func NewCachedSource(client *caldav.Client, calendarPath string, ttl time.Duration, eventParseLoc *time.Location, skipTransparent bool) *CachedSource {
	return &CachedSource{
		client:          client,
		calendarPath:    calendarPath,
		ttl:             ttl,
		eventParseLoc:   eventParseLoc,
		skipTransparent: skipTransparent,
	}
}

const errCooldown = 30 * time.Second

// Invalidate drops the in-memory copy so the next Slots call refetches from CalDAV.
func (s *CachedSource) Invalidate() {
	s.mu.Lock()
	s.fetchedAt = time.Time{}
	s.slots = nil
	s.fetchErr = nil
	s.mu.Unlock()
}

func (s *CachedSource) Slots(ctx context.Context, queryStart, queryEnd time.Time) ([]Slot, error) {
	s.mu.RLock()
	age := time.Since(s.fetchedAt)
	if !s.fetchedAt.IsZero() && age < s.ttl && s.fetchErr == nil {
		cached := s.slots
		s.mu.RUnlock()
		return clipSlots(cached, queryStart, queryEnd), nil
	}
	if s.fetchErr != nil && age < errCooldown {
		err := s.fetchErr
		s.mu.RUnlock()
		return nil, err
	}
	s.mu.RUnlock()

	slots, err := FetchSlots(ctx, s.client, s.calendarPath, queryStart, queryEnd, s.eventParseLoc, s.skipTransparent)

	s.mu.Lock()
	s.fetchedAt = time.Now()
	s.fetchErr = err
	if err == nil {
		s.slots = slots
	}
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return clipSlots(slots, queryStart, queryEnd), nil
}

func clipSlots(in []Slot, qs, qe time.Time) []Slot {
	out := make([]Slot, 0, len(in))
	for _, sl := range in {
		if !intervalOverlaps(sl.Start, sl.End, qs, qe) {
			continue
		}
		out = append(out, sl)
	}
	return out
}

func intervalOverlaps(start, end, winStart, winEnd time.Time) bool {
	return end.After(winStart) && start.Before(winEnd)
}

func NormalizeCalendarPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if !strings.HasSuffix(p, "/") {
		return p + "/"
	}
	return p
}
