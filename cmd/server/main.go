package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata"

	"github.com/emersion/go-webdav/caldav"

	"github.com/sasho/calendar-availability-proxy/internal/availability"
	"github.com/sasho/calendar-availability-proxy/internal/config"
	"github.com/sasho/calendar-availability-proxy/internal/httpapi"
	"github.com/sasho/calendar-availability-proxy/internal/icloud"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	loc, err := time.LoadLocation("Europe/Skopje")
	if err != nil {
		log.Fatal(err)
	}

	client, err := icloud.NewCalDAVClient(cfg.ICLOUDEmail, cfg.ICLOUDAppPassword, cfg.CalDAVBase)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	rawPath, err := icloud.ResolveCalendarPath(ctx, client, cfg.CalendarID)
	cancel()
	if err != nil {
		log.Fatalf("calendar path: %v", err)
	}
	path := availability.NormalizeCalendarPath(rawPath)

	src := availability.NewCachedSource(client, path, cfg.CacheTTL, cfg.EventParseLoc, cfg.SkipTransparent)
	studio := loadStudioDeps(client, path)
	srv := httpapi.New(src, loc, cfg.RatePerSecond, cfg.RateBurst, httpapi.CalDAVDiagnostics{
		Enabled:         cfg.CalDAVDiagnostics,
		Client:          client,
		Path:            path,
		EventParseLoc:   cfg.EventParseLoc,
		SkipTransparent: cfg.SkipTransparent,
	}, studio)
	if cfg.CalDAVDiagnostics {
		log.Printf("CALDAV_DIAGNOSTICS=1: open GET /api/diagnostics/caldav for a step-by-step CalDAV probe")
	}

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func loadStudioDeps(client *caldav.Client, calendarPath string) *httpapi.StudioDeps {
	return &httpapi.StudioDeps{
		JWTSecret:           strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET")),
		SupabaseURL:         strings.TrimSpace(os.Getenv("SUPABASE_URL")),
		SupabaseServiceRole: strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY")),
		SupabaseAnonKey:     strings.TrimSpace(os.Getenv("SUPABASE_ANON_KEY")),
		AppPublicURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("APP_PUBLIC_URL")), "/"),
		BrevoAPIKey:         strings.TrimSpace(os.Getenv("BREVO_API_KEY")),
		CalDAV:              client,
		CalendarPath:        calendarPath,
	}
}
