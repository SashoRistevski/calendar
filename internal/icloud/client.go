package icloud

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

const DefaultCalDAVBase = "https://caldav.icloud.com"

// iCloud often responds 403 to Go's default User-Agent; mimic Apple Calendar's DAV client.
const calDAVUserAgent = "Mac OS X/15.0 (24A335) CalendarAgent/9.0"

type userAgentRT struct {
	base http.RoundTripper
	ua   string
}

func (t *userAgentRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", t.ua)
	}
	return t.base.RoundTrip(r)
}

func NewCalDAVClient(email, password, baseURL string) (*caldav.Client, error) {
	inner := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &userAgentRT{base: http.DefaultTransport, ua: calDAVUserAgent},
	}
	hc := webdav.HTTPClientWithBasicAuth(inner, email, password)
	return caldav.NewClient(hc, strings.TrimSuffix(baseURL, "/"))
}

func ResolveCalendarPath(ctx context.Context, client *caldav.Client, calendarID string) (string, error) {
	id := strings.TrimSpace(calendarID)
	if id == "" {
		return "", fmt.Errorf("empty calendar id")
	}
	if strings.HasPrefix(id, "/") && strings.Contains(id, "/calendars/") {
		return id, nil
	}

	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("current-user-principal: %w", err)
	}
	home, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("calendar-home-set: %w", err)
	}
	suffix := strings.Trim(id, "/")
	return strings.TrimSuffix(home, "/") + "/" + suffix + "/", nil
}

func ListCalendars(ctx context.Context, client *caldav.Client) ([]caldav.Calendar, error) {
	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("current-user-principal: %w", err)
	}
	home, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("calendar-home-set: %w", err)
	}
	return client.FindCalendars(ctx, home)
}
