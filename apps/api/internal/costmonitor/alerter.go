package costmonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const webhookTimeout = 10 * time.Second

// WebhookAlerter posts a Slack-compatible `{"text": "..."}` payload —
// supported by Slack incoming webhooks and most "Slack-compatible" ops
// tooling (Mattermost, Discord-via-adapter, generic log-to-chat bridges)
// without any per-provider config.
type WebhookAlerter struct {
	url        string
	httpClient *http.Client
}

// NewWebhookAlerter builds an Alerter that posts to url. An empty url is
// valid and makes Alert a no-op — the same "unconfigured means disabled"
// convention as the rest of this package, so wiring a WebhookAlerter
// unconditionally in main.go is safe even when COST_ALERT_WEBHOOK_URL isn't
// set.
func NewWebhookAlerter(url string) *WebhookAlerter {
	return &WebhookAlerter{
		url:        url,
		httpClient: &http.Client{Timeout: webhookTimeout},
	}
}

func (w *WebhookAlerter) Alert(ctx context.Context, b Breach) error {
	if w.url == "" {
		return nil
	}

	text := fmt.Sprintf(
		":rotating_light: *Cost budget %s*: `%s/%s` used %d of %d calls today (%.0f%%)",
		b.Severity, b.Budget.Category, b.Budget.Provider, b.Usage, b.Budget.DailyLimit,
		float64(b.Usage)/float64(b.Budget.DailyLimit)*100,
	)
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("costmonitor: alert webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("costmonitor: alert webhook returned status %d", resp.StatusCode)
	}
	return nil
}
