package httpapi

import "github.com/emersion/go-webdav/caldav"

type StudioDeps struct {
	JWTSecret           string
	SupabaseURL         string
	SupabaseServiceRole string
	SupabaseAnonKey     string
	AppPublicURL        string
	BrevoAPIKey         string
	CalDAV              *caldav.Client
	CalendarPath        string
}
