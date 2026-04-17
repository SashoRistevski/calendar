package supabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrProfileNotFound = errors.New("profile not found for user")

type Profile struct {
	FullName    string `json:"full_name"`
	PhoneNumber string `json:"phone_number"`
}

func ProfileComplete(p Profile) bool {
	return strings.TrimSpace(p.FullName) != "" && strings.TrimSpace(p.PhoneNumber) != ""
}

func GetProfile(ctx context.Context, baseURL, serviceRoleKey, userID string) (Profile, error) {
	return getProfileBySelect(ctx, baseURL, serviceRoleKey, userID, "full_name,phone_number")
}

func UpdateProfile(ctx context.Context, baseURL, serviceRoleKey, userID, userEmail, fullName, phoneNumber string) error {
	fullName = strings.TrimSpace(fullName)
	phoneNumber = strings.TrimSpace(phoneNumber)
	if fullName == "" || phoneNumber == "" {
		return fmt.Errorf("full_name and phone_number are required")
	}
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(serviceRoleKey) == "" || strings.TrimSpace(userID) == "" {
		return fmt.Errorf("supabase URL, service role key, or user id not configured")
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	client := &http.Client{Timeout: 15 * time.Second}

	patchBody, err := json.Marshal(map[string]string{
		"full_name":    fullName,
		"phone_number": phoneNumber,
	})
	if err != nil {
		return err
	}
	rows, perr := patchProfileReturnRows(ctx, client, base, serviceRoleKey, userID, patchBody)
	if perr != nil {
		return perr
	}
	if len(rows) > 0 {
		return nil
	}
	if err := insertProfileRow(ctx, client, base, serviceRoleKey, userID, userEmail, fullName, phoneNumber); err != nil {
		if isUniqueOrConflict(err) {
			rows2, retryErr := patchProfileReturnRows(ctx, client, base, serviceRoleKey, userID, patchBody)
			if retryErr != nil {
				return retryErr
			}
			if len(rows2) == 0 {
				return fmt.Errorf("could not upsert profile after insert conflict")
			}
			return nil
		}
		return err
	}
	return nil
}

func patchProfileReturnRows(ctx context.Context, client *http.Client, base, serviceRoleKey, userID string, body []byte) ([]Profile, error) {
	u, err := url.Parse(base + "/rest/v1/profiles")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("id", "eq."+userID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "return=representation")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("profiles %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var rows []Profile
	if len(respBody) == 0 {
		return rows, nil
	}
	if err := json.Unmarshal(respBody, &rows); err != nil {
		return nil, fmt.Errorf("decode profiles patch: %w", err)
	}
	return rows, nil
}

func insertProfileRow(ctx context.Context, client *http.Client, base, serviceRoleKey, userID, userEmail, fullName, phoneNumber string) error {
	row := map[string]string{
		"id":           userID,
		"full_name":    fullName,
		"phone_number": phoneNumber,
	}
	if em := strings.TrimSpace(userEmail); em != "" {
		row["email"] = em
	}
	body, err := json.Marshal(row)
	if err != nil {
		return err
	}
	u := base + "/rest/v1/profiles"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return insertOrPatchError(resp.StatusCode, respBody)
	}
	return nil
}

func insertOrPatchError(status int, body []byte) error {
	s := strings.TrimSpace(string(body))
	return fmt.Errorf("profiles %d: %s", status, s)
}

func isUniqueOrConflict(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "23505") || strings.Contains(s, "409") || strings.Contains(strings.ToLower(s), "duplicate")
}

func getProfileBySelect(ctx context.Context, baseURL, serviceRoleKey, userID, selectList string) (Profile, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" || serviceRoleKey == "" {
		return Profile{}, fmt.Errorf("supabase URL or service role key not configured")
	}
	u, err := url.Parse(base + "/rest/v1/profiles")
	if err != nil {
		return Profile{}, err
	}
	q := u.Query()
	q.Set("select", selectList)
	q.Set("id", "eq."+userID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("apikey", serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+serviceRoleKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Profile{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Profile{}, fmt.Errorf("profiles %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rows []Profile
	if err := json.Unmarshal(body, &rows); err != nil {
		return Profile{}, fmt.Errorf("decode profiles: %w", err)
	}
	if len(rows) == 0 {
		return Profile{}, ErrProfileNotFound
	}
	return rows[0], nil
}
