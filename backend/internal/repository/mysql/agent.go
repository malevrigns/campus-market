package mysqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"qingfly/edu-mall/internal/domain"
	"qingfly/edu-mall/internal/infra/mysqlx"
	"qingfly/edu-mall/internal/port"
)

type AgentRepo struct{ *base }

const capabilitySelect = "SELECT id,kind,name,description,config,enabled,revision,updated_at FROM agent_capabilities"
const definitionSelect = "SELECT id,draft,enabled,revision,published_version,updated_at FROM agent_definitions"

func scanCapability(row interface{ Scan(...any) error }) (domain.AgentCapability, error) {
	var c domain.AgentCapability
	var raw string
	err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.Description, &raw, &c.Enabled, &c.Revision, &c.UpdatedAt)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c.Config)
	}
	return c, err
}

func scanDefinition(row interface{ Scan(...any) error }) (domain.AgentDefinition, error) {
	var d domain.AgentDefinition
	var raw string
	err := row.Scan(&d.ID, &raw, &d.Enabled, &d.Revision, &d.PublishedVersion, &d.UpdatedAt)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &d.Draft)
	}
	return d, err
}

func (s *AgentRepo) Threads(ctx context.Context, userID string) ([]domain.AgentThread, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT t.id,t.title,t.created_at,COALESCE(b.agent_id,?) FROM agent_threads t LEFT JOIN agent_thread_bindings b ON b.thread_id=t.id WHERE t.user_id=? ORDER BY t.created_at DESC LIMIT 100", domain.DefaultAgentID, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	items := []domain.AgentThread{}
	for rows.Next() {
		var t domain.AgentThread
		if err := rows.Scan(&t.ID, &t.Title, &t.CreatedAt, &t.AgentID); err != nil {
			return nil, translate(err)
		}
		items = append(items, t)
	}
	return items, translate(rows.Err())
}

func (s *AgentRepo) ThreadTitle(ctx context.Context, threadID, userID string) (string, error) {
	var title string
	err := s.db.QueryRowContext(ctx, "SELECT title FROM agent_threads WHERE id=? AND user_id=?", threadID, userID).Scan(&title)
	return title, notFound(err, "内容不存在或无权访问")
}

func (s *AgentRepo) ThreadAgent(ctx context.Context, threadID, userID string) (string, error) {
	var agentID string
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(b.agent_id,?) FROM agent_threads t LEFT JOIN agent_thread_bindings b ON b.thread_id=t.id WHERE t.id=? AND t.user_id=?", domain.DefaultAgentID, threadID, userID).Scan(&agentID)
	return agentID, notFound(err, "内容不存在或无权访问")
}

func (s *AgentRepo) Runs(ctx context.Context, userID, threadID string, limit int) ([]domain.AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT r.id,r.thread_id,r.prompt,r.answer,r.status,r.error,r.created_at,COALESCE(s.agent_id,?),COALESCE(s.version,0),COALESCE(JSON_UNQUOTE(JSON_EXTRACT(s.snapshot,'$.name')),'小集') FROM (SELECT id,thread_id,prompt,answer,status,error,created_at FROM agent_runs WHERE user_id=? AND thread_id=? ORDER BY created_at DESC LIMIT ?) r LEFT JOIN agent_run_snapshots s ON s.run_id=r.id ORDER BY r.created_at", domain.DefaultAgentID, userID, threadID, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	runs := []domain.AgentRun{}
	for rows.Next() {
		var v domain.AgentRun
		if err := rows.Scan(&v.ID, &v.ThreadID, &v.Prompt, &v.Answer, &v.Status, &v.Error, &v.CreatedAt, &v.AgentID, &v.AgentVersion, &v.AgentName); err != nil {
			return nil, translate(err)
		}
		runs = append(runs, v)
	}
	return runs, translate(rows.Err())
}

func (s *AgentRepo) FindRunByIdem(ctx context.Context, userID, idem string) (domain.AgentRun, domain.AgentSnapshot, bool, error) {
	var run domain.AgentRun
	err := s.db.QueryRowContext(ctx, "SELECT id,thread_id,prompt,answer,status,error,created_at FROM agent_runs WHERE user_id=? AND idem=?", userID, idem).Scan(&run.ID, &run.ThreadID, &run.Prompt, &run.Answer, &run.Status, &run.Error, &run.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run, domain.AgentSnapshot{}, false, nil
	}
	if err != nil {
		return run, domain.AgentSnapshot{}, false, err
	}
	run.AgentID = domain.DefaultAgentID
	var snap domain.AgentSnapshot
	var raw string
	if err := s.db.QueryRowContext(ctx, "SELECT snapshot FROM agent_run_snapshots WHERE run_id=?", run.ID).Scan(&raw); err == nil {
		if err = json.Unmarshal([]byte(raw), &snap); err != nil {
			return run, snap, false, err
		}
		run.AgentID, run.AgentVersion, run.AgentName = snap.AgentID, snap.Version, snap.Name
	} else if !errors.Is(err, sql.ErrNoRows) {
		return run, snap, false, err
	}
	return run, snap, true, nil
}

func (s *AgentRepo) RunningCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_runs WHERE user_id=? AND status='running'", userID).Scan(&n)
	return n, translate(err)
}

func (s *AgentRepo) FailStaleRuns(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE agent_runs SET status='failed',error='上次运行已超时，请重新发送' WHERE user_id<>'' AND status='running' AND created_at<?", before)
	return translate(err)
}

func (s *AgentRepo) SaveRunResult(ctx context.Context, runID, answer, status, errorMessage string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE agent_runs SET answer=?,status=?,error=? WHERE id=?", answer, status, errorMessage, runID)
	return translate(err)
}

func (s *AgentRepo) RecordToolEvent(ctx context.Context, runID, capabilityID, name, status string, at int64) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO agent_tool_events(run_id,capability_id,name,status,created_at) VALUES(?,?,?,?,?)", runID, capabilityID, name, status, at)
	return translate(err)
}

func (s *AgentRepo) Capabilities(ctx context.Context) ([]domain.AgentCapability, error) {
	rows, err := s.db.QueryContext(ctx, capabilitySelect+" ORDER BY kind,name")
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	caps := []domain.AgentCapability{}
	for rows.Next() {
		c, err := scanCapability(rows)
		if err != nil {
			return nil, translate(err)
		}
		caps = append(caps, c)
	}
	return caps, translate(rows.Err())
}

func (s *AgentRepo) Capability(ctx context.Context, id string) (domain.AgentCapability, error) {
	c, err := scanCapability(s.db.QueryRowContext(ctx, capabilitySelect+" WHERE id=?", id))
	return c, notFound(err, "能力不存在")
}

func (s *AgentRepo) Definitions(ctx context.Context) ([]domain.AgentDefinition, error) {
	rows, err := s.db.QueryContext(ctx, definitionSelect+" WHERE archived=0 ORDER BY updated_at DESC")
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	defs := []domain.AgentDefinition{}
	for rows.Next() {
		d, err := scanDefinition(rows)
		if err != nil {
			return nil, translate(err)
		}
		defs = append(defs, d)
	}
	return defs, translate(rows.Err())
}

func (s *AgentRepo) Definition(ctx context.Context, id string) (domain.AgentDefinition, error) {
	d, err := scanDefinition(s.db.QueryRowContext(ctx, definitionSelect+" WHERE id=?", id))
	return d, notFound(err, "助手不存在")
}

func (s *AgentRepo) PublishedSnapshots(ctx context.Context) ([]domain.AgentSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT v.snapshot FROM agent_definitions d JOIN agent_versions v ON v.agent_id=d.id AND v.version=d.published_version WHERE d.enabled=1 AND d.archived=0 ORDER BY d.id=? DESC,d.updated_at DESC", domain.DefaultAgentID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	snaps := []domain.AgentSnapshot{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, translate(err)
		}
		var snap domain.AgentSnapshot
		if err := json.Unmarshal([]byte(raw), &snap); err != nil {
			return nil, err
		}
		snaps = append(snaps, snap)
	}
	return snaps, translate(rows.Err())
}

func (s *AgentRepo) SnapshotForAgent(ctx context.Context, agentID string) (domain.AgentSnapshot, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT v.snapshot FROM agent_definitions d JOIN agent_versions v ON v.agent_id=d.id AND v.version=d.published_version WHERE d.id=? AND d.enabled=1 AND d.archived=0", agentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSnapshot{}, domain.NotFound("助手尚未发布或已停用")
	}
	if err != nil {
		return domain.AgentSnapshot{}, err
	}
	var snap domain.AgentSnapshot
	err = json.Unmarshal([]byte(raw), &snap)
	return snap, err
}

func (s *AgentRepo) RunSnapshot(ctx context.Context, runID string) (domain.AgentSnapshot, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT snapshot FROM agent_run_snapshots WHERE run_id=?", runID).Scan(&raw)
	if err != nil {
		return domain.AgentSnapshot{}, notFound(err, "内容不存在或无权访问")
	}
	var snap domain.AgentSnapshot
	err = json.Unmarshal([]byte(raw), &snap)
	return snap, err
}

func (s *AgentRepo) Versions(ctx context.Context, agentID string) ([]domain.AgentVersion, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT version,snapshot,published_by,published_at FROM agent_versions WHERE agent_id=? ORDER BY version DESC LIMIT 100", agentID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.AgentVersion{}
	for rows.Next() {
		var v domain.AgentVersion
		var raw string
		if err := rows.Scan(&v.Version, &raw, &v.PublishedBy, &v.PublishedAt); err != nil {
			return nil, translate(err)
		}
		if err := json.Unmarshal([]byte(raw), &v.Snapshot); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, translate(rows.Err())
}

func (s *AgentRepo) Audit(ctx context.Context, limit int) ([]domain.AgentAudit, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT actor_id,action,target_id,revision,created_at FROM agent_audit ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	out := []domain.AgentAudit{}
	for rows.Next() {
		var a domain.AgentAudit
		if err := rows.Scan(&a.ActorID, &a.Action, &a.TargetID, &a.Revision, &a.CreatedAt); err != nil {
			return nil, translate(err)
		}
		out = append(out, a)
	}
	return out, translate(rows.Err())
}

func (s *AgentRepo) Enabled(ctx context.Context, agentID string) (bool, bool, error) {
	var enabled, archived bool
	err := s.db.QueryRowContext(ctx, "SELECT enabled,archived FROM agent_definitions WHERE id=?", agentID).Scan(&enabled, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return enabled, archived, translate(err)
}

func (s *AgentRepo) CapabilityEnabled(ctx context.Context, id string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, "SELECT enabled FROM agent_capabilities WHERE id=?", id).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled, translate(err)
}

func (s *AgentRepo) Seed(ctx context.Context, caps []domain.AgentCapability, def domain.AgentDefinition, snap domain.AgentSnapshot, at int64) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		var seeded int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_definitions WHERE id=?", def.ID).Scan(&seeded); err != nil {
			return err
		}
		if seeded != 0 {
			return nil
		}
		for _, c := range caps {
			raw, _ := json.Marshal(c.Config)
			if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO agent_capabilities (id,kind,name,description,config,enabled,revision,updated_at) VALUES(?,?,?,?,?,?,?,?)", c.ID, c.Kind, c.Name, c.Description, string(raw), c.Enabled, 1, at); err != nil {
				return err
			}
		}
		draft, _ := json.Marshal(def.Draft)
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO agent_definitions (id,draft,enabled,revision,published_version,updated_at) VALUES(?,?,1,1,1,?)", def.ID, string(draft), at); err != nil {
			return err
		}
		raw, _ := json.Marshal(snap)
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO agent_versions (agent_id,version,snapshot,published_by,published_at) VALUES(?,?,?,?,?)", def.ID, 1, string(raw), "system", at); err != nil {
			return err
		}
		return nil
	}))
}

func (s *AgentRepo) BackfillThreadBindings(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, "INSERT IGNORE INTO agent_thread_bindings (thread_id,agent_id) SELECT t.id,? FROM agent_threads t LEFT JOIN agent_thread_bindings b ON b.thread_id=t.id WHERE b.thread_id IS NULL", agentID)
	return translate(err)
}

func (s *AgentRepo) InTx(ctx context.Context, fn func(port.AgentTx) error) error {
	return translate(mysqlx.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(&Tx{tx: tx})
	}))
}

// ---------- AgentTx ----------

func (t *Tx) Capability(ctx context.Context, id string) (domain.AgentCapability, error) {
	c, err := scanCapability(t.tx.QueryRowContext(ctx, capabilitySelect+" WHERE id=?", id))
	return c, notFound(err, "能力不存在")
}

func (t *Tx) InsertCapability(ctx context.Context, c domain.AgentCapability) error {
	raw, _ := json.Marshal(c.Config)
	_, err := t.tx.ExecContext(ctx, "INSERT INTO agent_capabilities (id,kind,name,description,config,enabled,revision,updated_at) VALUES(?,?,?,?,?,?,1,?)", c.ID, c.Kind, c.Name, c.Description, string(raw), c.Enabled, c.UpdatedAt)
	return err
}

func (t *Tx) UpdateCapability(ctx context.Context, c domain.AgentCapability, expectedRevision int) (bool, error) {
	raw, _ := json.Marshal(c.Config)
	res, err := t.tx.ExecContext(ctx, "UPDATE agent_capabilities SET name=?,description=?,config=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?", c.Name, c.Description, string(raw), c.Enabled, c.UpdatedAt, c.ID, expectedRevision)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *Tx) CapabilityReferenced(ctx context.Context, id string) (int, error) {
	var count int
	err := t.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_definitions d WHERE d.archived=0 AND (JSON_UNQUOTE(JSON_EXTRACT(d.draft,'$.model_capability_id'))=? OR JSON_CONTAINS(JSON_EXTRACT(d.draft,'$.tool_capability_ids'),JSON_QUOTE(?)) OR EXISTS(SELECT 1 FROM agent_versions v WHERE v.agent_id=d.id AND v.version=d.published_version AND (JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.model.id'))=? OR JSON_CONTAINS(JSON_EXTRACT(v.snapshot,'$.tools[*].id'),JSON_QUOTE(?)))))`, id, id, id, id).Scan(&count)
	return count, err
}

func (t *Tx) DeleteCapability(ctx context.Context, id string) (bool, error) {
	res, err := t.tx.ExecContext(ctx, "DELETE FROM agent_capabilities WHERE id=?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *Tx) InsertDefinition(ctx context.Context, d domain.AgentDefinition) error {
	raw, _ := json.Marshal(d.Draft)
	_, err := t.tx.ExecContext(ctx, "INSERT INTO agent_definitions(id,draft,enabled,revision,updated_at) VALUES(?,?,?,1,?)", d.ID, string(raw), d.Enabled, d.UpdatedAt)
	return err
}

func (t *Tx) UpdateDefinition(ctx context.Context, d domain.AgentDefinition, expectedRevision int) (bool, error) {
	raw, _ := json.Marshal(d.Draft)
	res, err := t.tx.ExecContext(ctx, "UPDATE agent_definitions SET draft=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND archived=0", string(raw), d.Enabled, d.UpdatedAt, d.ID, expectedRevision)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *Tx) Definition(ctx context.Context, id string) (domain.AgentDefinition, error) {
	d, err := scanDefinition(t.tx.QueryRowContext(ctx, definitionSelect+" WHERE id=?", id))
	return d, notFound(err, "助手不存在")
}

func (t *Tx) DefinitionForUpdate(ctx context.Context, id string) (domain.AgentDefinition, error) {
	d, err := scanDefinition(t.tx.QueryRowContext(ctx, definitionSelect+" WHERE id=? AND archived=0 FOR UPDATE", id))
	return d, notFound(err, "助手不存在")
}

func (t *Tx) ArchiveDefinition(ctx context.Context, id string, expectedRevision int, at int64) (bool, error) {
	res, err := t.tx.ExecContext(ctx, "UPDATE agent_definitions SET archived=1,enabled=0,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND archived=0", at, id, expectedRevision)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (t *Tx) InsertVersion(ctx context.Context, agentID string, version int, snapshot domain.AgentSnapshot, publishedBy string, at int64) error {
	raw, _ := json.Marshal(snapshot)
	_, err := t.tx.ExecContext(ctx, "INSERT INTO agent_versions (agent_id,version,snapshot,published_by,published_at) VALUES(?,?,?,?,?)", agentID, version, string(raw), publishedBy, at)
	return err
}

func (t *Tx) UpdatePublished(ctx context.Context, agentID string, draft domain.AgentDraft, publishedVersion int, at int64) error {
	raw, _ := json.Marshal(draft)
	_, err := t.tx.ExecContext(ctx, "UPDATE agent_definitions SET draft=?,published_version=?,revision=revision+1,updated_at=? WHERE id=?", string(raw), publishedVersion, at, agentID)
	return err
}

func (t *Tx) VersionSnapshot(ctx context.Context, agentID string, version int) (domain.AgentSnapshot, error) {
	var raw string
	err := t.tx.QueryRowContext(ctx, "SELECT snapshot FROM agent_versions WHERE agent_id=? AND version=?", agentID, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSnapshot{}, domain.NotFound("历史版本不存在")
	}
	if err != nil {
		return domain.AgentSnapshot{}, err
	}
	var snap domain.AgentSnapshot
	err = json.Unmarshal([]byte(raw), &snap)
	return snap, err
}

func (t *Tx) InsertAudit(ctx context.Context, actorID, action, targetID string, revision int, at int64) error {
	_, err := t.tx.ExecContext(ctx, "INSERT INTO agent_audit(actor_id,action,target_id,revision,created_at) VALUES(?,?,?,?,?)", actorID, action, targetID, revision, at)
	return err
}

func (t *Tx) CreateRun(ctx context.Context, userID, idem string, thread *domain.AgentThread, bindingAgent string, run domain.AgentRun, snapshot domain.AgentSnapshot) error {
	if thread != nil {
		if _, err := t.tx.ExecContext(ctx, "INSERT INTO agent_threads (id,user_id,title,created_at) VALUES(?,?,?,?)", thread.ID, userID, thread.Title, thread.CreatedAt); err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, "INSERT INTO agent_thread_bindings (thread_id,agent_id) VALUES(?,?)", thread.ID, bindingAgent); err != nil {
			return err
		}
	}
	if _, err := t.tx.ExecContext(ctx, "INSERT INTO agent_runs(id,thread_id,user_id,idem,prompt,status,created_at) VALUES(?,?,?,?,?,?,?)", run.ID, run.ThreadID, userID, idem, run.Prompt, run.Status, run.CreatedAt); err != nil {
		return err
	}
	raw, _ := json.Marshal(snapshot)
	if _, err := t.tx.ExecContext(ctx, "INSERT INTO agent_run_snapshots (run_id,agent_id,version,snapshot) VALUES(?,?,?,?)", run.ID, snapshot.AgentID, snapshot.Version, string(raw)); err != nil {
		return err
	}
	return nil
}
