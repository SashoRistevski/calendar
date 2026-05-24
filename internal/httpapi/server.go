package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/emersion/go-webdav/caldav"

	"github.com/sasho/calendar-availability-proxy/internal/auth"
	"github.com/sasho/calendar-availability-proxy/internal/availability"
	"github.com/sasho/calendar-availability-proxy/internal/booking"
	"github.com/sasho/calendar-availability-proxy/internal/diagnostics"
	"github.com/sasho/calendar-availability-proxy/internal/mail"
	"github.com/sasho/calendar-availability-proxy/internal/supabase"
)

//go:embed web/*
var webFS embed.FS

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
	studio     *StudioDeps
}

func New(src *availability.CachedSource, skopje *time.Location, rps float64, burst int, diag CalDAVDiagnostics, studio *StudioDeps) *Server {
	return &Server{
		src:    src,
		skopje: skopje,
		rps:    rate.Limit(rps),
		burst:  burst,
		diag:   diag,
		studio: studio,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	corsOK := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	if s.diag.Enabled && s.diag.Client != nil && s.diag.Path != "" {
		mux.Handle("GET /api/diagnostics/caldav", s.withCORS(http.HandlerFunc(s.handleCalDAVDiagnostics)))
		mux.Handle("OPTIONS /api/diagnostics/caldav", s.withCORS(corsOK))
	}

	mux.Handle("GET /api/availability", s.withCORS(s.withRateLimit(http.HandlerFunc(s.handleAvailability))))
	mux.Handle("OPTIONS /api/availability", s.withCORS(corsOK))

	mux.Handle("GET /api/public-config", s.withCORS(http.HandlerFunc(s.handlePublicConfig)))
	mux.Handle("OPTIONS /api/public-config", s.withCORS(corsOK))

	mux.Handle("GET /api/me", s.withCORS(s.withRateLimit(http.HandlerFunc(s.handleMe))))
	mux.Handle("OPTIONS /api/me", s.withCORS(corsOK))

	mux.Handle("PUT /api/profile", s.withCORS(s.withRateLimit(http.HandlerFunc(s.handleProfile))))
	mux.Handle("OPTIONS /api/profile", s.withCORS(corsOK))

	mux.Handle("POST /api/book", s.withCORS(s.withRateLimit(http.HandlerFunc(s.handleBook))))
	mux.Handle("OPTIONS /api/book", s.withCORS(corsOK))

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("GET /{$}", http.FileServerFS(static))

	return mux
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
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

func (s *Server) supabaseAuthParams() (jwtSecret, supabaseURL, anonKey string) {
	if s.studio == nil {
		return "", "", ""
	}
	return strings.TrimSpace(s.studio.JWTSecret),
		strings.TrimSpace(s.studio.SupabaseURL),
		strings.TrimSpace(s.studio.SupabaseAnonKey)
}

// func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
// 	w.Header().Set("Content-Type", "application/json")
// 	w.Header().Set("Cache-Control", "public, max-age=3600")
// 	url, key := "", ""
// 	if s.studio != nil {
// 		url = strings.TrimSpace(s.studio.SupabaseURL)
// 		key = strings.TrimSpace(s.studio.SupabaseAnonKey)
// 	}
// 	_ = json.NewEncoder(w).Encode(map[string]string{
// 		"supabase_url":      url,
// 		"supabase_anon_key": key,
// 	})
// }
func publicAppURL(r *http.Request, configured string) string {
	if u := strings.TrimRight(strings.TrimSpace(configured), "/"); u != "" {
		return u
	}
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	proto := "https"
	if p := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); p != "" {
		proto = strings.ToLower(strings.TrimSpace(strings.Split(p, ",")[0]))
	} else if r.TLS == nil {
		proto = "http"
	}
	return proto + "://" + host
}

func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	url, key, appURL := "", "", ""
	if s.studio != nil {
		url = strings.TrimSpace(s.studio.SupabaseURL)
		key = strings.TrimSpace(s.studio.SupabaseAnonKey)
		appURL = publicAppURL(r, s.studio.AppPublicURL)
	} else {
		appURL = publicAppURL(r, "")
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"supabase_url":      url,
		"supabase_anon_key": key,
		"app_url":           appURL,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if s.studio == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	jwts, url, anon := s.supabaseAuthParams()
	if jwts == "" && (url == "" || anon == "") {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	ac, err := auth.VerifyAccessToken(ctx, jwts, url, anon, r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	out := map[string]interface{}{
		"email":            ac.Email,
		"is_admin":         ac.IsAdmin,
		"full_name":        "",
		"phone_number":     "",
		"profile_complete": ac.IsAdmin,
	}
	if strings.TrimSpace(s.studio.SupabaseServiceRole) != "" && strings.TrimSpace(s.studio.SupabaseURL) != "" {
		prof, perr := supabase.GetProfile(ctx, s.studio.SupabaseURL, s.studio.SupabaseServiceRole, ac.UserID)
		if perr != nil && !errors.Is(perr, supabase.ErrProfileNotFound) {
			log.Printf("me profile: %v", perr)
		}
		if perr == nil {
			out["full_name"] = prof.FullName
			out["phone_number"] = prof.PhoneNumber
			if !ac.IsAdmin {
				out["profile_complete"] = supabase.ProfileComplete(prof)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

type profilePutRequest struct {
	FullName    string `json:"full_name"`
	PhoneNumber string `json:"phone_number"`
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.studio == nil || strings.TrimSpace(s.studio.SupabaseServiceRole) == "" || strings.TrimSpace(s.studio.SupabaseURL) == "" {
		http.Error(w, "profile update not configured", http.StatusServiceUnavailable)
		return
	}
	jwts, supaURL, anon := s.supabaseAuthParams()
	if jwts == "" && (supaURL == "" || anon == "") {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	ac, err := auth.VerifyAccessToken(ctx, jwts, supaURL, anon, r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req profilePutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := supabase.UpdateProfile(ctx, s.studio.SupabaseURL, s.studio.SupabaseServiceRole, ac.UserID, ac.Email, req.FullName, req.PhoneNumber); err != nil {
		log.Printf("profile update: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

type slotPublicDTO struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Title string `json:"title,omitempty"`
}

type slotAdminDTO struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	BandName string `json:"band_name"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
}

func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()

	authz := r.Header.Get("Authorization")
	var ac *auth.Context
	var hadAuth bool
	var authErr error
	jwts, supaURL, anon := s.supabaseAuthParams()
	if jwts != "" || (supaURL != "" && anon != "") {
		ac, hadAuth, authErr = auth.TryParseBearer(ctx, jwts, supaURL, anon, authz)
		if authErr != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	q := r.URL.Query()
	if q.Get("fresh") == "1" || q.Get("refresh") == "1" {
		s.src.Invalidate()
	}

	qs, qe := availability.QueryWindow(s.skopje, time.Now())
	now := time.Now().In(s.skopje)

	w.Header().Set("X-Availability-Window-Start-Date", qs.In(s.skopje).Format("2006-01-02"))
	w.Header().Set("X-Availability-Window-End-Date", qe.In(s.skopje).Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json")
	if q.Get("fresh") == "1" || q.Get("refresh") == "1" {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}

	if hadAuth && ac != nil && ac.IsAdmin {
		blocks, err := s.src.BusyBlocks(ctx, qs, qe)
		if err != nil {
			http.Error(w, "upstream calendar unavailable", http.StatusBadGateway)
			return
		}
		out := make([]slotAdminDTO, 0, len(blocks))
		for _, b := range blocks {
			if !b.End.After(now) {
				continue
			}
			out = append(out, slotAdminDTO{
				Start:    b.Start.In(s.skopje).Format(time.RFC3339),
				End:      b.End.In(s.skopje).Format(time.RFC3339),
				BandName: strings.TrimSpace(b.Band),
				Phone:    strings.TrimSpace(b.Phone),
				Email:    strings.TrimSpace(b.Email),
			})
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Printf("encode: %v", err)
		}
		return
	}

	slots, err := s.src.Slots(ctx, qs, qe)
	if err != nil {
		http.Error(w, "upstream calendar unavailable", http.StatusBadGateway)
		return
	}
	out := make([]slotPublicDTO, 0, len(slots))
	for _, sl := range slots {
		if !sl.End.After(now) {
			continue
		}
		out = append(out, slotPublicDTO{
			Start: sl.Start.In(s.skopje).Format(time.RFC3339),
			End:   sl.End.In(s.skopje).Format(time.RFC3339),
			Title: "Occupied",
		})
	}
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("encode: %v", err)
	}
}

type bookRequest struct {
	Start         string `json:"start"`
	DurationHours int    `json:"duration_hours"`
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.studio == nil || strings.TrimSpace(s.studio.SupabaseServiceRole) == "" {
		http.Error(w, "booking not configured", http.StatusServiceUnavailable)
		return
	}
	jwts, supaURL, anon := s.supabaseAuthParams()
	if jwts == "" && (supaURL == "" || anon == "") {
		http.Error(w, "booking not configured", http.StatusServiceUnavailable)
		return
	}
	ac, err := auth.VerifyAccessToken(r.Context(), jwts, supaURL, anon, r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.studio.CalDAV == nil || strings.TrimSpace(s.studio.CalendarPath) == "" {
		http.Error(w, "server misconfiguration", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req bookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.DurationHours != 2 && req.DurationHours != 3 && req.DurationHours != 4 {
		http.Error(w, "duration_hours must be 2, 3, or 4", http.StatusBadRequest)
		return
	}
	loc := s.skopje
	if loc == nil {
		loc = time.UTC
	}
	start, err := parseBookingStart(strings.TrimSpace(req.Start), loc)
	if err != nil {
		http.Error(w, "invalid start time (RFC3339 with offset/Z, or Europe/Skopje local like 2006-01-02T15:04:05)", http.StatusBadRequest)
		return
	}
	start = start.In(s.skopje)
	end := start.Add(time.Duration(req.DurationHours) * time.Hour)
	now := time.Now().In(s.skopje)
	if !end.After(now) || !start.Before(end) {
		http.Error(w, "invalid time range", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	prof, err := supabase.GetProfile(ctx, s.studio.SupabaseURL, s.studio.SupabaseServiceRole, ac.UserID)
	if err != nil && !errors.Is(err, supabase.ErrProfileNotFound) {
		log.Printf("profile: %v", err)
		http.Error(w, "could not load profile", http.StatusBadGateway)
		return
	}
	if errors.Is(err, supabase.ErrProfileNotFound) {
		prof = supabase.Profile{}
	}
	if !ac.IsAdmin && !supabase.ProfileComplete(prof) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code":    "profile_incomplete",
			"message": "Add your full name and phone number in your studio profile before booking.",
		})
		return
	}
	name, phone := bookingNamePhone(ac, prof)
	userEmail := strings.TrimSpace(ac.Email)

	s.src.Invalidate()
	qs, qe := availability.QueryWindow(s.skopje, time.Now())
	blocks, err := s.src.BusyBlocks(ctx, qs, qe)
	if err != nil {
		http.Error(w, "upstream calendar unavailable", http.StatusBadGateway)
		return
	}
	for _, b := range blocks {
		if intervalOverlap(b.Start, b.End, start, end) {
			http.Error(w, "slot no longer available", http.StatusConflict)
			return
		}
	}

	if err := booking.PutRehearsal(ctx, s.studio.CalDAV, s.studio.CalendarPath, start, end, s.skopje, name, phone, userEmail); err != nil {
		log.Printf("put rehearsal: %v", err)
		http.Error(w, "could not create booking", http.StatusBadGateway)
		return
	}
	s.src.Invalidate()

	if s.studio.BrevoAPIKey != "" && userEmail != "" {
    subj := "Rehearsal booking confirmed"
    emailBody := fmt.Sprintf(
        "Your rehearsal is booked.\n\nWhen: %s – %s (Europe/Skopje)\nDuration: %d hours\n\nIf you need to change this, contact the studio.\n",
        start.Format("2006-01-02 15:04"),
        end.Format("15:04"),
        req.DurationHours,
    )
    brevoKey := s.studio.BrevoAPIKey
    go func() {
        if err := mail.BookingConfirmation(brevoKey, "studio.porta.vlae@gmail.com", "Studio Porta Vlae", userEmail, subj, emailBody); err != nil {
            log.Printf("brevo: %v", err)
        }
    }()
}

w.Header().Set("Content-Type", "application/json")
_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func bookingNamePhone(ac *auth.Context, prof supabase.Profile) (name, phone string) {
	name = strings.TrimSpace(prof.FullName)
	phone = strings.TrimSpace(prof.PhoneNumber)
	if ac != nil && ac.IsAdmin {
		if name == "" {
			name = displayNameFromEmail(ac.Email)
		}
		if phone == "" {
			phone = "\u2014"
		}
	}
	return name, phone
}

func displayNameFromEmail(email string) string {
	e := strings.TrimSpace(email)
	if idx := strings.Index(e, "@"); idx > 0 {
		return e[:idx]
	}
	if e != "" {
		return e
	}
	return "Studio admin"
}

func parseBookingStart(s string, skopje *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty start")
	}
	if skopje == nil {
		skopje = time.UTC
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.000000000",
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
	} {
		if t, err := time.ParseInLocation(layout, s, skopje); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized start format")
}

func intervalOverlap(a0, a1, b0, b1 time.Time) bool {
	return a1.After(b0) && a0.Before(b1)
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
