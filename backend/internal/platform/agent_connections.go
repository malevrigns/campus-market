package platform

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var connectionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func validConnectionURL(raw string, production bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (!production && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

// LoadAgentConnections loads operator-owned endpoint allowlists. The file holds
// environment variable names, not API keys; secrets never enter catalog snapshots.
func LoadAgentConnections(path string, cfg *Config) error {
	if path == "" {
		return nil
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if len(b) > 65536 {
		return errors.New("agent connections file too large")
	}
	var input struct {
		Providers map[string]struct {
			BaseURL string `json:"base_url"`
			Model   string `json:"model"`
			KeyEnv  string `json:"api_key_env"`
		} `json:"providers"`
		MCP map[string]struct {
			URL            string `json:"url"`
			TokenEnv       string `json:"token_env"`
			PublicDataOnly bool   `json:"public_data_only"`
		} `json:"mcp_servers"`
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&input); e != nil {
		return e
	}
	cfg.ModelProviders = map[string]ModelProvider{}
	cfg.MCPServers = map[string]MCPServer{}
	for key, p := range input.Providers {
		if !connectionID.MatchString(key) || !validConnectionURL(p.BaseURL, cfg.Production) || len(p.Model) > 128 {
			return errors.New("invalid model connection")
		}
		cfg.ModelProviders[key] = ModelProvider{BaseURL: p.BaseURL, DefaultModel: p.Model, APIKey: os.Getenv(p.KeyEnv)}
	}
	for key, server := range input.MCP {
		if !connectionID.MatchString(key) || !validConnectionURL(server.URL, cfg.Production) || !server.PublicDataOnly {
			return errors.New("MCP connections must be explicitly trusted public-data endpoints")
		}
		cfg.MCPServers[key] = MCPServer{URL: server.URL, Token: os.Getenv(server.TokenEnv)}
	}
	return nil
}
