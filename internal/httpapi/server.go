package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/emersion/go-webdav/caldav"

	"github.com/sasho/calendar-availability-proxy/internal/availability"
	"github.com/sasho/calendar-availability-proxy/internal/diagnostics"
)

//go:embed web/*
var webFS embed.FS

// CalDAVDiagnostics enables GET /api/diagnostics/caldav (set CALDAV_DIAGNOSTICS=1). Do not enable on public URLs.
type CalDAVDiagnostics struct {
	Enabled         bool
	Client          *caldav.Client
	Path            string
	EventParseLoc   *time.Location
	SkipTransparent bool
}

type Server struct {
	src        *availability.CachedSource
	skopje     *time.Location
	ipLimiters sync.Map
	rps        rate.Limit
	burst      int
	diag       CalDAVDiagnostics
}

func New(src *availability.CachedSource, skopje *time.Location, rps float64, burst int, diag CalDAVDiagnostics) *Server {
	return &Server{
		src:    src,
		skopje: skopje,
		rps:    rate.Limit(rps),
		burst:  burst,
		diag:   diag,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/availability", s.withRateLimit(http.HandlerFunc(s.handleAvailability)))
	mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	if s.diag.Enabled && s.diag.Client != nil && s.diag.Path != "" {
		mux.Handle("GET /api/diagnostics/caldav", http.HandlerFunc(s.handleCalDAVDiagnostics))
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("GET /", http.FileServerFS(static))

	return mux
}

func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		limiter := s.perIPLimiter(host)
		if !limiter.Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) perIPLimiter(ip string) *rate.Limiter {
	if v, ok := s.ipLimiters.Load(ip); ok {
		return v.(*rate.Limiter)
	}
	l := rate.NewLimiter(s.rps, s.burst)
	actual, _ := s.ipLimiters.LoadOrStore(ip, l)
	return actual.(*rate.Limiter)
}

type slotDTO struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()

	// Bypass server-side TTL when the client asks for a fresh pull (e.g. UI Refresh).
	// Still subject to per-IP rate limiting.
	q := r.URL.Query()
	if q.Get("fresh") == "1" || q.Get("refresh") == "1" {
		s.src.Invalidate()
	}

	qs, qe := availability.QueryWindow(s.skopje, time.Now())
	slots, err := s.src.Slots(ctx, qs, qe)
	if err != nil {
		http.Error(w, "upstream calendar unavailable", http.StatusBadGateway)
		return
	}

	now := time.Now().In(s.skopje)
	out := make([]slotDTO, 0, len(slots))
	for _, sl := range slots {
		if !sl.End.After(now) {
			continue
		}
		out = append(out, slotDTO{
			Start: sl.Start.In(s.skopje).Format(time.RFC3339),
			End:   sl.End.In(s.skopje).Format(time.RFC3339),
		})
	}

	// Europe/Skopje calendar dates for FullCalendar validRange (end is exclusive, matches QueryWindow upper bound).
	w.Header().Set("X-Availability-Window-Start-Date", qs.In(s.skopje).Format("2006-01-02"))
	w.Header().Set("X-Availability-Window-End-Date", qe.In(s.skopje).Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json")
	if q.Get("fresh") == "1" || q.Get("refresh") == "1" {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("encode: %v", err)
	}
}

func (s *Server) handleCalDAVDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	rep := diagnostics.CalDAVProbe(ctx, s.diag.Client, s.diag.Path, s.skopje)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(rep); err != nil {
		log.Printf("diagnostics encode: %v", err)
	}
}

