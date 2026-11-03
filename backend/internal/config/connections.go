package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"

	"qingfly/edu-mall/internal/domain"
)

var connectionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

func validConnectionURL(raw string, production bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if production || u.Scheme != "http" {
		return false
	}
	return u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
}

// loadAgentConnections 加载运维维护的端点允许列表。文件里保存的是环境变量名，
// 不是 API Key；密钥永远不会进入能力快照。
func loadAgentConnections(path string, cfg *App) error {
	cfg.ModelProviders = map[string]domain.ModelProvider{}
	cfg.MCPServers = map[string]domain.MCPServer{}
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
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
	if err = d.Decode(&input); err != nil {
		return err
	}
	for key, p := range input.Providers {
		if !connectionID.MatchString(key) || !validConnectionURL(p.BaseURL, cfg.Production) || len(p.Model) > 128 {
			return errors.New("invalid model connection")
		}
		cfg.ModelProviders[key] = domain.ModelProvider{BaseURL: p.BaseURL, DefaultModel: p.Model, APIKey: os.Getenv(p.KeyEnv)}
	}
	for key, server := range input.MCP {
		if !connectionID.MatchString(key) || !validConnectionURL(server.URL, cfg.Production) || !server.PublicDataOnly {
			return errors.New("MCP connections must be explicitly trusted public-data endpoints")
		}
		cfg.MCPServers[key] = domain.MCPServer{URL: server.URL, Token: os.Getenv(server.TokenEnv)}
	}
	return nil
}
