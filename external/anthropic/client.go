package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leonardorifeli/clawtop/internal/domain"
)

const (
	endpoint    = "https://api.anthropic.com/v1/messages"
	apiVersion  = "2023-06-01"
	oauthBeta   = "oauth-2025-04-20"
	probeModel  = "claude-haiku-4-5"
	defaultUA   = "clawtop/0.2 (+https://github.com/leonardorifeli/clawtop)"
	httpTimeout = 30 * time.Second
)

type Client struct {
	credsPath string
	hc        *http.Client
}

func New(credsPath string) *Client {
	return &Client{
		credsPath: credsPath,
		hc:        &http.Client{Timeout: httpTimeout},
	}
}

// Probe makes a minimal Haiku call and returns the parsed rate-limit status from
// response headers. The body cost is one token; the headers are what we want.
func (c *Client) Probe(ctx context.Context) (*domain.Status, error) {
	creds, err := LoadCreds(c.credsPath)
	if err != nil {
		return nil, err
	}

	body, _ := json.Marshal(map[string]any{
		"model":      probeModel,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "."}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("anthropic-beta", oauthBeta)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", defaultUA)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	// Drain to free the connection; we only care about headers (except on errors).
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	io.Copy(io.Discard, resp.Body)

	s := parseHeaders(resp.Header)
	s.TS = time.Now().Unix()
	s.Subscription = creds.SubscriptionType
	return s, nil
}

// parseHeaders extracts the unified rate-limit fields. The Anthropic OAuth
// surface exposes a few related headers; we read what is present and ignore
// what is not, so we are forward-compatible with naming tweaks.
func parseHeaders(h http.Header) *domain.Status {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := h.Get(k); v != "" {
				return v
			}
		}
		return ""
	}

	pct := func(s string) float64 {
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return 0
		}
		// Some Anthropic surfaces return 0..1, others 0..100. Normalize to 0..100.
		if f > 0 && f <= 1.0 {
			f *= 100
		}
		return f
	}

	reset := func(s string) int64 {
		if s == "" {
			return 0
		}
		// Either a unix timestamp (seconds) or RFC3339.
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Unix()
		}
		return 0
	}

	return &domain.Status{
		Session: domain.Window{
			Pct: pct(get(
				"anthropic-ratelimit-unified-5h-utilization",
				"anthropic-ratelimit-unified-utilization",
			)),
			ResetAt: reset(get(
				"anthropic-ratelimit-unified-5h-reset",
				"anthropic-ratelimit-unified-reset",
			)),
		},
		Week: domain.Window{
			Pct: pct(get(
				"anthropic-ratelimit-unified-7d-utilization",
				"anthropic-ratelimit-unified-week-utilization",
			)),
			ResetAt: reset(get(
				"anthropic-ratelimit-unified-7d-reset",
				"anthropic-ratelimit-unified-week-reset",
			)),
		},
		Limit: get(
			"anthropic-ratelimit-unified-status",
			"anthropic-ratelimit-unified-5h-status",
		),
	}
}
