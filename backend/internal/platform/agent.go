package platform

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"qingfly/edu-mall/internal/infra/mysqlx"
)

type AgentRun struct {
	AgentID      string `json:"agent_id"`
	AgentVersion int    `json:"agent_version"`
	AgentName    string `json:"agent_name"`
	ID           string `json:"id"`
	ThreadID     string `json:"thread_id"`
	Prompt       string `json:"prompt"`
	Answer       string `json:"answer"`
	Status       string `json:"status"`
	Error        string `json:"error"`
	CreatedAt    int64  `json:"created_at"`
}

func (s *Server) agentThreads(w http.ResponseWriter, r *http.Request, u *User) error {
	rows, e := s.db.QueryContext(r.Context(), "SELECT t.id,t.title,t.created_at,COALESCE(b.agent_id,?) FROM agent_threads t LEFT JOIN agent_thread_bindings b ON b.thread_id=t.id WHERE t.user_id=? ORDER BY t.created_at DESC LIMIT 100", defaultAgentID, u.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title, aid string
		var created int64
		if e = rows.Scan(&id, &title, &created, &aid); e != nil {
			return e
		}
		items = append(items, map[string]any{"id": id, "title": title, "created_at": created, "agent_id": aid})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"items": items})
}
func (s *Server) agentHistory(w http.ResponseWriter, r *http.Request, u *User) error {
	var title string
	if e := s.db.QueryRowContext(r.Context(), "SELECT title FROM agent_threads WHERE id=? AND user_id=?", r.PathValue("id"), u.ID).Scan(&title); e != nil {
		return e
	}
	runs, e := s.threadRuns(r.Context(), u.ID, r.PathValue("id"))
	if e != nil {
		return e
	}
	var aid string
	if e := s.db.QueryRowContext(r.Context(), "SELECT COALESCE(b.agent_id,?) FROM agent_threads t LEFT JOIN agent_thread_bindings b ON b.thread_id=t.id WHERE t.id=?", defaultAgentID, r.PathValue("id")).Scan(&aid); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"items": runs, "title": title, "agent_id": aid})
}
func (s *Server) threadRuns(ctx context.Context, uid, tid string) ([]AgentRun, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT r.id,r.thread_id,r.prompt,r.answer,r.status,r.error,r.created_at,COALESCE(s.agent_id,?),COALESCE(s.version,0),COALESCE(JSON_UNQUOTE(JSON_EXTRACT(s.snapshot,'$.name')),'小集') FROM (SELECT id,thread_id,prompt,answer,status,error,created_at FROM agent_runs WHERE user_id=? AND thread_id=? ORDER BY created_at DESC LIMIT 30) r LEFT JOIN agent_run_snapshots s ON s.run_id=r.id ORDER BY r.created_at", defaultAgentID, uid, tid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	runs := []AgentRun{}
	for rows.Next() {
		var v AgentRun
		if e = rows.Scan(&v.ID, &v.ThreadID, &v.Prompt, &v.Answer, &v.Status, &v.Error, &v.CreatedAt, &v.AgentID, &v.AgentVersion, &v.AgentName); e != nil {
			return nil, e
		}
		runs = append(runs, v)
	}
	return runs, rows.Err()
}
func streamEvent(w http.ResponseWriter, kind string, data any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b); e != nil {
		return e
	}
	return http.NewResponseController(w).Flush()
}
func (s *Server) agentRun(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		AgentID  string `json:"agent_id"`
		Prompt   string `json:"prompt"`
		ThreadID string `json:"thread_id"`
		Idem     string `json:"idempotency_key"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	v.Prompt = strings.TrimSpace(v.Prompt)
	if utf8.RuneCountInString(v.Prompt) < 1 || utf8.RuneCountInString(v.Prompt) > 2000 || !keyValid(v.Idem) {
		return fail(400, "请输入 1–2000 字问题和有效请求 ID")
	}
	// Every run has a 90 second deadline. Reap abandoned leases after two minutes
	// so a failed final save cannot permanently prevent this user from chatting.
	if _, e := s.db.ExecContext(r.Context(), "UPDATE agent_runs SET status='failed',error='上次运行已超时，请重新发送' WHERE user_id=? AND status='running' AND created_at<?", u.ID, now()-120000); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	run := AgentRun{}
	e = tx.QueryRow("SELECT id,thread_id,prompt,answer,status,error,created_at FROM agent_runs WHERE user_id=? AND idem=?", u.ID, v.Idem).Scan(&run.ID, &run.ThreadID, &run.Prompt, &run.Answer, &run.Status, &run.Error, &run.CreatedAt)
	if e == nil {
		run.AgentID = defaultAgentID
		var raw string
		if e := tx.QueryRow("SELECT snapshot FROM agent_run_snapshots WHERE run_id=?", run.ID).Scan(&raw); e == nil {
			var snap AgentSnapshot
			if e = json.Unmarshal([]byte(raw), &snap); e != nil {
				return e
			}
			run.AgentID = snap.AgentID
			run.AgentVersion = snap.Version
			run.AgentName = snap.Name
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if v.AgentID != "" && v.AgentID != run.AgentID {
			return fail(409, "请求 ID 已用于其他助手")
		}
		if run.Prompt != v.Prompt || (v.ThreadID != "" && v.ThreadID != run.ThreadID) {
			return fail(409, "请求 ID 已用于其他对话")
		}
		if run.Status == "running" {
			return fail(409, "这条消息正在处理中")
		}
		_ = tx.Rollback()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
		w.Header().Set("Content-Type", "text/event-stream")
		if e := streamEvent(w, "start", run); e != nil {
			return nil
		}
		if run.Answer != "" {
			if e := streamEvent(w, "delta", map[string]string{"text": run.Answer}); e != nil {
				return nil
			}
		}
		_ = streamEvent(w, "done", run)
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	aid := v.AgentID
	if v.ThreadID != "" {
		var bound string
		if e = tx.QueryRow("SELECT COALESCE(b.agent_id,?) FROM agent_threads t LEFT JOIN agent_thread_bindings b ON b.thread_id=t.id WHERE t.id=? AND t.user_id=?", defaultAgentID, v.ThreadID, u.ID).Scan(&bound); e != nil {
			return e
		}
		if aid != "" && aid != bound {
			return fail(409, "这段对话属于另一位助手，请新建对话")
		}
		aid = bound
	}
	if aid == "" {
		aid = defaultAgentID
	}
	snapshot, e := s.loadSnapshot(r.Context(), tx, aid)
	if e != nil {
		return e
	}
	if !s.snapshotAvailable(r.Context(), tx, snapshot) {
		return fail(503, "该助手的模型或工具暂不可用，请选择其他助手")
	}
	var running int
	if e = tx.QueryRow("SELECT COUNT(*) FROM agent_runs WHERE user_id=? AND status='running'", u.ID).Scan(&running); e != nil {
		return e
	}
	if running > 0 {
		return fail(409, "请等待当前回答结束，或先停止生成")
	}
	if !s.allow(r, "agent:"+u.ID, 10, time.Minute) {
		return fail(429, "请求较多，请稍后再试")
	}
	tid := v.ThreadID
	if tid == "" {
		tid = id("thread_")
		title := []rune(v.Prompt)
		if len(title) > 30 {
			title = title[:30]
		}
		if _, e = tx.Exec("INSERT INTO agent_threads (id,user_id,title,created_at) VALUES(?,?,?,?)", tid, u.ID, string(title), now()); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO agent_thread_bindings (thread_id,agent_id) VALUES(?,?)", tid, aid); e != nil {
			return e
		}
	} else {
		var exists int
		if e = tx.QueryRow("SELECT 1 FROM agent_threads WHERE id=? AND user_id=?", tid, u.ID).Scan(&exists); e != nil {
			return e
		}
	}
	run = AgentRun{ID: id("run_"), ThreadID: tid, Prompt: v.Prompt, Status: "running", CreatedAt: now()}
	run.AgentID = aid
	run.AgentVersion = snapshot.Version
	run.AgentName = snapshot.Name
	if snapshot.Model.Config.Model == "" {
		provider, _ := s.modelProvider(snapshot.Model.Config.Provider)
		snapshot.Model.Config.Model = provider.DefaultModel
	}
	if _, e = tx.Exec("INSERT INTO agent_runs(id,thread_id,user_id,idem,prompt,status,created_at) VALUES(?,?,?,?,?,?,?)", run.ID, tid, u.ID, v.Idem, v.Prompt, run.Status, run.CreatedAt); e != nil {
		return e
	}
	snapshotJSON, _ := json.Marshal(snapshot)
	if _, e = tx.Exec("INSERT INTO agent_run_snapshots (run_id,agent_id,version,snapshot) VALUES(?,?,?,?)", run.ID, aid, snapshot.Version, string(snapshotJSON)); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = streamEvent(w, "start", run)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(snapshot.TimeoutSeconds)*time.Second)
	defer cancel()
	answer, err := s.executeAgent(ctx, w, u, run, snapshot)
	run.Answer = answer
	run.Status = "completed"
	if err != nil {
		run.Status = "failed"
		run.Error = "AI 暂时无法完成回答，请重试"
		if errors.Is(r.Context().Err(), context.Canceled) {
			run.Status = "cancelled"
			run.Error = "已停止生成"
		}
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer saveCancel()
	_, saveErr := s.db.ExecContext(saveCtx, "UPDATE agent_runs SET answer=?,status=?,error=? WHERE id=?", run.Answer, run.Status, run.Error, run.ID)
	if saveErr != nil {
		_ = streamEvent(w, "error", map[string]string{"message": "回答保存失败，请稍后查看历史"})
		return nil
	}
	if run.Error != "" {
		_ = streamEvent(w, "error", map[string]string{"message": run.Error})
	}
	_ = streamEvent(w, "done", run)
	return nil
}

type toolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (s *Server) executeAgent(ctx context.Context, w http.ResponseWriter, u *User, run AgentRun, snapshot AgentSnapshot) (string, error) {
	history, e := s.threadRuns(ctx, u.ID, run.ThreadID)
	if e != nil {
		return "", e
	}
	messages := []map[string]any{{"role": "system", "content": agentSafetyPrompt + "\n" + snapshot.SystemPrompt}}
	for _, h := range history {
		messages = append(messages, map[string]any{"role": "user", "content": h.Prompt})
		if h.Answer != "" {
			messages = append(messages, map[string]any{"role": "assistant", "content": h.Answer})
		}
	}
	tools := snapshotToolDefinitions(snapshot)
	provider, _ := s.modelProvider(snapshot.Model.Config.Provider)
	model := snapshot.Model.Config.Model
	if model == "" {
		model = provider.DefaultModel
	}
	var answer strings.Builder
	for step := 0; step < snapshot.MaxIterations; step++ {
		if ctx.Err() != nil {
			return answer.String(), ctx.Err()
		}
		if e := s.checkRunEnabled(ctx, snapshot); e != nil {
			return answer.String(), e
		}
		body := map[string]any{"model": model, "messages": messages, "stream": true, "max_tokens": snapshot.MaxTokens}
		if len(tools) > 0 {
			body["tools"] = tools
		}
		payload, _ := json.Marshal(body)
		req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
		if e != nil {
			return answer.String(), e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
		resp, e := s.client.Do(req)
		if e != nil {
			return answer.String(), e
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return answer.String(), errors.New("model request failed")
		}
		calls := map[int]*toolCall{}
		var turn strings.Builder
		scan := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
		scan.Buffer(make([]byte, 4096), 1<<20)
		finished := false
		finishReason := ""
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				finished = true
				break
			}
			var event struct {
				Choices []struct {
					Delta struct {
						Content   string     `json:"content"`
						ToolCalls []toolCall `json:"tool_calls"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if e = json.Unmarshal([]byte(data), &event); e != nil {
				resp.Body.Close()
				return answer.String(), e
			}
			for _, choice := range event.Choices {
				if choice.FinishReason != nil {
					finished = true
					finishReason = *choice.FinishReason
				}
				if text := choice.Delta.Content; text != "" {
					turn.WriteString(text)
					answer.WriteString(text)
					if answer.Len() > 100000 {
						resp.Body.Close()
						return answer.String(), errors.New("output limit")
					}
					if e = streamEvent(w, "delta", map[string]string{"text": text}); e != nil {
						resp.Body.Close()
						return answer.String(), e
					}
				}
				for _, d := range choice.Delta.ToolCalls {
					if d.Index < 0 || d.Index > 7 {
						resp.Body.Close()
						return answer.String(), errors.New("tool limit")
					}
					c := calls[d.Index]
					if c == nil {
						c = &toolCall{Type: "function"}
						calls[d.Index] = c
					}
					if d.ID != "" {
						c.ID = d.ID
					}
					c.Function.Name += d.Function.Name
					c.Function.Arguments += d.Function.Arguments
					if len(c.Function.Arguments) > 8192 {
						resp.Body.Close()
						return answer.String(), errors.New("tool arguments too large")
					}
				}
			}
		}
		scanErr := scan.Err()
		resp.Body.Close()
		if scanErr != nil {
			return answer.String(), scanErr
		}
		if !finished {
			return answer.String(), errors.New("incomplete model stream")
		}
		if finishReason != "stop" && finishReason != "tool_calls" {
			return answer.String(), errors.New("model output incomplete or filtered")
		}
		if len(calls) == 0 {
			if turn.Len() == 0 {
				return answer.String(), errors.New("empty model answer")
			}
			return answer.String(), nil
		}
		list := []toolCall{}
		for i := 0; i < 8; i++ {
			if c := calls[i]; c != nil {
				if c.ID == "" {
					return answer.String(), errors.New("missing tool call id")
				}
				list = append(list, *c)
			}
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": turn.String(), "tool_calls": list})
		for _, c := range list {
			label := boundToolLabel(snapshot, c.Function.Name)
			_ = streamEvent(w, "tool", map[string]string{"name": c.Function.Name, "label": label, "status": "running"})
			out, e := s.executeBoundTool(ctx, u, run.ID, snapshot, c)
			if e != nil {
				_ = streamEvent(w, "tool", map[string]string{"name": c.Function.Name, "label": label, "status": "failed"})
				var recoverable *toolResultError
				if !errors.As(e, &recoverable) {
					return answer.String(), e
				}
				failure, _ := json.Marshal(map[string]string{"error": recoverable.message})
				out = string(failure)
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": c.ID, "content": out})
			if e == nil {
				_ = streamEvent(w, "tool", map[string]string{"name": c.Function.Name, "label": label, "status": "completed"})
			}
		}
	}
	return answer.String(), errors.New("tool iteration limit reached")
}
func (s *Server) readTool(ctx context.Context, u *User, c toolCall) (string, error) {
	switch c.Function.Name {
	case "search_products":
		var args struct {
			Query string `json:"query"`
		}
		d := json.NewDecoder(strings.NewReader(c.Function.Arguments))
		d.DisallowUnknownFields()
		if e := d.Decode(&args); e != nil || len(args.Query) > 300 {
			return `{"error":"invalid search arguments"}`, nil
		}
		pattern := "%" + mysqlx.EscapeLike(strings.TrimSpace(args.Query)) + "%"
		rows, e := s.db.QueryContext(ctx, productSelect+" WHERE p.status='available' AND (p.title LIKE ? OR p.description LIKE ?) ORDER BY p.created_at DESC,p.id DESC LIMIT 8", pattern, pattern)
		if e != nil {
			return "", e
		}
		defer rows.Close()
		items := []Product{}
		for rows.Next() {
			p, e := scanProduct(rows)
			if e != nil {
				return "", e
			}
			items = append(items, p)
		}
		if e = rows.Err(); e != nil {
			return "", e
		}
		b, e := json.Marshal(items)
		return string(b), e
	case "my_orders":
		var args map[string]json.RawMessage
		if json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil || len(args) != 0 {
			return `{"error":"no identity arguments allowed"}`, nil
		}
		rows, e := s.db.QueryContext(ctx, orderSelect+" WHERE buyer_id=? ORDER BY created_at DESC LIMIT 8", u.ID)
		if e != nil {
			return "", e
		}
		defer rows.Close()
		items := []Order{}
		for rows.Next() {
			o, e := scanOrder(rows)
			if e != nil {
				return "", e
			}
			items = append(items, o)
		}
		if e = rows.Err(); e != nil {
			return "", e
		}
		b, e := json.Marshal(items)
		return string(b), e
	default:
		return `{"error":"tool not permitted"}`, nil
	}
}
