package diagnostics

import (
	"context"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/sasho/calendar-availability-proxy/internal/availability"
)

type Report struct {
	OK                  bool                `json:"ok"`
	CalendarPath        string              `json:"calendar_path"`
	QueryWindow         WindowUTC           `json:"query_window_utc"`
	Principal           Step                `json:"principal"`
	CalendarHome        Step                `json:"calendar_home"`
	Collection          CollectionStep      `json:"collection"`
	ReadDir             ReadDirStep         `json:"read_dir"`
	QueryExpand         QueryStep           `json:"calendar_query_expand"`
	QueryPlain          QueryStep           `json:"calendar_query_plain"`
	AvailabilityPreview AvailabilityPreview `json:"availability_preview"`
	FirstResource       *ResourceProbe      `json:"first_calendar_resource,omitempty"`
}

type AvailabilityPreview struct {
	SlotsFloatingAsUTC      int `json:"slots_floating_as_utc"`
	SlotsFloatingAsWindowTZ int `json:"slots_floating_as_window_tz"`
}

type WindowUTC struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type Step struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

type CollectionStep struct {
	OK    bool   `json:"ok"`
	IsDir bool   `json:"is_dir,omitempty"`
	Error string `json:"error,omitempty"`
}

type ReadDirStep struct {
	OK          bool     `json:"ok"`
	EntryCount  int      `json:"entry_count"`
	FileCount   int      `json:"file_count"`
	SamplePaths []string `json:"sample_paths,omitempty"`
	Error       string   `json:"error,omitempty"`
	Note        string   `json:"note,omitempty"`
}

type QueryStep struct {
	OK           bool   `json:"ok"`
	ObjectCount  int    `json:"object_count"`
	WithCalendar int    `json:"objects_with_parsed_calendar"`
	Error        string `json:"error,omitempty"`
}

type ResourceProbe struct {
	Path          string   `json:"path"`
	OK            bool     `json:"get_ok"`
	Error         string   `json:"error,omitempty"`
	TopChildNames []string `json:"vcalendar_child_names,omitempty"`
	VEVENTCount   int      `json:"vevent_component_count"`
}

func CalDAVProbe(ctx context.Context, client *caldav.Client, calendarPath string, loc *time.Location) Report {
	r := Report{CalendarPath: calendarPath}
	qs, qe := availability.QueryWindow(loc, time.Now())
	r.QueryWindow = WindowUTC{Start: qs.UTC().Format(time.RFC3339), End: qe.UTC().Format(time.RFC3339)}
	startUTC := qs.UTC()
	endUTC := qe.UTC()

	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		r.Principal = Step{OK: false, Error: err.Error()}
		return r
	}
	r.Principal = Step{OK: true, Path: principal}

	home, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		r.CalendarHome = Step{OK: false, Error: err.Error()}
		return r
	}
	r.CalendarHome = Step{OK: true, Path: home}

	fi, err := client.Stat(ctx, calendarPath)
	if err != nil {
		r.Collection = CollectionStep{OK: false, Error: err.Error()}
	} else {
		r.Collection = CollectionStep{OK: true, IsDir: fi.IsDir}
	}

	files, err := client.ReadDir(ctx, calendarPath, false)
	var firstFilePath string
	if err != nil {
		r.ReadDir = ReadDirStep{OK: false, Error: err.Error()}
	} else {
		rd := ReadDirStep{OK: true, EntryCount: len(files)}
		const maxSample = 12
		for _, f := range files {
			if f.IsDir {
				continue
			}
			rd.FileCount++
			if f.Path != "" && firstFilePath == "" {
				firstFilePath = f.Path
			}
			if len(rd.SamplePaths) < maxSample && f.Path != "" {
				rd.SamplePaths = append(rd.SamplePaths, f.Path)
			}
		}
		r.ReadDir = rd
	}

	qExp := availability.ExpandedCalendarQuery(startUTC, endUTC)
	objs, err := client.QueryCalendar(ctx, calendarPath, qExp)
	if err != nil {
		r.QueryExpand = QueryStep{OK: false, Error: err.Error()}
	} else {
		r.QueryExpand = QueryStep{OK: true, ObjectCount: len(objs), WithCalendar: countWithData(objs)}
	}

	qPl := availability.PlainCalendarQuery(startUTC, endUTC)
	objs2, err := client.QueryCalendar(ctx, calendarPath, qPl)
	if err != nil {
		r.QueryPlain = QueryStep{OK: false, Error: err.Error()}
	} else {
		objs2 = availability.HydrateCalendarObjectsIfNeeded(ctx, client, objs2)
		r.QueryPlain = QueryStep{OK: true, ObjectCount: len(objs2), WithCalendar: countWithData(objs2)}
		r.AvailabilityPreview.SlotsFloatingAsUTC = availability.ExtractedSlotCount(objs2, qs, qe, time.UTC, false)
		if loc != nil {
			r.AvailabilityPreview.SlotsFloatingAsWindowTZ = availability.ExtractedSlotCount(objs2, qs, qe, loc, false)
		}
	}

	if r.ReadDir.OK && r.ReadDir.FileCount > 0 && firstFilePath != "" {
		r.FirstResource = probeFirstFile(ctx, client, calendarPath, firstFilePath)
	}

	r.OK = r.Principal.OK && r.CalendarHome.OK && r.Collection.OK
	if !r.ReadDir.OK && r.QueryPlain.OK && r.QueryPlain.ObjectCount > 0 {
		r.ReadDir.Note = "iCloud often returns 404 for DAV:resourcetype when listing a calendar collection; calendar-query REPORT still works — safe to ignore read_dir for iCloud."
	}
	return r
}

func countWithData(objs []caldav.CalendarObject) int {
	n := 0
	for _, o := range objs {
		if o.Data != nil {
			n++
		}
	}
	return n
}

func probeFirstFile(ctx context.Context, client *caldav.Client, calendarPath, p string) *ResourceProbe {
	out := &ResourceProbe{Path: p}
	if p == "" {
		out.Error = "empty path"
		return out
	}
	obj, err := client.GetCalendarObject(ctx, p)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.OK = true
	if obj.Data == nil {
		return out
	}
	cal := obj.Data
	for _, ch := range cal.Children {
		out.TopChildNames = append(out.TopChildNames, ch.Name)
	}
	out.VEVENTCount = countVEVENTsInCalendar(cal)
	return out
}

func countVEVENTsInCalendar(cal *ical.Calendar) int {
	if cal == nil {
		return 0
	}
	n := 0
	var walk func(*ical.Component)
	walk = func(comp *ical.Component) {
		if comp == nil {
			return
		}
		if comp.Name == ical.CompEvent {
			n++
		}
		for _, ch := range comp.Children {
			walk(ch)
		}
	}
	for _, ch := range cal.Children {
		walk(ch)
	}
	return n
}
