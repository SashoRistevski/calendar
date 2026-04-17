package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func VerifyAccessToken(ctx context.Context, jwtSecret, supabaseURL, anonKey, authorization string) (*Context, error) {
	raw, err := rawAccessToken(authorization)
	if err != nil {
		return nil, err
	}

	var lastErr error
	if strings.TrimSpace(jwtSecret) != "" {
		if c, e := parseHS256(jwtSecret, raw); e == nil {
			return c, nil
		} else {
			lastErr = e
		}
	}
	if strings.TrimSpace(supabaseURL) != "" && strings.TrimSpace(anonKey) != "" {
		if c, e := verifyUserViaSupabaseAuth(ctx, supabaseURL, anonKey, raw); e == nil {
			return c, nil
		} else {
			lastErr = e
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoBearer, lastErr)
	}
	return nil, ErrNoBearer
}

func TryParseBearer(ctx context.Context, jwtSecret, supabaseURL, anonKey, authorization string) (*Context, bool, error) {
	if strings.TrimSpace(authorization) == "" {
		return nil, false, nil
	}
	c, err := VerifyAccessToken(ctx, jwtSecret, supabaseURL, anonKey, authorization)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

func rawAccessToken(authorization string) (string, error) {
	const prefix = "Bearer "
	if len(authorization) < len(prefix) || !strings.EqualFold(authorization[:len(prefix)], prefix) {
		return "", ErrNoBearer
	}
	raw := strings.TrimSpace(authorization[len(prefix):])
	if raw == "" {
		return "", ErrNoBearer
	}
	return raw, nil
}

var authHTTPClient = &http.Client{Timeout: 12 * time.Second}

func verifyUserViaSupabaseAuth(ctx context.Context, baseURL, anonKey, accessToken string) (*Context, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/auth/v1/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", strings.TrimSpace(anonKey))
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := authHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var u struct {
		ID          string                 `json:"id"`
		Email       string                 `json:"email"`
		AppMetadata map[string]interface{} `json:"app_metadata"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}
	if u.ID == "" {
		return nil, fmt.Errorf("missing user id")
	}
	return &Context{
		UserID:  u.ID,
		Email:   strings.TrimSpace(u.Email),
		IsAdmin: truthy(u.AppMetadata["is_admin"]),
	}, nil
}
