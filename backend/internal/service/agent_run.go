package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/pkg/toolschema"
	"qingfly/edu-mall/internal/port"
)

type AgentRunService struct {
	store       port.AgentStore
	catalog     *AgentCatalogService
	connections port.ConnectionRegistry
	llm         port.LLMClient
	mcp         port.MCPGateway
	products    port.CatalogStore
	orders      port.OrderStore
	limiter     port.RateLimiter
}

func NewAgentRun(store port.AgentStore, catalog *AgentCatalogService, connections port.ConnectionRegistry, llm port.LLMClient, mcp port.MCPGateway, products port.CatalogStore, orders port.OrderStore, limiter port.RateLimiter) *AgentRunService {
	return &AgentRunService{store: store, catalog: catalog, connections: connections, llm: llm, mcp: mcp, products: products, orders: orders, limiter: limiter}
}

type AgentHistory struct {
	Items   []domain.AgentRun `json:"items"`
	Title   string            `json:"title"`
	AgentID string            `json:"agent_id"`
}

// Recover 在进程启动时回收遗留的 running 记录。
func (s *AgentRunService) Recover(ctx context.Context) error {
	return s.store.FailStaleRuns(ctx, now()-120000)
}

func (s *AgentRunService) Threads(ctx context.Context, user *domain.User) ([]domain.AgentThread, error) {
	return s.store.Threads(ctx, user.ID)
}

func (s *AgentRunService) History(ctx context.Context, user *domain.User, threadID string) (AgentHistory, error) {
	title, err := s.store.ThreadTitle(ctx, threadID, user.ID)
	if err != nil {
		return AgentHistory{}, err
	}
	runs, err := s.store.Runs(ctx, user.ID, threadID, 30)
	if err != nil {
		return AgentHistory{}, err
	}
	aid, err := s.store.ThreadAgent(ctx, threadID, user.ID)
	if err != nil {
		return AgentHistory{}, err
	}
	return AgentHistory{Items: runs, Title: title, AgentID: aid}, nil
}

// Stream 执行一次 Agent 回答。事件通过 sink 输出，HTTP/SSE 编码留在应用层。
func (s *AgentRunService) Stream(ctx context.Context, user *domain.User, input domain.AgentRunInput, sink func(domain.AgentEvent) error) error {
	input.Prompt = strings.TrimSpace(input.Prompt)
	if len([]rune(input.Prompt)) < 1 || len([]rune(input.Prompt)) > 2000 || !keyValid(input.Idem) {
		return domain.Invalid("请输入 1–2000 字问题和有效请求 ID")
	}
	if err := s.store.FailStaleRuns(ctx, now()-120000); err != nil {
		return err
	}
	if run, snap, found, err := s.store.FindRunByIdem(ctx, user.ID, input.Idem); err != nil {
		return err
	} else if found {
		return s.emitReplay(run, snap, input, sink)
	}
	aid := input.AgentID
	threadID := input.ThreadID
	if threadID != "" {
		bound, err := s.store.ThreadAgent(ctx, threadID, user.ID)
		if err != nil {
			return err
		}
		if aid != "" && aid != bound {
			return domain.Conflict("这段对话属于另一位助手，请新建对话")
		}
		aid = bound
	}
	if aid == "" {
		aid = domain.DefaultAgentID
	}
	snapshot, err := s.store.SnapshotForAgent(ctx, aid)
	if err != nil {
		return err
	}
	if !s.catalog.snapshotAvailable(ctx, snapshot) {
		return domain.Unavailable("该助手的模型或工具暂不可用，请选择其他助手")
	}
	running, err := s.store.RunningCount(ctx, user.ID)
	if err != nil {
		return err
	}
	if running > 0 {
		return domain.Conflict("请等待当前回答结束，或先停止生成")
	}
	if retry, allowed := s.limiter.Window(ctx, "agent:"+user.ID, 10, time.Minute); !allowed {
		return domain.Retryable(domain.KindTooMany, "请求较多，请稍后再试", int(retry.Seconds()+0.999))
	}
	provider, _ := s.connections.Model(snapshot.Model.Config.Provider)
	if snapshot.Model.Config.Model == "" {
		snapshot.Model.Config.Model = provider.DefaultModel
	}
	var thread *domain.AgentThread
	if threadID == "" {
		threadID = idgen.New("thread_")
		title := []rune(input.Prompt)
		if len(title) > 30 {
			title = title[:30]
		}
		thread = &domain.AgentThread{ID: threadID, Title: string(title), CreatedAt: now()}
	} else if _, err = s.store.ThreadTitle(ctx, threadID, user.ID); err != nil {
		return err
	}
	run := domain.AgentRun{ID: idgen.New("run_"), ThreadID: threadID, Prompt: input.Prompt, Status: "running", CreatedAt: now(), AgentID: aid, AgentVersion: snapshot.Version, AgentName: snapshot.Name}
	if err = s.store.InTx(ctx, func(tx port.AgentTx) error {
		return tx.CreateRun(ctx, user.ID, input.Idem, thread, aid, run, snapshot)
	}); err != nil {
		if domain.DuplicateConstraint(err) != "" {
			if old, snap, found, queryErr := s.store.FindRunByIdem(ctx, user.ID, input.Idem); queryErr == nil && found {
				return s.emitReplay(old, snap, input, sink)
			}
		}
		return err
	}
	if err = sink(domain.AgentEvent{Type: "start", Data: run}); err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(snapshot.TimeoutSeconds)*time.Second)
	answer, execErr := s.execute(runCtx, sink, user, run, snapshot)
	cancel()
	run.Answer = answer
	run.Status = "completed"
	if execErr != nil {
		run.Status = "failed"
		run.Error = "AI 暂时无法完成回答，请重试"
		if errors.Is(ctx.Err(), context.Canceled) {
			run.Status = "cancelled"
			run.Error = "已停止生成"
		}
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	saveErr := s.store.SaveRunResult(saveCtx, run.ID, run.Answer, run.Status, run.Error)
	saveCancel()
	if saveErr != nil {
		_ = sink(domain.AgentEvent{Type: "error", Data: map[string]string{"message": "回答保存失败，请稍后查看历史"}})
		return nil
	}
	if run.Error != "" {
		_ = sink(domain.AgentEvent{Type: "error", Data: map[string]string{"message": run.Error}})
	}
	_ = sink(domain.AgentEvent{Type: "done", Data: run})
	return nil
}

func (s *AgentRunService) emitReplay(run domain.AgentRun, _ domain.AgentSnapshot, input domain.AgentRunInput, sink func(domain.AgentEvent) error) error {
	if input.AgentID != "" && input.AgentID != run.AgentID {
		return domain.Conflict("请求 ID 已用于其他助手")
	}
	if run.Prompt != input.Prompt || (input.ThreadID != "" && input.ThreadID != run.ThreadID) {
		return domain.Conflict("请求 ID 已用于其他对话")
	}
	if run.Status == "running" {
		return domain.Conflict("这条消息正在处理中")
	}
	if err := sink(domain.AgentEvent{Type: "start", Data: run}); err != nil {
		return err
	}
	if run.Answer != "" {
		if err := sink(domain.AgentEvent{Type: "delta", Data: map[string]string{"text": run.Answer}}); err != nil {
			return err
		}
	}
	return sink(domain.AgentEvent{Type: "done", Data: run})
}

func (s *AgentRunService) execute(ctx context.Context, sink func(domain.AgentEvent) error, user *domain.User, run domain.AgentRun, snapshot domain.AgentSnapshot) (string, error) {
	history, err := s.store.Runs(ctx, user.ID, run.ThreadID, 30)
	if err != nil {
		return "", err
	}
	messages := []map[string]any{{"role": "system", "content": domain.AgentSafetyPrompt + "\n" + snapshot.SystemPrompt}}
	for _, h := range history {
		messages = append(messages, map[string]any{"role": "user", "content": h.Prompt})
		if h.Answer != "" {
			messages = append(messages, map[string]any{"role": "assistant", "content": h.Answer})
		}
	}
	tools := snapshotToolDefinitions(snapshot)
	provider, ok := s.connections.Model(snapshot.Model.Config.Provider)
	if !ok || provider.BaseURL == "" || provider.APIKey == "" {
		return "", errors.New("model provider unavailable")
	}
	model := snapshot.Model.Config.Model
	if model == "" {
		model = provider.DefaultModel
	}
	var answer strings.Builder
	for step := 0; step < snapshot.MaxIterations; step++ {
		if ctx.Err() != nil {
			return answer.String(), ctx.Err()
		}
		if err := s.CheckRunEnabled(ctx, snapshot); err != nil {
			return answer.String(), err
		}
		turn, err := s.llm.Stream(ctx, domain.LLMRequest{BaseURL: provider.BaseURL, APIKey: provider.APIKey, Model: model, Messages: messages, Tools: tools, MaxTokens: snapshot.MaxTokens}, func(text string) error {
			answer.WriteString(text)
			return sink(domain.AgentEvent{Type: "delta", Data: map[string]string{"text": text}})
		})
		if err != nil {
			return answer.String(), err
		}
		if len(turn.ToolCalls) == 0 {
			return answer.String(), nil
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": turn.Content, "tool_calls": turn.ToolCalls})
		for _, call := range turn.ToolCalls {
			label := boundToolLabel(snapshot, call.Function.Name)
			if err := sink(domain.AgentEvent{Type: "tool", Data: map[string]string{"name": call.Function.Name, "label": label, "status": "running"}}); err != nil {
				return answer.String(), err
			}
			out, err := s.ExecuteBoundTool(ctx, user, run.ID, snapshot, call)
			if err != nil {
				_ = sink(domain.AgentEvent{Type: "tool", Data: map[string]string{"name": call.Function.Name, "label": label, "status": "failed"}})
				var recoverable *domain.ToolResultError
				if !errors.As(err, &recoverable) {
					return answer.String(), err
				}
				failure, _ := json.Marshal(map[string]string{"error": recoverable.Message})
				out = string(failure)
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": out})
			if err == nil {
				_ = sink(domain.AgentEvent{Type: "tool", Data: map[string]string{"name": call.Function.Name, "label": label, "status": "completed"}})
			}
		}
	}
	return answer.String(), errors.New("tool iteration limit reached")
}

func capabilityFunction(c domain.AgentCapability) string { return "tool_" + c.ID }

func toolSchema(c domain.AgentCapability) json.RawMessage {
	if c.Kind == "mcp" {
		return c.Config.InputSchema
	}
	if c.Config.Tool == "search_products" {
		return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":100,"description":"短关键词；空字符串浏览所有好物"}},"required":["query"],"additionalProperties":false}`)
	}
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func snapshotToolDefinitions(snap domain.AgentSnapshot) []map[string]any {
	out := []map[string]any{}
	for _, c := range snap.Tools {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": capabilityFunction(c), "description": c.Name + "：" + c.Description, "parameters": toolSchema(c)}})
	}
	return out
}

func boundToolLabel(snap domain.AgentSnapshot, name string) string {
	for _, c := range snap.Tools {
		if capabilityFunction(c) == name {
			return c.Name
		}
	}
	return "未授权工具"
}

// CheckRunEnabled 在每次模型调用与工具调用前确认助手与绑定能力仍启用。
func (s *AgentRunService) CheckRunEnabled(ctx context.Context, snap domain.AgentSnapshot) error {
	enabled, archived, err := s.store.Enabled(ctx, snap.AgentID)
	if err != nil || !enabled || archived {
		return errors.New("agent disabled")
	}
	for _, c := range append([]domain.AgentCapability{snap.Model}, snap.Tools...) {
		ok, err := s.store.CapabilityEnabled(ctx, c.ID)
		if err != nil || !ok {
			return errors.New("bound capability disabled")
		}
	}
	return nil
}

func (s *AgentRunService) ExecuteBoundTool(ctx context.Context, user *domain.User, runID string, snap domain.AgentSnapshot, call domain.ToolCall) (string, error) {
	if err := s.CheckRunEnabled(ctx, snap); err != nil {
		return "", err
	}
	var bound *domain.AgentCapability
	for i := range snap.Tools {
		if capabilityFunction(snap.Tools[i]) == call.Function.Name {
			bound = &snap.Tools[i]
			break
		}
	}
	if bound == nil {
		return "", errors.New("tool not bound to this published agent")
	}
	if err := s.store.RecordToolEvent(ctx, runID, bound.ID, bound.Name, "started", now()); err != nil {
		return "", err
	}
	var out string
	var err error
	if _, validationErr := toolschema.Validate(toolSchema(*bound), call.Function.Arguments); validationErr != nil {
		err = &domain.ToolResultError{Message: "参数不符合工具 schema"}
	} else if bound.Kind == "mcp" {
		out, err = s.mcp.CallTool(ctx, bound.Config.ServerID, *bound, call.Function.Arguments, func() error { return s.CheckRunEnabled(ctx, snap) })
	} else {
		call.Function.Name = bound.Config.Tool
		out, err = s.ReadTool(ctx, user, call)
	}
	status := "completed"
	if err != nil {
		status = "failed"
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if saveErr := s.store.RecordToolEvent(saveCtx, runID, bound.ID, bound.Name, status, now()); saveErr != nil && err == nil {
		err = saveErr
	}
	return out, err
}

// ReadTool 只执行当前用户有权读取的内置只读工具；身份从服务器注入。
func (s *AgentRunService) ReadTool(ctx context.Context, user *domain.User, c domain.ToolCall) (string, error) {
	switch c.Function.Name {
	case "search_products":
		var args struct {
			Query string `json:"query"`
		}
		d := json.NewDecoder(strings.NewReader(c.Function.Arguments))
		d.DisallowUnknownFields()
		if err := d.Decode(&args); err != nil || len([]rune(args.Query)) > 300 {
			return `{"error":"invalid search arguments"}`, nil
		}
		items, err := s.products.SearchAvailable(ctx, strings.TrimSpace(args.Query), 8)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(items)
		return string(b), err
	case "my_orders":
		var args map[string]json.RawMessage
		if json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil || len(args) != 0 {
			return `{"error":"no identity arguments allowed"}`, nil
		}
		all, err := s.orders.List(ctx, user.ID)
		if err != nil {
			return "", err
		}
		items := []domain.Order{}
		for _, o := range all {
			if o.BuyerID == user.ID {
				items = append(items, o)
				if len(items) == 8 {
					break
				}
			}
		}
		b, err := json.Marshal(items)
		return string(b), err
	default:
		return `{"error":"tool not permitted"}`, nil
	}
}
