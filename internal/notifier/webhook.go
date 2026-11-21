package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Webhook struct{ URL, Format string }

func (w Webhook) Send(ctx context.Context, event any) error {
	if w.URL == "" {
		return nil
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return e
	}
	var payload any = event
	if w.Format == "slack" {
		payload = map[string]string{"text": "FlagGuard rollback\n" + string(raw)}
	}
	if w.Format == "discord" {
		payload = map[string]string{"content": "FlagGuard rollback\n" + string(raw)}
	}
	raw, e = json.Marshal(payload)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(raw))
	if e != nil {
		return fmt.Errorf("invalid webhook request")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return fmt.Errorf("webhook delivery failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
	}
	return nil
}
