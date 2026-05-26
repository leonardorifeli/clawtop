package anthropic

import (
	"encoding/json"
	"fmt"
	"os"
)

type Credentials struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

type credsFile struct {
	ClaudeAiOauth Credentials `json:"claudeAiOauth"`
}

// LoadCreds reads ~/.claude/.credentials.json and returns the OAuth credentials.
// The Claude CLI is responsible for refreshing the token; this just rereads from disk.
func LoadCreds(path string) (*Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var f credsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if f.ClaudeAiOauth.AccessToken == "" {
		return nil, fmt.Errorf("no accessToken in %s", path)
	}
	return &f.ClaudeAiOauth, nil
}
