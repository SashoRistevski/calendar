package availability

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

type Slot struct {
	Start time.Time
	End   time.Time
}

func calDebug() bool { return os.Getenv("DEBUG_CALDAV") == "1" }

// PlainCalendarQuery builds a calendar REPORT body without recurrence expand (for diagnostics / picky servers).
func PlainCalendarQuery(startUTC, endUTC time.Time) *caldav.CalendarQuery {
	return calendarQuery(startUTC, endUTC, false)
}

// ExpandedCalendarQuery builds a calendar REPORT body with recurrence expand.
func ExpandedCalendarQuery(startUTC, endUTC time.Time) *caldav.CalendarQuery {
	return calendarQuery(startUTC, endUTC, true)
}

// HydrateCalendarObjectsIfNeeded re-fetches each object with GET when calendar-query returns VEVENTs without DTSTART (e.g. iCloud).
func HydrateCalendarObjectsIfNeeded(ctx context.Context, client *caldav.Client, objs []caldav.CalendarObject) []caldav.CalendarObject {
	if client == nil || len(objs) == 0 {
		return objs
	}
	out := make([]caldav.CalendarObject, len(objs))
	for i := range objs {
		obj := objs[i]
		if !calendarQueryEventLacksDTSTART(obj) || obj.Path == "" {
			out[i] = obj
			continue
		}
		full, err := client.GetCalendarObject(ctx, obj.Path)
		if err == nil && full != nil && full.Data != nil {
			if calDebug() {
				log.Printf("caldav: hydrated %q via GET (query had VEVENT without DTSTART)", obj.Path)
			}
			out[i] = *full
			continue
		}
		if calDebug() {
			log.Printf("caldav: hydrate GET %q err=%v", obj.Path, err)
		}
		out[i] = obj
	}
	return out
}

func calendarQueryEventLacksDTSTART(obj caldav.CalendarObject) bool {
	if obj.Data == nil {
		return false
	}
	var anyEvent, missing bool
	walkComponents(obj.Data, func(ch *ical.Component) {
		if ch.Name != ical.CompEvent {
			return
		}
		anyEvent = true
		if ch.Props.Get(ical.PropDateTimeStart) == nil {
			missing = true
		}
	})
	return anyEvent && missing
}

func FetchSlots(ctx context.Context, client *caldav.Client, calendarPath string, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) ([]Slot, error) {
	if !queryStart.Before(queryEnd) {
		return nil, fmt.Errorf("invalid query range")
	}
	if eventParseLoc == nil {
		eventParseLoc = time.UTC
	}
	startUTC := queryStart.UTC()
	endUTC := queryEnd.UTC()

	// Plain query first: iCloud sometimes returns expanded REPORT payloads that parse but yield no usable VEVENT instants; plain matches a typical .ics export shape (see rehearsal-calculator).
	queryPlain := calendarQuery(startUTC, endUTC, false)
	objectsPlain, err := client.QueryCalendar(ctx, calendarPath, queryPlain)
	if err != nil {
		return nil, err
	}
	if calDebug() {
		log.Printf("caldav: calendar-query (plain) objects=%d", len(objectsPlain))
	}
	objectsPlain = HydrateCalendarObjectsIfNeeded(ctx, client, objectsPlain)

	slots := extractSlots(objectsPlain, queryStart, queryEnd, eventParseLoc, skipTransparent)
	if len(slots) == 0 {
		queryExpanded := calendarQuery(startUTC, endUTC, true)
		objects, err2 := client.QueryCalendar(ctx, calendarPath, queryExpanded)
		if err2 == nil {
			if calDebug() {
				log.Printf("caldav: calendar-query (expand) objects=%d", len(objects))
			}
			objects = HydrateCalendarObjectsIfNeeded(ctx, client, objects)
			slots = extractSlots(objects, queryStart, queryEnd, eventParseLoc, skipTransparent)
		}
	}

	if len(slots) == 0 {
		listSlots, err3 := fetchSlotsByListing(ctx, client, calendarPath, queryStart, queryEnd, eventParseLoc, skipTransparent)
		if err3 != nil {
			if calDebug() {
				log.Printf("caldav: list+get fallback err=%v", err3)
			}
		} else {
			if calDebug() {
				log.Printf("caldav: list+get resources parsed slots=%d", len(listSlots))
			}
			slots = listSlots
		}
	}

	sort.Slice(slots, func(i, j int) bool { return slots[i].Start.Before(slots[j].Start) })
	return slots, nil
}

func fetchSlotsByListing(ctx context.Context, client *caldav.Client, calendarPath string, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) ([]Slot, error) {
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

	var slots []Slot
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
		slots = append(slots, extractSlots([]caldav.CalendarObject{*obj}, queryStart, queryEnd, eventParseLoc, skipTransparent)...)
	}
	return slots, nil
}

func countFileResources(files []webdav.FileInfo, calendarPath string) int {
	n := 0
	for _, fi := range files {
		if fi.IsDir {
			continue
		}
		p := fi.Path
		if p == "" || p == strings.TrimSuffix(calendarPath, "/") || p == calendarPath {
			continue
		}
		n++
	}
	return n
}

func calendarQuery(startUTC, endUTC time.Time, expand bool) *caldav.CalendarQuery {
	cr := caldav.CalendarCompRequest{
		Name: ical.CompCalendar,
		Comps: []caldav.CalendarCompRequest{
			{Name: ical.CompEvent, AllProps: true},
		},
	}
	if expand {
		cr.Expand = &caldav.CalendarExpandRequest{Start: startUTC, End: endUTC}
	}
	return &caldav.CalendarQuery{
		CompRequest: cr,
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{
				{Name: ical.CompEvent, Start: startUTC, End: endUTC},
			},
		},
	}
}

// ExtractedSlotCount is how many slots /api/availability would return for these objects (diagnostics only).
func ExtractedSlotCount(objects []caldav.CalendarObject, queryStart, queryEnd time.Time, parseLoc *time.Location, skipTransparent bool) int {
	if parseLoc == nil {
		parseLoc = time.UTC
	}
	return len(extractSlots(objects, queryStart, queryEnd, parseLoc, skipTransparent))
}

func extractSlots(objects []caldav.CalendarObject, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) []Slot {
	var slots []Slot
	for _, obj := range objects {
		if obj.Data == nil {
			continue
		}
		slots = append(slots, extractSlotsFromRoot(obj.Data, queryStart, queryEnd, eventParseLoc, skipTransparent)...)
	}
	return dedupeSlots(slots)
}

func extractSlotsFromRoot(root *ical.Calendar, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) []Slot {
	if eventParseLoc == nil {
		eventParseLoc = time.UTC
	}
	var slots []Slot
	walkComponents(root, func(ch *ical.Component) {
		slots = append(slots, slotsFromVEVENT(ch, queryStart, queryEnd, eventParseLoc, skipTransparent)...)
	})
	return slots
}

const maxRecurrenceInstances = 2048

// slotsFromVEVENT returns busy intervals for one VEVENT, expanding RRULE into the query window when needed.
func slotsFromVEVENT(ch *ical.Component, queryStart, queryEnd time.Time, eventParseLoc *time.Location, skipTransparent bool) []Slot {
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
			out := make([]Slot, 0, len(instances))
			for _, t := range instances {
				tEnd := t.Add(dur)
				if intervalOverlaps(t, tEnd, queryStart, queryEnd) {
					out = append(out, Slot{Start: t, End: tEnd})
				}
			}
			return out
		}
	}

	if !intervalOverlaps(start, end, queryStart, queryEnd) {
		return nil
	}
	return []Slot{{Start: start, End: end}}
}

func dedupeSlots(slots []Slot) []Slot {
	if len(slots) < 2 {
		return slots
	}
	seen := make(map[string]struct{}, len(slots))
	out := make([]Slot, 0, len(slots))
	for _, s := range slots {
		k := s.Start.UTC().Format(time.RFC3339Nano) + "|" + s.End.UTC().Format(time.RFC3339Nano)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}

func walkComponents(c *ical.Calendar, fn func(*ical.Component)) {
	if c == nil {
		return
	}
	for _, ch := range c.Children {
		fn(ch)
		walkChildComponents(ch, fn)
	}
}

func walkChildComponents(comp *ical.Component, fn func(*ical.Component)) {
	for _, ch := range comp.Children {
		fn(ch)
		walkChildComponents(ch, fn)
	}
}

// eventEndForSlot resolves DTEND / DURATION / all-day rules using eventLoc for floating times.
func eventEndForSlot(comp *ical.Component, start time.Time, eventLoc *time.Location) (time.Time, error) {
	if eventLoc == nil {
		eventLoc = time.UTC
	}
	end, err := comp.Props.DateTime(ical.PropDateTimeEnd, eventLoc)
	if err != nil {
		return time.Time{}, err
	}
	if !end.IsZero() {
		return end, nil
	}
	if p := comp.Props.Get(ical.PropDuration); p != nil {
		d, err := p.Duration()
		if err != nil {
			return time.Time{}, err
		}
		return start.Add(d), nil
	}
	if p := comp.Props.Get(ical.PropDateTimeStart); p != nil && p.ValueType() == ical.ValueDate {
		return start.AddDate(0, 0, 1), nil
	}
	return time.Time{}, fmt.Errorf("no DTEND or DURATION")
}
