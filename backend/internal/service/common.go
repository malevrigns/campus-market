// Package service 是业务层：编排用例、持有业务规则、依赖 port 中的能力契约。
// 该层不引用 HTTP、SQL、Redis 或具体第三方 SDK。
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"qingfly/edu-mall/internal/domain"
)

func now() int64 { return time.Now().UnixMilli() }

func digestToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func validText(s string, min, max int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= min && n <= max
}

func validPassword(p string) bool {
	return utf8.RuneCountInString(p) >= 8 && len(p) <= 72 && strings.TrimSpace(p) != ""
}

func keyValid(v string) bool {
	return len(v) >= 8 && len(v) <= 128 && strings.TrimSpace(v) == v
}

var localImage = regexp.MustCompile(`^/media/img_[a-f0-9]{32}\.jpg$`)

func validImageURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && ((u.Scheme == "https" && u.Host != "" && u.User == nil) || localImage.MatchString(v)) && len(v) <= 2048
}

// AdminSet 是管理员允许名单。配置可在运行时更新，无需重启进程。
type AdminSet struct {
	mu  sync.RWMutex
	ids map[string]bool
}

func NewAdminSet(csv string) *AdminSet {
	a := &AdminSet{ids: map[string]bool{}}
	a.SetCSV(csv)
	return a
}

func (a *AdminSet) SetCSV(csv string) {
	ids := map[string]bool{}
	for _, id := range strings.Split(csv, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = true
		}
	}
	a.mu.Lock()
	a.ids = ids
	a.mu.Unlock()
}

func (a *AdminSet) IsAdmin(uid string) bool {
	if uid == "" {
		return false
	}
	a.mu.RLock()
	ok := a.ids[uid]
	a.mu.RUnlock()
	return ok
}

// RuntimeConnections 是后端可信模型/MCP 连接的运行时目录。
// 它实现了 port.ConnectionRegistry，并支持测试或运维在运行时替换连接。
type RuntimeConnections struct {
	mu           sync.RWMutex
	models       map[string]domain.ModelProvider
	defaultModel domain.ModelProvider
	mcps         map[string]domain.MCPServer
}

func NewRuntimeConnections(defaultModel domain.ModelProvider, models map[string]domain.ModelProvider, mcps map[string]domain.MCPServer) *RuntimeConnections {
	c := &RuntimeConnections{models: map[string]domain.ModelProvider{}, mcps: map[string]domain.MCPServer{}}
	for k, v := range models {
		if k != "default" {
			c.models[k] = v
		}
	}
	c.defaultModel = defaultModel
	for k, v := range mcps {
		c.mcps[k] = v
	}
	return c
}

func (c *RuntimeConnections) Model(key string) (domain.ModelProvider, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if key == "default" {
		return c.defaultModel, true
	}
	p, ok := c.models[key]
	return p, ok
}

func (c *RuntimeConnections) MCP(serverID string) (domain.MCPServer, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.mcps[serverID]
	return s, ok
}

func (c *RuntimeConnections) ModelNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []string{"default"}
	for k := range c.models {
		out = append(out, k)
	}
	return out
}

func (c *RuntimeConnections) MCPServerNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.mcps))
	for k := range c.mcps {
		out = append(out, k)
	}
	return out
}

func (c *RuntimeConnections) SetDefaultModel(provider domain.ModelProvider) {
	c.mu.Lock()
	c.defaultModel = provider
	c.mu.Unlock()
}

func (c *RuntimeConnections) SetMCP(serverID string, server domain.MCPServer) {
	c.mu.Lock()
	c.mcps[serverID] = server
	c.mu.Unlock()
}

func (c *RuntimeConnections) ClearMCP() {
	c.mu.Lock()
	c.mcps = map[string]domain.MCPServer{}
	c.mu.Unlock()
}
