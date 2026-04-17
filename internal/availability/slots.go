package availability

import (
	"context"
	"fmt"
	"log"
	"os"
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

func PlainCalendarQuery(startUTC, endUTC time.Time) *caldav.CalendarQuery {
	return calendarQuery(startUTC, endUTC, false)
}

func ExpandedCalendarQuery(startUTC, endUTC time.Time) *caldav.CalendarQuery {
	return calendarQuery(startUTC, endUTC, true)
}

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
	blocks, err := FetchBusyBlocks(ctx, client, calendarPath, queryStart, queryEnd, eventParseLoc, skipTransparent)
	if err != nil {
		return nil, err
	}
	return blocksToSlots(blocks), nil
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

func ExtractedSlotCount(objects []caldav.CalendarObject, queryStart, queryEnd time.Time, parseLoc *time.Location, skipTransparent bool) int {
	if parseLoc == nil {
		parseLoc = time.UTC
	}
	return len(extractBusyBlocks(objects, queryStart, queryEnd, parseLoc, skipTransparent))
}

const maxRecurrenceInstances = 2048

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
