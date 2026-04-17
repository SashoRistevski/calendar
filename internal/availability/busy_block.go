package availability

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

type BusyBlock struct {
	Start   time.Time
	End     time.Time
	Summary string
	Band    string
	Phone   string
	Email   string
}

func (b BusyBlock) Slot() Slot {
	return Slot{Start: b.Start, End: b.End}
}

var rehearsalSummary = regexp.MustCompile(`(?i)REHEARSAL:\s*(.+)\s*\(([^)]+)\)\s*$`)
var emailInText = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

func textProp(ch *ical.Component, name string) string {
	if ch == nil {
		return ""
	}
	p := ch.Props.Get(name)
	if p == nil {
		return ""
	}
	t, _ := p.Text()
	return strings.TrimSpace(t)
}

func contactFromVEVENT(ch *ical.Component) (band, phone, email, rawSummary string) {
	rawSummary = textProp(ch, "SUMMARY")
	desc := textProp(ch, "DESCRIPTION")
	if m := emailInText.FindString(desc); m != "" {
		email = strings.TrimSpace(m)
	}
	if m := rehearsalSummary.FindStringSubmatch(rawSummary); len(m) == 3 {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), email, rawSummary
	}
	band = strings.TrimSpace(rawSummary)
	return band, "", email, rawSummary
}

func extractBusyBlocks(objects []caldav.CalendarObject, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) []BusyBlock {
	var blocks []BusyBlock
	for _, obj := range objects {
		if obj.Data == nil {
			continue
		}
		blocks = append(blocks, extractBusyBlocksFromRoot(obj.Data, queryStart, queryEnd, eventParseLoc, skipTransparent)...)
	}
	return dedupeBusyBlocks(blocks)
}

func extractBusyBlocksFromRoot(root *ical.Calendar, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) []BusyBlock {
	if eventParseLoc == nil {
		eventParseLoc = time.UTC
	}
	var blocks []BusyBlock
	walkComponents(root, func(ch *ical.Component) {
		blocks = append(blocks, busyBlocksFromVEVENT(ch, queryStart, queryEnd, eventParseLoc, skipTransparent)...)
	})
	return blocks
}

func busyBlocksFromVEVENT(ch *ical.Component, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) []BusyBlock {
	if ch == nil || ch.Name != ical.CompEvent {
		return nil
	}
	if p := ch.Props.Get(ical.PropStatus); p != nil {
		if st, _ := p.Text(); strings.EqualFold(strings.TrimSpace(st), string(ical.EventCancelled)) {
			return nil
		}
	}
	if skipTransparent {
		if p := ch.Props.Get(ical.PropTransparency); p != nil {
			if tr, _ := p.Text(); strings.EqualFold(strings.TrimSpace(tr), "TRANSPARENT") {
				return nil
			}
		}
	}
	start, err := ch.Props.DateTime(ical.PropDateTimeStart, eventParseLoc)
	if err != nil || start.IsZero() {
		if calDebug() {
			log.Printf("availability: skip VEVENT DateTimeStart err=%v zero=%v", err, start.IsZero())
		}
		return nil
	}
	end, err := eventEndForSlot(ch, start, eventParseLoc)
	if err != nil || !end.After(start) {
		if calDebug() {
			log.Printf("availability: skip VEVENT end err=%v start=%v end=%v", err, start, end)
		}
		return nil
	}
	dur := end.Sub(start)
	band, phone, email, summary := contactFromVEVENT(ch)

	rr, rerr := ch.Props.RecurrenceRule()
	if rerr == nil && rr != nil {
		set, rsErr := ch.RecurrenceSet(eventParseLoc)
		if rsErr != nil {
			if calDebug() {
				log.Printf("availability: RecurrenceSet err=%v", rsErr)
			}
		} else if set != nil {
			instances := set.Between(queryStart, queryEnd, true)
			if len(instances) > maxRecurrenceInstances {
				instances = instances[:maxRecurrenceInstances]
			}
			out := make([]BusyBlock, 0, len(instances))
			for _, t := range instances {
				tEnd := t.Add(dur)
				if intervalOverlaps(t, tEnd, queryStart, queryEnd) {
					out = append(out, BusyBlock{
						Start: t, End: tEnd,
						Summary: summary, Band: band, Phone: phone, Email: email,
					})
				}
			}
			return out
		}
	}

	if !intervalOverlaps(start, end, queryStart, queryEnd) {
		return nil
	}
	return []BusyBlock{{
		Start: start, End: end,
		Summary: summary, Band: band, Phone: phone, Email: email,
	}}
}

func dedupeBusyBlocks(blocks []BusyBlock) []BusyBlock {
	if len(blocks) < 2 {
		return blocks
	}
	seen := make(map[string]struct{}, len(blocks))
	out := make([]BusyBlock, 0, len(blocks))
	for _, b := range blocks {
		k := b.Start.UTC().Format(time.RFC3339Nano) + "|" + b.End.UTC().Format(time.RFC3339Nano)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, b)
	}
	return out
}

func blocksToSlots(blocks []BusyBlock) []Slot {
	out := make([]Slot, len(blocks))
	for i := range blocks {
		out[i] = blocks[i].Slot()
	}
	return out
}

func FetchBusyBlocks(ctx context.Context, client *caldav.Client, calendarPath string, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) ([]BusyBlock, error) {
	if !queryStart.Before(queryEnd) {
		return nil, fmt.Errorf("invalid query range")
	}
	if eventParseLoc == nil {
		eventParseLoc = time.UTC
	}
	startUTC := queryStart.UTC()
	endUTC := queryEnd.UTC()

	queryPlain := calendarQuery(startUTC, endUTC, false)
	objectsPlain, err := client.QueryCalendar(ctx, calendarPath, queryPlain)
	if err != nil {
		return nil, err
	}
	if calDebug() {
		log.Printf("caldav: calendar-query (plain) objects=%d", len(objectsPlain))
	}
	objectsPlain = HydrateCalendarObjectsIfNeeded(ctx, client, objectsPlain)

	blocks := extractBusyBlocks(objectsPlain, queryStart, queryEnd, eventParseLoc, skipTransparent)
	if len(blocks) == 0 {
		queryExpanded := calendarQuery(startUTC, endUTC, true)
		objects, err2 := client.QueryCalendar(ctx, calendarPath, queryExpanded)
		if err2 == nil {
			if calDebug() {
				log.Printf("caldav: calendar-query (expand) objects=%d", len(objects))
			}
			objects = HydrateCalendarObjectsIfNeeded(ctx, client, objects)
			blocks = extractBusyBlocks(objects, queryStart, queryEnd, eventParseLoc, skipTransparent)
		}
	}

	if len(blocks) == 0 {
		listBlocks, err3 := fetchBusyBlocksByListing(ctx, client, calendarPath, queryStart, queryEnd, eventParseLoc, skipTransparent)
		if err3 != nil {
			if calDebug() {
				log.Printf("caldav: list+get fallback err=%v", err3)
			}
		} else {
			if calDebug() {
				log.Printf("caldav: list+get resources parsed blocks=%d", len(listBlocks))
			}
			blocks = listBlocks
		}
	}

	sortBusyBlocks(blocks)
	return blocks, nil
}

func fetchBusyBlocksByListing(ctx context.Context, client *caldav.Client, calendarPath string, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) ([]BusyBlock, error) {
	files, err := client.ReadDir(ctx, calendarPath, false)
	if err != nil {
		return nil, err
	}
	if calDebug() {
		log.Printf("caldav: ReadDir depth1 entries=%d path=%q", len(files), calendarPath)
	}
	if countFileResources(files, calendarPath) == 0 {
		deep, err2 := client.ReadDir(ctx, calendarPath, true)
		if err2 == nil && len(deep) > 0 {
			files = deep
			if calDebug() {
				log.Printf("caldav: ReadDir recursive entries=%d", len(files))
			}
		}
	}

	var blocks []BusyBlock
	const maxObjects = 800
	n := 0
	for _, fi := range files {
		if n >= maxObjects {
			break
		}
		if fi.IsDir {
			continue
		}
		p := fi.Path
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = strings.TrimSuffix(calendarPath, "/") + "/" + strings.TrimPrefix(p, "/")
		}
		if p == strings.TrimSuffix(calendarPath, "/") || p == calendarPath {
			continue
		}

		obj, err := client.GetCalendarObject(ctx, p)
		if err != nil {
			if calDebug() {
				log.Printf("caldav: GetCalendarObject skip path=%q err=%v", p, err)
			}
			continue
		}
		n++
		if obj.Data == nil {
			continue
		}
		blocks = append(blocks, extractBusyBlocks([]caldav.CalendarObject{*obj}, queryStart, queryEnd, eventParseLoc, skipTransparent)...)
	}
	return blocks, nil
}

func sortBusyBlocks(blocks []BusyBlock) {
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Start.Before(blocks[j].Start)
	})
}
