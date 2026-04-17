package availability

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-webdav/caldav"
)

type CachedSource struct {
	client          *caldav.Client
	calendarPath    string
	ttl             time.Duration
	eventParseLoc   *time.Location
	skipTransparent bool
	mu              sync.RWMutex
	fetchedAt       time.Time
	blocks          []BusyBlock
	fetchErr        error
}

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

func (s *CachedSource) Invalidate() {
	s.mu.Lock()
	s.fetchedAt = time.Time{}
	s.blocks = nil
	s.fetchErr = nil
	s.mu.Unlock()
}

func (s *CachedSource) Slots(ctx context.Context, queryStart, queryEnd time.Time) ([]Slot, error) {
	blocks, err := s.busyBlocks(ctx, queryStart, queryEnd)
	if err != nil {
		return nil, err
	}
	return blocksToSlots(clipBusyBlocks(blocks, queryStart, queryEnd)), nil
}

func (s *CachedSource) BusyBlocks(ctx context.Context, queryStart, queryEnd time.Time) ([]BusyBlock, error) {
	return s.busyBlocks(ctx, queryStart, queryEnd)
}

func (s *CachedSource) busyBlocks(ctx context.Context, queryStart, queryEnd time.Time) ([]BusyBlock, error) {
	s.mu.RLock()
	age := time.Since(s.fetchedAt)
	if !s.fetchedAt.IsZero() && age < s.ttl && s.fetchErr == nil {
		cached := s.blocks
		s.mu.RUnlock()
		return clipBusyBlocks(cached, queryStart, queryEnd), nil
	}
	if s.fetchErr != nil && age < errCooldown {
		err := s.fetchErr
		s.mu.RUnlock()
		return nil, err
	}
	s.mu.RUnlock()

	blocks, err := FetchBusyBlocks(ctx, s.client, s.calendarPath, queryStart, queryEnd, s.eventParseLoc, s.skipTransparent)

	s.mu.Lock()
	s.fetchedAt = time.Now()
	s.fetchErr = err
	if err == nil {
		s.blocks = blocks
	}
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return clipBusyBlocks(blocks, queryStart, queryEnd), nil
}

func clipBusyBlocks(in []BusyBlock, qs, qe time.Time) []BusyBlock {
	out := make([]BusyBlock, 0, len(in))
	for _, b := range in {
		if !intervalOverlaps(b.Start, b.End, qs, qe) {
			continue
		}
		out = append(out, b)
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
