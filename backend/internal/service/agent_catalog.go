package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/pkg/idgen"
	"qingfly/edu-mall/internal/pkg/toolschema"
	"qingfly/edu-mall/internal/port"
)

type AgentCatalogService struct {
	store       port.AgentStore
	connections port.ConnectionRegistry
	mcp         port.MCPGateway
}

func NewAgentCatalog(store port.AgentStore, connections port.ConnectionRegistry, mcp port.MCPGateway) *AgentCatalogService {
	return &AgentCatalogService{store: store, connections: connections, mcp: mcp}
}

type CapabilityInput struct {
	Kind        string
	Name        string
	Description string
	Config      domain.CapabilityConfig
	Enabled     bool
	Revision    int
}

type AgentInput struct {
	Draft    domain.AgentDraft
	Revision int
}

type AgentCatalogView struct {
	Capabilities []domain.AgentCapability `json:"capabilities"`
	Agents       []domain.AgentDefinition `json:"agents"`
	Providers    []ProviderView           `json:"providers"`
	MCPServers   []string                 `json:"mcp_servers"`
}

type ProviderView struct {
	ID           string `json:"id"`
	DefaultModel string `json:"default_model"`
	Ready        bool   `json:"ready"`
}

// Available 用于公开配置接口：默认模型连接齐全即可展示 Agent 入口。
func (s *AgentCatalogService) Available() bool {
	p, ok := s.connections.Model("default")
	return ok && p.BaseURL != "" && p.APIKey != "" && p.DefaultModel != ""
}

func (s *AgentCatalogService) Public(ctx context.Context) ([]domain.PublicAgent, error) {
	snaps, err := s.store.PublishedSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PublicAgent, 0, len(snaps))
	for _, snap := range snaps {
		tools := make([]domain.PublicAgentTool, 0, len(snap.Tools))
		for _, c := range snap.Tools {
			tools = append(tools, domain.PublicAgentTool{Name: c.Name, Kind: c.Kind})
		}
		out = append(out, domain.PublicAgent{ID: snap.AgentID, Name: snap.Name, Description: snap.Description, Welcome: snap.Welcome, Suggestions: snap.Suggestions, Version: snap.Version, Tools: tools, Available: s.snapshotAvailable(ctx, snap)})
	}
	return out, nil
}

func (s *AgentCatalogService) snapshotAvailable(ctx context.Context, snap domain.AgentSnapshot) bool {
	p, ok := s.connections.Model(snap.Model.Config.Provider)
	if !ok || p.APIKey == "" || p.BaseURL == "" || (snap.Model.Config.Model == "" && p.DefaultModel == "") {
		return false
	}
	caps := append([]domain.AgentCapability{snap.Model}, snap.Tools...)
	for _, c := range caps {
		enabled, err := s.store.CapabilityEnabled(ctx, c.ID)
		if err != nil || !enabled {
			return false
		}
		if c.Kind == "mcp" {
			if _, ok := s.connections.MCP(c.Config.ServerID); !ok {
				return false
			}
		}
	}
	return true
}

func (s *AgentCatalogService) AdminCatalog(ctx context.Context) (AgentCatalogView, error) {
	var out AgentCatalogView
	caps, err := s.store.Capabilities(ctx)
	if err != nil {
		return out, err
	}
	defs, err := s.store.Definitions(ctx)
	if err != nil {
		return out, err
	}
	currentCaps := map[string]int{}
	for _, c := range caps {
		currentCaps[c.ID] = c.Revision
	}
	for i := range defs {
		if defs[i].PublishedVersion == 0 {
			defs[i].HasUnpublishedChanges = true
			continue
		}
		version, err := s.store.Versions(ctx, defs[i].ID)
		if err != nil {
			return out, err
		}
		var published *domain.AgentVersion
		for j := range version {
			if version[j].Version == defs[i].PublishedVersion {
				published = &version[j]
				break
			}
		}
		if published == nil {
			return out, domain.NotFound("历史版本不存在")
		}
		snap := published.Snapshot
		draft := defs[i].Draft
		draft.Enabled = snap.Enabled
		a, _ := json.Marshal(draft)
		b, _ := json.Marshal(snap.AgentDraft)
		defs[i].HasUnpublishedChanges = string(a) != string(b)
		for _, c := range append([]domain.AgentCapability{snap.Model}, snap.Tools...) {
			if currentCaps[c.ID] != c.Revision {
				defs[i].HasUnpublishedChanges = true
			}
		}
	}
	keys := s.connections.ModelNames()
	slices.Sort(keys)
	for _, key := range keys {
		p, _ := s.connections.Model(key)
		out.Providers = append(out.Providers, ProviderView{ID: key, DefaultModel: p.DefaultModel, Ready: p.BaseURL != "" && p.APIKey != ""})
	}
	servers := s.connections.MCPServerNames()
	slices.Sort(servers)
	out.Capabilities, out.Agents, out.MCPServers = caps, defs, servers
	return out, nil
}

func (s *AgentCatalogService) Audit(ctx context.Context) ([]domain.AgentAudit, error) {
	return s.store.Audit(ctx, 100)
}

func (s *AgentCatalogService) Versions(ctx context.Context, agentID string) ([]domain.AgentVersion, error) {
	return s.store.Versions(ctx, agentID)
}

// ListMCPTools 与 CallMCPTool 暴露协议层能力，供管理端预览和集成测试直接驱动。
func (s *AgentCatalogService) ListMCPTools(ctx context.Context, serverID string) ([]domain.MCPTool, error) {
	return s.mcp.ListTools(ctx, serverID)
}

func (s *AgentCatalogService) CallMCPTool(ctx context.Context, capability domain.AgentCapability, args string) (string, error) {
	return s.mcp.CallTool(ctx, capability.Config.ServerID, capability, args, nil)
}

func (s *AgentCatalogService) DiscoverMCPTools(ctx context.Context, serverID string) ([]domain.MCPTool, error) {
	tools, err := s.mcp.ListTools(ctx, serverID)
	if err != nil {
		return nil, domain.NewError(domain.KindBadGateway, "MCP 服务不可用、协议不兼容或工具目录无效")
	}
	return tools, nil
}

func (s *AgentCatalogService) validateCapability(ctx context.Context, v *CapabilityInput) error {
	if !validText(v.Name, 2, 60) || !validText(v.Description, 0, 600) {
		return domain.Invalid("能力名称需 2–60 字，描述不超过 600 字")
	}
	switch v.Kind {
	case "model":
		if _, ok := s.connections.Model(v.Config.Provider); !ok || len(v.Config.Model) > 128 {
			return domain.Invalid("请选择后端已配置的模型连接")
		}
		v.Config = domain.CapabilityConfig{Provider: v.Config.Provider, Model: strings.TrimSpace(v.Config.Model)}
	case "tool":
		if v.Config.Tool != "search_products" && v.Config.Tool != "my_orders" {
			return domain.Invalid("未注册的内置工具")
		}
		v.Config = domain.CapabilityConfig{Tool: v.Config.Tool}
	case "mcp":
		if !v.Config.ReadOnly {
			return domain.Invalid("仅支持管理员确认只读的 MCP 工具")
		}
		tools, err := s.mcp.ListTools(ctx, v.Config.ServerID)
		if err != nil {
			return domain.NewError(domain.KindBadGateway, "无法读取 MCP 工具目录，请检查服务器连接")
		}
		found := false
		for _, tool := range tools {
			if tool.Name == v.Config.Tool {
				if !tool.Annotations.ReadOnlyHint {
					return domain.Invalid("该 MCP 工具未声明只读，不能绑定")
				}
				if _, err = toolschema.Compile(tool.InputSchema); err != nil {
					return domain.Invalid("MCP 工具参数 schema 无效或引用了外部资源")
				}
				v.Config = domain.CapabilityConfig{ServerID: v.Config.ServerID, Tool: tool.Name, ReadOnly: true, InputSchema: tool.InputSchema}
				found = true
				break
			}
		}
		if !found {
			return domain.Invalid("MCP 工具不存在")
		}
	default:
		return domain.Invalid("能力类型必须为 model、tool 或 mcp")
	}
	v.Name = strings.TrimSpace(v.Name)
	return nil
}

func (s *AgentCatalogService) CreateCapability(ctx context.Context, actor *domain.User, v CapabilityInput) (domain.AgentCapability, error) {
	if err := s.validateCapability(ctx, &v); err != nil {
		return domain.AgentCapability{}, err
	}
	cid := idgen.New("cap_")
	err := s.store.InTx(ctx, func(tx port.AgentTx) error {
		c := domain.AgentCapability{ID: cid, Kind: v.Kind, Name: v.Name, Description: v.Description, Config: v.Config, Enabled: v.Enabled, Revision: 1, UpdatedAt: now()}
		if err := tx.InsertCapability(ctx, c); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, actor.ID, "capability.create", cid, 1, now())
	})
	if err != nil {
		return domain.AgentCapability{}, err
	}
	return s.store.Capability(ctx, cid)
}

func (s *AgentCatalogService) UpdateCapability(ctx context.Context, actor *domain.User, id string, v CapabilityInput) (domain.AgentCapability, error) {
	old, err := s.store.Capability(ctx, id)
	if err != nil {
		return domain.AgentCapability{}, err
	}
	if v.Revision != old.Revision {
		return domain.AgentCapability{}, domain.Conflict("能力已被修改，请刷新后重试")
	}
	if v.Kind != old.Kind {
		return domain.AgentCapability{}, domain.Invalid("已有能力不能更改类型，请新建能力")
	}
	if !v.Enabled && v.Kind == "mcp" {
		v.Config = old.Config
		if !validText(v.Name, 2, 60) || !validText(v.Description, 0, 600) {
			return domain.AgentCapability{}, domain.Invalid("能力名称或描述无效")
		}
	} else if err = s.validateCapability(ctx, &v); err != nil {
		return domain.AgentCapability{}, err
	}
	var updated domain.AgentCapability
	err = s.store.InTx(ctx, func(tx port.AgentTx) error {
		c := old
		c.Name, c.Description, c.Config, c.Enabled, c.UpdatedAt = v.Name, v.Description, v.Config, v.Enabled, now()
		ok, err := tx.UpdateCapability(ctx, c, v.Revision)
		if err != nil {
			return err
		}
		if !ok {
			return domain.Conflict("能力已被修改，请刷新后重试")
		}
		if err = tx.InsertAudit(ctx, actor.ID, "capability.update", id, v.Revision+1, now()); err != nil {
			return err
		}
		updated = c
		return nil
	})
	if err != nil {
		return domain.AgentCapability{}, err
	}
	return s.store.Capability(ctx, updated.ID)
}

func (s *AgentCatalogService) DeleteCapability(ctx context.Context, actor *domain.User, id string) error {
	err := s.store.InTx(ctx, func(tx port.AgentTx) error {
		count, err := tx.CapabilityReferenced(ctx, id)
		if err != nil {
			return err
		}
		if count > 0 {
			return domain.Conflict("能力仍被草稿或已发布助手引用，请先解除绑定；紧急情况下可停用能力")
		}
		ok, err := tx.DeleteCapability(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return domain.NotFound("能力不存在")
		}
		return tx.InsertAudit(ctx, actor.ID, "capability.delete", id, 0, now())
	})
	return err
}

func validateDraft(d domain.AgentDraft) error {
	if !validText(d.Name, 2, 40) || !validText(d.Description, 0, 300) || !validText(d.SystemPrompt, 1, 6000) || !validText(d.Welcome, 0, 160) || d.MaxIterations < 1 || d.MaxIterations > 8 || d.MaxTokens < 128 || d.MaxTokens > 4096 || d.TimeoutSeconds < 10 || d.TimeoutSeconds > 90 || len(d.ToolIDs) > 8 || len(d.Suggestions) > 4 {
		return domain.Invalid("请检查名称、提示词与运行限额（1–8 轮、128–4096 tokens、10–90 秒）")
	}
	for _, p := range d.Suggestions {
		if !validText(p, 1, 100) {
			return domain.Invalid("建议问题需为 1–100 字")
		}
	}
	ids := map[string]bool{}
	for _, id := range d.ToolIDs {
		if ids[id] {
			return domain.Invalid("不能重复绑定同一能力")
		}
		ids[id] = true
	}
	return nil
}

func (s *AgentCatalogService) resolveDraft(ctx context.Context, tx port.AgentTx, d domain.AgentDraft, requireEnabled bool) (domain.AgentSnapshot, error) {
	out := domain.AgentSnapshot{AgentDraft: d, Tools: []domain.AgentCapability{}}
	c, err := tx.Capability(ctx, d.ModelID)
	if err != nil {
		return out, domain.Invalid("模型能力不存在")
	}
	if c.Kind != "model" || (requireEnabled && !c.Enabled) {
		return out, domain.Invalid("请选择已启用的模型能力")
	}
	out.Model = c
	for _, cid := range d.ToolIDs {
		c, err := tx.Capability(ctx, cid)
		if err != nil || c.Kind == "model" || (requireEnabled && !c.Enabled) {
			return out, domain.Invalid("工具能力不存在或已停用")
		}
		out.Tools = append(out.Tools, c)
	}
	return out, nil
}

func (s *AgentCatalogService) CreateAgent(ctx context.Context, actor *domain.User, v AgentInput) (domain.AgentDefinition, error) {
	if err := validateDraft(v.Draft); err != nil {
		return domain.AgentDefinition{}, err
	}
	aid := idgen.New("agent_")
	err := s.store.InTx(ctx, func(tx port.AgentTx) error {
		if _, err := s.resolveDraft(ctx, tx, v.Draft, false); err != nil {
			return err
		}
		d := domain.AgentDefinition{ID: aid, Draft: v.Draft, Enabled: v.Draft.Enabled, Revision: 1, UpdatedAt: now()}
		if err := tx.InsertDefinition(ctx, d); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, actor.ID, "agent.create", aid, 1, now())
	})
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	return s.store.Definition(ctx, aid)
}

func (s *AgentCatalogService) UpdateAgent(ctx context.Context, actor *domain.User, id string, v AgentInput) (domain.AgentDefinition, error) {
	if err := validateDraft(v.Draft); err != nil {
		return domain.AgentDefinition{}, err
	}
	err := s.store.InTx(ctx, func(tx port.AgentTx) error {
		if _, err := s.resolveDraft(ctx, tx, v.Draft, false); err != nil {
			return err
		}
		d := domain.AgentDefinition{ID: id, Draft: v.Draft, Enabled: v.Draft.Enabled, Revision: v.Revision, UpdatedAt: now()}
		ok, err := tx.UpdateDefinition(ctx, d, v.Revision)
		if err != nil {
			return err
		}
		if !ok {
			return domain.Conflict("助手已被修改或移除，请刷新后重试")
		}
		return tx.InsertAudit(ctx, actor.ID, "agent.update", id, v.Revision+1, now())
	})
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	return s.store.Definition(ctx, id)
}

func (s *AgentCatalogService) PublishAgent(ctx context.Context, actor *domain.User, id string, revision, sourceVersion int) (domain.AgentDefinition, error) {
	err := s.store.InTx(ctx, func(tx port.AgentTx) error {
		d, err := tx.DefinitionForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if d.Revision != revision {
			return domain.Conflict("草稿版本已变化，请刷新后重试")
		}
		var snap domain.AgentSnapshot
		if sourceVersion > 0 {
			snap, err = tx.VersionSnapshot(ctx, id, sourceVersion)
			if err != nil {
				return err
			}
			for _, c := range append([]domain.AgentCapability{snap.Model}, snap.Tools...) {
				enabled, err := tx.Capability(ctx, c.ID)
				if err != nil || !enabled.Enabled {
					return domain.Conflict("历史版本引用的能力已删除或停用")
				}
			}
			snap.Enabled = d.Enabled
			d.Draft = snap.AgentDraft
		} else {
			snap, err = s.resolveDraft(ctx, tx, d.Draft, true)
			if err != nil {
				return err
			}
		}
		snap.AgentID = d.ID
		snap.Version = d.PublishedVersion + 1
		if err = tx.InsertVersion(ctx, d.ID, snap.Version, snap, actor.ID, now()); err != nil {
			return err
		}
		if err = tx.UpdatePublished(ctx, d.ID, d.Draft, snap.Version, now()); err != nil {
			return err
		}
		return tx.InsertAudit(ctx, actor.ID, "agent.publish", d.ID, snap.Version, now())
	})
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	return s.store.Definition(ctx, id)
}

func (s *AgentCatalogService) ArchiveAgent(ctx context.Context, actor *domain.User, id string, revision int) error {
	return s.store.InTx(ctx, func(tx port.AgentTx) error {
		ok, err := tx.ArchiveDefinition(ctx, id, revision, now())
		if err != nil {
			return err
		}
		if !ok {
			return domain.Conflict("助手已变化，请刷新后重试")
		}
		return tx.InsertAudit(ctx, actor.ID, "agent.archive", id, revision+1, now())
	})
}

// Snapshot 返回已发布快照，供 Agent 运行时与测试使用。
func (s *AgentCatalogService) Snapshot(ctx context.Context, agentID string) (domain.AgentSnapshot, error) {
	return s.store.SnapshotForAgent(ctx, agentID)
}

// Init 只在首次启动时写入默认能力/助手，并回填旧线程绑定。
func (s *AgentCatalogService) Init(ctx context.Context) error {
	caps := []domain.AgentCapability{
		{ID: "cap_default_model", Kind: "model", Name: "默认对话模型", Description: "使用服务器配置的默认模型", Config: domain.CapabilityConfig{Provider: "default"}, Enabled: true, Revision: 1},
		{ID: "cap_search_products", Kind: "tool", Name: "搜索校园好物", Description: "读取在售二手商品，金额单位为分", Config: domain.CapabilityConfig{Tool: "search_products"}, Enabled: true, Revision: 1},
		{ID: "cap_my_orders", Kind: "tool", Name: "查询我的订单", Description: "只读取当前登录用户自己的订单", Config: domain.CapabilityConfig{Tool: "my_orders"}, Enabled: true, Revision: 1},
	}
	draft := domain.AgentDraft{Name: "小集", Description: "你的校园购物助手", SystemPrompt: "帮助同学搜索好物、比较价格、查询自己的订单并准备面交验货清单。需要时先询问预算与校区。", Welcome: "今天想发现点什么？", Suggestions: []string{"300 元以内，有什么值得入手的好物？", "买二手相机，面交时要检查什么？", "帮我看看最近的订单状态", "想给宿舍添点氛围感，有推荐吗？"}, ModelID: "cap_default_model", ToolIDs: []string{"cap_search_products", "cap_my_orders"}, MaxIterations: 5, MaxTokens: 1800, TimeoutSeconds: 90, Enabled: true}
	snap := domain.AgentSnapshot{AgentID: domain.DefaultAgentID, Version: 1, AgentDraft: draft, Model: caps[0], Tools: []domain.AgentCapability{caps[1], caps[2]}}
	def := domain.AgentDefinition{ID: domain.DefaultAgentID, Draft: draft, Enabled: true, Revision: 1, PublishedVersion: 1, UpdatedAt: now()}
	if err := s.store.Seed(ctx, caps, def, snap, now()); err != nil {
		return err
	}
	return s.store.BackfillThreadBindings(ctx, domain.DefaultAgentID)
}
