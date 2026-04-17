package booking

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/sasho/calendar-availability-proxy/internal/availability"
)

func PutRehearsal(ctx context.Context, client *caldav.Client, calendarPath string, start, end time.Time, skopje *time.Location, fullName, phone, userEmail string) error {
	if client == nil {
		return fmt.Errorf("nil caldav client")
	}
	calendarPath = availability.NormalizeCalendarPath(calendarPath)
	name := strings.TrimSpace(fullName)
	ph := strings.TrimSpace(phone)
	if name == "" || ph == "" {
		return fmt.Errorf("full name and phone required for calendar title")
	}
	summary := fmt.Sprintf("REHEARSAL: %s (%s)", name, ph)
	desc := strings.TrimSpace(userEmail)

	uid := newUID()

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//studio-porta//booking//EN")

	ev := ical.NewComponent(ical.CompEvent)
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())

	startSk := start.In(skopje)
	endSk := end.In(skopje)
	ev.Props.SetDateTime(ical.PropDateTimeStart, startSk)
	ev.Props.SetDateTime(ical.PropDateTimeEnd, endSk)
	ev.Props.SetText(ical.PropSummary, summary)
	if desc != "" {
		ev.Props.SetText(ical.PropDescription, desc)
	}

	cal.Children = append(cal.Children, ev)

	var fn [16]byte
	_, _ = rand.Read(fn[:])
	filename := hex.EncodeToString(fn[:]) + ".ics"
	path := strings.TrimSuffix(calendarPath, "/") + "/" + filename

	_, err := client.PutCalendarObject(ctx, path, cal)
	return err
}

func newUID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) + "@studio.local"
}
