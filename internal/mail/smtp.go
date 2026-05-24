package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func BookingConfirmation(apiKey, fromAddr, fromName, toUser, subject, body string) error {
	if strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("BREVO_API_KEY not configured")
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"sender": map[string]string{
			"email": fromAddr,
			"name":  fromName,
		},
		"to": []map[string]string{
			{"email": toUser},
		},
		"subject":     subject,
		"textContent": body,
	})

	req, err := http.NewRequest("POST", "https://api.brevo.com/v3/smtp/email", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("brevo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("brevo status: %d", resp.StatusCode)
	}
	return nil
}