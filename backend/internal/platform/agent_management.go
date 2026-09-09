package platform

import (
	"encoding/json"
	"net/http"
	"strings"
)

type capabilityInput struct {
	Kind        string           `json:"kind"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Config      CapabilityConfig `json:"config"`
	Enabled     bool             `json:"enabled"`
	Revision    int              `json:"revision"`
}

func (s *Server) validateCapability(r *http.Request, v *capabilityInput) error {
	if !validText(v.Name, 2, 60) || !validText(v.Description, 0, 600) {
		return fail(400, "能力名称需 2–60 字，描述不超过 600 字")
	}
	switch v.Kind {
	case "model":
		if _, ok := s.modelProvider(v.Config.Provider); !ok || len(v.Config.Model) > 128 {
			return fail(400, "请选择后端已配置的模型连接")
		}
		v.Config = CapabilityConfig{Provider: v.Config.Provider, Model: strings.TrimSpace(v.Config.Model)}
	case "tool":
		if v.Config.Tool != "search_products" && v.Config.Tool != "my_orders" {
			return fail(400, "未注册的内置工具")
		}
		v.Config = CapabilityConfig{Tool: v.Config.Tool}
	case "mcp":
		if !v.Config.ReadOnly {
			return fail(400, "仅支持管理员确认只读的 MCP 工具")
		}
		tools, e := s.listMCPTools(r.Context(), v.Config.ServerID)
		if e != nil {
			return fail(502, "无法读取 MCP 工具目录，请检查服务器连接")
		}
		found := false
		for _, tool := range tools {
			if tool.Name == v.Config.Tool {
				if !tool.Annotations.ReadOnlyHint {
					return fail(400, "该 MCP 工具未声明只读，不能绑定")
				}
				if _, e = compileToolSchema(tool.InputSchema); e != nil {
					return fail(400, "MCP 工具参数 schema 无效或引用了外部资源")
				}
				v.Config = CapabilityConfig{ServerID: v.Config.ServerID, Tool: tool.Name, ReadOnly: true, InputSchema: tool.InputSchema}
				found = true
				break
			}
		}
		if !found {
			return fail(400, "MCP 工具不存在")
		}
	default:
		return fail(400, "能力类型必须为 model、tool 或 mcp")
	}
	return nil
}
func (s *Server) createCapability(w http.ResponseWriter, r *http.Request, u *User) error {
	var v capabilityInput
	if e := decode(r, &v); e != nil {
		return e
	}
	if e := s.validateCapability(r, &v); e != nil {
		return e
	}
	cid := id("cap_")
	raw, _ := json.Marshal(v.Config)
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT INTO agent_capabilities VALUES(?,?,?,?,?,?,1,?)", cid, v.Kind, strings.TrimSpace(v.Name), v.Description, string(raw), v.Enabled, now()); e != nil {
		return e
	}
	if e = catalogAudit(tx, u.ID, "capability.create", cid, 1); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	c, e := scanCapability(s.db.QueryRowContext(r.Context(), capSelect+" WHERE id=?", cid))
	if e != nil {
		return e
	}
	return result(w, 201, c)
}
func (s *Server) updateCapability(w http.ResponseWriter, r *http.Request, u *User) error {
	var v capabilityInput
	if e := decode(r, &v); e != nil {
		return e
	}
	old, e := scanCapability(s.db.QueryRowContext(r.Context(), capSelect+" WHERE id=?", r.PathValue("id")))
	if e != nil {
		return e
	}
	if v.Revision != old.Revision {
		return fail(409, "能力已被修改，请刷新后重试")
	}
	if v.Kind != old.Kind {
		return fail(400, "已有能力不能更改类型，请新建能力")
	}
	if !v.Enabled && v.Kind == "mcp" {
		v.Config = old.Config
		if !validText(v.Name, 2, 60) || !validText(v.Description, 0, 600) {
			return fail(400, "能力名称或描述无效")
		}
	} else if e = s.validateCapability(r, &v); e != nil {
		return e
	}
	raw, _ := json.Marshal(v.Config)
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE agent_capabilities SET name=?,description=?,config=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?", strings.TrimSpace(v.Name), v.Description, string(raw), v.Enabled, now(), old.ID, v.Revision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fail(409, "能力已被修改，请刷新后重试")
	}
	if e = catalogAudit(tx, u.ID, "capability.update", old.ID, v.Revision+1); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	c, e := scanCapability(s.db.QueryRowContext(r.Context(), capSelect+" WHERE id=?", old.ID))
	if e != nil {
		return e
	}
	return result(w, 200, c)
}
func (s *Server) deleteCapability(w http.ResponseWriter, r *http.Request, u *User) error {
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	cid := r.PathValue("id")
	var count int
	e = tx.QueryRow(`SELECT COUNT(*) FROM agent_definitions d WHERE d.archived=0 AND (json_extract(d.draft,'$.model_capability_id')=? OR EXISTS(SELECT 1 FROM json_each(d.draft,'$.tool_capability_ids') WHERE value=?) OR EXISTS(SELECT 1 FROM agent_versions v WHERE v.agent_id=d.id AND v.version=d.published_version AND (json_extract(v.snapshot,'$.model.id')=? OR EXISTS(SELECT 1 FROM json_each(v.snapshot,'$.tools') WHERE json_extract(value,'$.id')=?))))`, cid, cid, cid, cid).Scan(&count)
	if e != nil {
		return e
	}
	if count > 0 {
		return fail(409, "能力仍被草稿或已发布助手引用，请先解除绑定；紧急情况下可停用能力")
	}
	res, e := tx.Exec("DELETE FROM agent_capabilities WHERE id=?", cid)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fail(404, "能力不存在")
	}
	if e = catalogAudit(tx, u.ID, "capability.delete", cid, 0); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return result(w, 200, map[string]bool{"deleted": true})
}

type agentInput struct {
	AgentDraft
	Revision int `json:"revision"`
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, u *User) error {
	var v agentInput
	if e := decode(r, &v); e != nil {
		return e
	}
	if e := validateDraft(v.AgentDraft); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = s.resolveDraft(r.Context(), tx, v.AgentDraft, false); e != nil {
		return e
	}
	aid := id("agent_")
	raw, _ := json.Marshal(v.AgentDraft)
	if _, e = tx.Exec("INSERT INTO agent_definitions(id,draft,enabled,revision,updated_at) VALUES(?,?,?,1,?)", aid, string(raw), v.Enabled, now()); e != nil {
		return e
	}
	if e = catalogAudit(tx, u.ID, "agent.create", aid, 1); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	d, e := scanDefinition(s.db.QueryRowContext(r.Context(), defSelect+" WHERE id=?", aid))
	if e != nil {
		return e
	}
	return result(w, 201, d)
}
func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request, u *User) error {
	var v agentInput
	if e := decode(r, &v); e != nil {
		return e
	}
	if e := validateDraft(v.AgentDraft); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = s.resolveDraft(r.Context(), tx, v.AgentDraft, false); e != nil {
		return e
	}
	raw, _ := json.Marshal(v.AgentDraft)
	res, e := tx.Exec("UPDATE agent_definitions SET draft=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND archived=0", string(raw), v.Enabled, now(), r.PathValue("id"), v.Revision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fail(409, "助手已被修改或移除，请刷新后重试")
	}
	if e = catalogAudit(tx, u.ID, "agent.update", r.PathValue("id"), v.Revision+1); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	d, e := scanDefinition(s.db.QueryRowContext(r.Context(), defSelect+" WHERE id=?", r.PathValue("id")))
	if e != nil {
		return e
	}
	return result(w, 200, d)
}
func (s *Server) publishAgent(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		Revision      int `json:"revision"`
		SourceVersion int `json:"source_version"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	d, e := scanDefinition(tx.QueryRowContext(r.Context(), defSelect+" WHERE id=? AND archived=0", r.PathValue("id")))
	if e != nil {
		return e
	}
	if d.Revision != v.Revision {
		return fail(409, "草稿版本已变化，请刷新后重试")
	}
	var snap AgentSnapshot
	if v.SourceVersion > 0 {
		var raw string
		if e = tx.QueryRow("SELECT snapshot FROM agent_versions WHERE agent_id=? AND version=?", d.ID, v.SourceVersion).Scan(&raw); e != nil {
			return fail(404, "历史版本不存在")
		}
		if e = json.Unmarshal([]byte(raw), &snap); e != nil {
			return e
		}
		for _, c := range append([]AgentCapability{snap.Model}, snap.Tools...) {
			var enabled bool
			if e = tx.QueryRow("SELECT enabled FROM agent_capabilities WHERE id=?", c.ID).Scan(&enabled); e != nil || !enabled {
				return fail(409, "历史版本引用的能力已删除或停用")
			}
		}
		snap.Enabled = d.Enabled
		d.Draft = snap.AgentDraft
	} else {
		snap, e = s.resolveDraft(r.Context(), tx, d.Draft, true)
		if e != nil {
			return e
		}
	}
	snap.AgentID = d.ID
	snap.Version = d.PublishedVersion + 1
	raw, _ := json.Marshal(snap)
	if _, e = tx.Exec("INSERT INTO agent_versions VALUES(?,?,?,?,?)", d.ID, snap.Version, string(raw), u.ID, now()); e != nil {
		return e
	}
	draft, _ := json.Marshal(d.Draft)
	if _, e = tx.Exec("UPDATE agent_definitions SET draft=?,published_version=?,revision=revision+1,updated_at=? WHERE id=?", string(draft), snap.Version, now(), d.ID); e != nil {
		return e
	}
	if e = catalogAudit(tx, u.ID, "agent.publish", d.ID, snap.Version); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	d, e = scanDefinition(s.db.QueryRowContext(r.Context(), defSelect+" WHERE id=?", d.ID))
	if e != nil {
		return e
	}
	return result(w, 200, d)
}
func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request, u *User) error {
	var v struct {
		Revision int `json:"revision"`
	}
	if e := decode(r, &v); e != nil {
		return e
	}
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE agent_definitions SET archived=1,enabled=0,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND archived=0", now(), r.PathValue("id"), v.Revision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fail(409, "助手已变化，请刷新后重试")
	}
	if e = catalogAudit(tx, u.ID, "agent.archive", r.PathValue("id"), v.Revision+1); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return result(w, 200, map[string]bool{"archived": true})
}
func (s *Server) agentVersions(w http.ResponseWriter, r *http.Request, _ *User) error {
	rows, e := s.db.QueryContext(r.Context(), "SELECT version,snapshot,published_by,published_at FROM agent_versions WHERE agent_id=? ORDER BY version DESC LIMIT 100", r.PathValue("id"))
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var version int
		var raw, by string
		var ts int64
		if e = rows.Scan(&version, &raw, &by, &ts); e != nil {
			return e
		}
		var snapshot AgentSnapshot
		if e = json.Unmarshal([]byte(raw), &snapshot); e != nil {
			return e
		}
		out = append(out, map[string]any{"version": version, "snapshot": snapshot, "published_by": by, "published_at": ts})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return result(w, 200, map[string]any{"items": out})
}
