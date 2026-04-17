package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCalDAVBase = "https://caldav.icloud.com"
	DefaultCacheTTL   = 90 * time.Second
)

type Config struct {
	ICLOUDEmail       string
	ICLOUDAppPassword string
	CalendarID        string
	CalDAVBase        string
	CacheTTL          time.Duration
	HTTPAddr          string
	RatePerSecond     float64
	RateBurst         int
	CalDAVDiagnostics bool
	EventParseLoc     *time.Location
	SkipTransparent   bool
}

func Load() (Config, error) {
	c := Config{
		ICLOUDEmail:       os.Getenv("ICLOUD_EMAIL"),
		ICLOUDAppPassword: os.Getenv("ICLOUD_APP_PASSWORD"),
		CalendarID:        os.Getenv("CALENDAR_ID"),
		CalDAVBase:        getenvDefault("CALDAV_BASE_URL", DefaultCalDAVBase),
		HTTPAddr:          listenAddr(),
		RatePerSecond:     0.5,
		RateBurst:         8,
		CacheTTL:          DefaultCacheTTL,
	}
	if v := os.Getenv("CACHE_TTL_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("CACHE_TTL_SECONDS: %w", err)
		}
		c.CacheTTL = time.Duration(n) * time.Second
	}
	if v := os.Getenv("RATE_PER_SECOND"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return Config{}, fmt.Errorf("RATE_PER_SECOND: %w", err)
		}
		c.RatePerSecond = f
	}
	if v := os.Getenv("RATE_BURST"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("RATE_BURST: %w", err)
		}
		c.RateBurst = n
	}
	if c.ICLOUDEmail == "" || c.ICLOUDAppPassword == "" {
		return Config{}, fmt.Errorf("ICLOUD_EMAIL and ICLOUD_APP_PASSWORD are required")
	}
	if c.CalendarID == "" {
		return Config{}, fmt.Errorf("CALENDAR_ID is required (full calendar collection path, see SETUP.md)")
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CALDAV_DIAGNOSTICS")))
	c.CalDAVDiagnostics = v == "1" || v == "true" || v == "yes"

	c.EventParseLoc = time.UTC
	if z := strings.TrimSpace(os.Getenv("EVENT_PARSE_TIMEZONE")); z != "" {
		loc, err := time.LoadLocation(z)
		if err != nil {
			return Config{}, fmt.Errorf("EVENT_PARSE_TIMEZONE: %w", err)
		}
		c.EventParseLoc = loc
	}

	st := strings.ToLower(strings.TrimSpace(os.Getenv("SKIP_TRANSPARENT")))
	c.SkipTransparent = st == "1" || st == "true" || st == "yes"

	return c, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func listenAddr() string {
	if v := os.Getenv("HTTP_ADDR"); v != "" {
		return v
	}
	p := getenvDefault("PORT", "8080")
	if strings.HasPrefix(p, ":") {
		return p
	}
	return ":" + p
}
