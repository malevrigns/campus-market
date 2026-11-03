// Package mcp 实现 MCP Streamable HTTP 客户端：会话、工具目录、schema 校验与调用。
// 仅允许后端登记的可信服务与只读工具；重定向、外部 schema 引用一律拒绝。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/pkg/toolschema"
	"qingfly/edu-mall/internal/port"
)

type Client struct {
	http     *http.Client
	registry port.ConnectionRegistry
}

func New(client *http.Client, registry port.ConnectionRegistry) *Client {
	if client == nil {
		client = &http.Client{}
	}
	return &Client{http: client, registry: registry}
}

type session struct {
	server            domain.MCPServer
	client            *http.Client
	session, protocol string
}

func (c *Client) newSession(ctx context.Context, serverID string) (*session, error) {
	server, ok := c.registry.MCP(serverID)
	if !ok {
		return nil, errors.New("MCP server not configured")
	}
	client := *c.http
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("MCP redirects forbidden") }
	m := &session{server: server, client: &client, protocol: "2025-06-18"}
	raw, err := m.rpc(ctx, "initialize", map[string]any{"protocolVersion": m.protocol, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "campus-market", "version": "3.0"}})
	if err != nil {
		return nil, err
	}
	var init struct {
		Protocol     string                     `json:"protocolVersion"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err = json.Unmarshal(raw, &init); err != nil {
		return nil, err
	}
	if init.Protocol != "2025-06-18" && init.Protocol != "2025-03-26" && init.Protocol != "2025-11-25" {
		m.close()
		return nil, errors.New("unsupported MCP protocol version")
	}
	if _, ok := init.Capabilities["tools"]; !ok {
		m.close()
		return nil, errors.New("MCP server has no tools capability")
	}
	m.protocol = init.Protocol
	if err = m.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		m.close()
		return nil, err
	}
	return m, nil
}

func (m *session) request(ctx context.Context, method string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.server.URL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", m.protocol)
	if m.session != "" {
		req.Header.Set("Mcp-Session-Id", m.session)
	}
	if m.server.Token != "" {
		req.Header.Set("Authorization", "Bearer "+m.server.Token)
	}
	return m.client.Do(req)
}

func (m *session) notify(ctx context.Context, method string, params any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	resp, err := m.request(ctx, "POST", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 && resp.StatusCode != 204 {
		return errors.New("MCP notification rejected")
	}
	return nil
}

func (m *session) close() {
	if m.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if resp, err := m.request(ctx, "DELETE", nil); err == nil {
		resp.Body.Close()
	}
}

func (m *session) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	requestID := idgen.New("rpc_")
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": method, "params": params})
	resp, err := m.request(ctx, "POST", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("MCP HTTP status %d", resp.StatusCode)
	}
	if method == "initialize" {
		sessionID := resp.Header.Get("Mcp-Session-Id")
		if len(sessionID) > 256 {
			return nil, errors.New("invalid MCP session")
		}
		for _, c := range sessionID {
			if c < 33 || c > 126 {
				return nil, errors.New("invalid MCP session")
			}
		}
		m.session = sessionID
	}
	decode := func(raw []byte) (json.RawMessage, bool, error) {
		var env struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code int `json:"code"`
			} `json:"error"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, false, err
		}
		if env.JSONRPC != "2.0" {
			return nil, false, errors.New("invalid MCP envelope")
		}
		if len(env.ID) == 0 {
			return nil, false, nil
		}
		var responseID string
		if err := json.Unmarshal(env.ID, &responseID); err != nil || responseID != requestID {
			return nil, false, errors.New("MCP response ID mismatch")
		}
		if env.Error != nil {
			return nil, false, fmt.Errorf("MCP error %d", env.Error.Code)
		}
		if env.Method != "" || len(env.Result) == 0 {
			return nil, false, errors.New("unsupported MCP response")
		}
		return env.Result, true, nil
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return nil, errors.New("MCP response too large")
		}
		result, ok, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("MCP result missing")
		}
		return result, nil
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, errors.New("unsupported MCP content type")
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, (1<<20)+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	data := []string{}
	total := 0
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line)
		if total > 1<<20 {
			return nil, errors.New("MCP response too large")
		}
		if line == "" {
			if len(data) > 0 {
				result, ok, err := decode([]byte(strings.Join(data, "\n")))
				if err != nil {
					return nil, err
				}
				if ok {
					return result, nil
				}
				data = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("MCP stream ended without result")
}

func (m *session) tools(ctx context.Context) ([]domain.MCPTool, error) {
	out := []domain.MCPTool{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		args := map[string]any{}
		if cursor != "" {
			args["cursor"] = cursor
		}
		raw, err := m.rpc(ctx, "tools/list", args)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []domain.MCPTool `json:"tools"`
			Next  string           `json:"nextCursor"`
		}
		if err = json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Tools...)
		if len(out) > 128 {
			return nil, errors.New("MCP tool count exceeds limit")
		}
		if res.Next == "" {
			return out, nil
		}
		if seen[res.Next] {
			return nil, errors.New("MCP pagination cycle")
		}
		seen[res.Next] = true
		cursor = res.Next
	}
	return nil, errors.New("MCP pagination limit")
}

func (c *Client) ListTools(ctx context.Context, serverID string) ([]domain.MCPTool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := c.newSession(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer m.close()
	return m.tools(ctx)
}

func (c *Client) CallTool(ctx context.Context, serverID string, capability domain.AgentCapability, args string, guard func() error) (string, error) {
	values, err := toolschema.Validate(capability.Config.InputSchema, args)
	if err != nil {
		return "", &domain.ToolResultError{Message: "工具参数不符合已发布的 schema"}
	}
	if !capability.Config.ReadOnly {
		return "", errors.New("MCP capability not approved read-only")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := c.newSession(ctx, serverID)
	if err != nil {
		return "", err
	}
	defer m.close()
	tools, err := m.tools(ctx)
	if err != nil {
		return "", err
	}
	found := false
	for _, tool := range tools {
		if tool.Name == capability.Config.Tool {
			if !tool.Annotations.ReadOnlyHint {
				return "", errors.New("MCP tool no longer declares read-only")
			}
			var old, current any
			if json.Unmarshal(capability.Config.InputSchema, &old) != nil || json.Unmarshal(tool.InputSchema, &current) != nil || !reflect.DeepEqual(old, current) {
				return "", errors.New("MCP tool schema changed; republish capability")
			}
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("MCP tool removed")
	}
	if guard != nil {
		if err := guard(); err != nil {
			return "", err
		}
	}
	raw, err := m.rpc(ctx, "tools/call", map[string]any{"name": capability.Config.Tool, "arguments": values})
	if err != nil {
		return "", err
	}
	if len(raw) > 65536 {
		return "", errors.New("MCP tool result exceeds limit")
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if result.IsError {
		return "", &domain.ToolResultError{Message: "外部工具执行失败"}
	}
	parts := []string{}
	for _, item := range result.Content {
		if item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	if len(result.Structured) > 0 {
		parts = append(parts, string(result.Structured))
	}
	return strings.Join(parts, "\n"), nil
}
