package platform

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

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}
type mcpSession struct {
	server            MCPServer
	client            *http.Client
	session, protocol string
}
type noSchemaLoader struct{}

func (noSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external schema references are disabled")
}
func compileToolSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 || len(raw) > 32768 {
		return nil, errors.New("tool schema too large or absent")
	}
	doc, e := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	obj, ok := doc.(map[string]any)
	if !ok || obj["type"] != "object" {
		return nil, errors.New("tool schema must describe an object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noSchemaLoader{})
	if e = compiler.AddResource("https://campus.invalid/tool-schema", doc); e != nil {
		return nil, e
	}
	return compiler.Compile("https://campus.invalid/tool-schema")
}
func validateToolArgs(raw json.RawMessage, args string) (map[string]any, error) {
	if len(args) > 8192 {
		return nil, errors.New("arguments too large")
	}
	schema, e := compileToolSchema(raw)
	if e != nil {
		return nil, e
	}
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	var doc any
	if e = decoder.Decode(&doc); e != nil {
		return nil, e
	}
	if e = decoder.Decode(&struct{}{}); e != io.EOF {
		return nil, errors.New("arguments contain trailing data")
	}
	if e = schema.Validate(doc); e != nil {
		return nil, e
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be object")
	}
	return m, nil
}
func (s *Server) newMCPSession(ctx context.Context, serverID string) (*mcpSession, error) {
	server, ok := s.cfg.MCPServers[serverID]
	if !ok {
		return nil, errors.New("MCP server not configured")
	}
	client := *s.client
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("MCP redirects forbidden") }
	m := &mcpSession{server: server, client: &client, protocol: "2025-06-18"}
	raw, e := m.rpc(ctx, "initialize", map[string]any{"protocolVersion": m.protocol, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "campus-market", "version": "3.0"}})
	if e != nil {
		return nil, e
	}
	var init struct {
		Protocol     string                     `json:"protocolVersion"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if e = json.Unmarshal(raw, &init); e != nil {
		return nil, e
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
	if e = m.notify(ctx, "notifications/initialized", map[string]any{}); e != nil {
		m.close()
		return nil, e
	}
	return m, nil
}
func (m *mcpSession) request(ctx context.Context, method string, body io.Reader) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, method, m.server.URL, body)
	if e != nil {
		return nil, e
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
func (m *mcpSession) notify(ctx context.Context, method string, params any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	resp, e := m.request(ctx, "POST", bytes.NewReader(body))
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 && resp.StatusCode != 204 {
		return errors.New("MCP notification rejected")
	}
	return nil
}
func (m *mcpSession) close() {
	if m.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if resp, e := m.request(ctx, "DELETE", nil); e == nil {
		resp.Body.Close()
	}
}
func (m *mcpSession) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	requestID := id("rpc_")
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": method, "params": params})
	resp, e := m.request(ctx, "POST", bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("MCP HTTP status %d", resp.StatusCode)
	}
	if method == "initialize" {
		session := resp.Header.Get("Mcp-Session-Id")
		if len(session) > 256 {
			return nil, errors.New("invalid MCP session")
		}
		for _, c := range session {
			if c < 33 || c > 126 {
				return nil, errors.New("invalid MCP session")
			}
		}
		m.session = session
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
		if e := json.Unmarshal(raw, &env); e != nil {
			return nil, false, e
		}
		if env.JSONRPC != "2.0" {
			return nil, false, errors.New("invalid MCP envelope")
		}
		if len(env.ID) == 0 {
			return nil, false, nil
		}
		var responseID string
		if e := json.Unmarshal(env.ID, &responseID); e != nil || responseID != requestID {
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
		raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if e != nil || len(raw) > 1<<20 {
			return nil, errors.New("MCP response too large")
		}
		result, ok, e := decode(raw)
		if e != nil {
			return nil, e
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
				result, ok, e := decode([]byte(strings.Join(data, "\n")))
				if e != nil {
					return nil, e
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
	if e = scanner.Err(); e != nil {
		return nil, e
	}
	return nil, errors.New("MCP stream ended without result")
}
func (m *mcpSession) tools(ctx context.Context) ([]MCPTool, error) {
	out := []MCPTool{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		args := map[string]any{}
		if cursor != "" {
			args["cursor"] = cursor
		}
		raw, e := m.rpc(ctx, "tools/list", args)
		if e != nil {
			return nil, e
		}
		var res struct {
			Tools []MCPTool `json:"tools"`
			Next  string    `json:"nextCursor"`
		}
		if e = json.Unmarshal(raw, &res); e != nil {
			return nil, e
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
func (s *Server) listMCPTools(ctx context.Context, serverID string) ([]MCPTool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, e := s.newMCPSession(ctx, serverID)
	if e != nil {
		return nil, e
	}
	defer m.close()
	return m.tools(ctx)
}
func (s *Server) discoverMCPTools(w http.ResponseWriter, r *http.Request, _ *User) error {
	tools, e := s.listMCPTools(r.Context(), r.PathValue("id"))
	if e != nil {
		return fail(502, "MCP 服务不可用、协议不兼容或工具目录无效")
	}
	return result(w, 200, map[string]any{"items": tools})
}
func (s *Server) callMCPTool(ctx context.Context, c AgentCapability, args string, beforeCall ...func() error) (string, error) {
	values, e := validateToolArgs(c.Config.InputSchema, args)
	if e != nil {
		return "", &toolResultError{"工具参数不符合已发布的 schema"}
	}
	if !c.Config.ReadOnly {
		return "", errors.New("MCP capability not approved read-only")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, e := s.newMCPSession(ctx, c.Config.ServerID)
	if e != nil {
		return "", e
	}
	defer m.close()
	tools, e := m.tools(ctx)
	if e != nil {
		return "", e
	}
	found := false
	for _, tool := range tools {
		if tool.Name == c.Config.Tool {
			if !tool.Annotations.ReadOnlyHint {
				return "", errors.New("MCP tool no longer declares read-only")
			}
			var old, current any
			if json.Unmarshal(c.Config.InputSchema, &old) != nil || json.Unmarshal(tool.InputSchema, &current) != nil || !reflect.DeepEqual(old, current) {
				return "", errors.New("MCP tool schema changed; republish capability")
			}
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("MCP tool removed")
	}
	for _, guard := range beforeCall {
		if e := guard(); e != nil {
			return "", e
		}
	}
	raw, e := m.rpc(ctx, "tools/call", map[string]any{"name": c.Config.Tool, "arguments": values})
	if e != nil {
		return "", e
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
	if e = json.Unmarshal(raw, &result); e != nil {
		return "", e
	}
	if result.IsError {
		return "", &toolResultError{"外部工具执行失败"}
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
