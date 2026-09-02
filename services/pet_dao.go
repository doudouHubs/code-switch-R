package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const petDefaultPageSize = 20

// 宠物数据独立使用 payload JSON + 查询索引列：JSON 保持跨版本可扩展，索引列保证分页和
// 时间排序不需要把整张表读进内存。每张表都以业务主键做 upsert，迁移重跑不会制造副本。
const petSchemaSQL = `
CREATE TABLE IF NOT EXISTS pet_state (
    pet_id TEXT PRIMARY KEY,
    state_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_experience (
    pet_id TEXT PRIMARY KEY,
    experience_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_exp_log (
    pet_id TEXT NOT NULL,
    id TEXT NOT NULL,
    at INTEGER NOT NULL,
    model TEXT NOT NULL,
    tokens INTEGER NOT NULL,
    premium INTEGER NOT NULL,
    exp REAL NOT NULL,
    entry_json TEXT NOT NULL,
    PRIMARY KEY (pet_id, id)
);

CREATE INDEX IF NOT EXISTS idx_pet_exp_log_page
    ON pet_exp_log(pet_id, at DESC, id DESC);

CREATE TABLE IF NOT EXISTS pet_care (
    pet_id TEXT PRIMARY KEY,
    config_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_agent (
    pet_id TEXT PRIMARY KEY,
    config_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_dream_config (
    pet_id TEXT PRIMARY KEY,
    config_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_window (
    pet_id TEXT PRIMARY KEY,
    config_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_heartbeat (
    pet_id TEXT PRIMARY KEY,
    config_json TEXT NOT NULL,
    runtime_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_plans (
    pet_id TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    title TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    plan_json TEXT NOT NULL,
    PRIMARY KEY (pet_id, plan_id)
);

CREATE INDEX IF NOT EXISTS idx_pet_plans_updated_at
    ON pet_plans(pet_id, updated_at DESC, plan_id DESC);

CREATE TABLE IF NOT EXISTS pet_skins (
    pet_id TEXT NOT NULL,
    skin_id TEXT NOT NULL,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    atlas_path TEXT NOT NULL,
    builtin INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    skin_json TEXT NOT NULL,
    PRIMARY KEY (pet_id, skin_id)
);

CREATE INDEX IF NOT EXISTS idx_pet_skins_updated_at
    ON pet_skins(pet_id, updated_at DESC, skin_id ASC);

CREATE TABLE IF NOT EXISTS pet_skin_selection (
    pet_id TEXT PRIMARY KEY,
    active_skin_id TEXT,
    selection_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_dream_history (
    pet_id TEXT NOT NULL,
    id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    record_json TEXT NOT NULL,
    PRIMARY KEY (pet_id, id)
);

CREATE INDEX IF NOT EXISTS idx_pet_dream_history_created_at
    ON pet_dream_history(pet_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS pet_memory (
    pet_id TEXT NOT NULL,
    id TEXT NOT NULL,
    date TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    record_json TEXT NOT NULL,
    PRIMARY KEY (pet_id, id)
);

CREATE INDEX IF NOT EXISTS idx_pet_memory_updated_at
    ON pet_memory(pet_id, updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS pet_codex_session (
    pet_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    workspace TEXT NOT NULL,
    persona_fingerprint TEXT NOT NULL,
    tool_fingerprint TEXT NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_codex_session (
    project_id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    workspace TEXT NOT NULL,
    persona_fingerprint TEXT NOT NULL,
    tool_fingerprint TEXT NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_migration_markers (
    migration_key TEXT PRIMARY KEY,
    version INTEGER NOT NULL,
    status TEXT NOT NULL,
    source_fingerprint TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    completed_at INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    marker_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pet_migration_diagnostics (
    diagnostic_id TEXT PRIMARY KEY,
    migration_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    source TEXT NOT NULL,
    diagnostic_key TEXT NOT NULL DEFAULT '',
    record_id TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    diagnostic_json TEXT NOT NULL,
    UNIQUE (migration_key, kind, source, diagnostic_key, record_id, message)
);

CREATE INDEX IF NOT EXISTS idx_pet_migration_diagnostics_key
    ON pet_migration_diagnostics(migration_key, diagnostic_id);
`

// EnsurePetSchema 是测试和独立服务构造时的显式 schema 入口；应用启动则由 InitDatabase 调用。
func EnsurePetSchema(db *sql.DB) error {
	return ensurePetTablesWithDB(db)
}

func ensurePetTablesWithDB(db *sql.DB) error {
	if db == nil {
		return errors.New("pet schema requires a non-nil database")
	}
	if _, err := db.Exec(petSchemaSQL); err != nil {
		return fmt.Errorf("create pet schema: %w", err)
	}
	// 旧版数据库的 CREATE TABLE IF NOT EXISTS 不会补列；工具权限指纹是
	// thread 复用条件的一部分，缺列时必须显式迁移而不是静默丢失隔离信息。
	if err := ensurePetColumnWithDB(db, "pet_codex_session", "tool_fingerprint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func ensurePetColumnWithDB(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect pet schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan pet schema: %w", err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read pet schema: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil {
		return fmt.Errorf("migrate pet schema: %w", err)
	}
	return nil
}

// PetAgentPersona 是聊天启动阶段需要的最小人格投影；它刻意不携带 provider、
// 历史记录或其它设置页字段，避免无关数据成为 Codex 启动依赖。
type PetAgentPersona struct {
	SystemPrompt string
	PetName      string
}

// PetDAO 只负责宠物持久化，不承载动作规则。多表一致性由调用方传入的快照事务保证。
type PetDAO struct {
	db *sql.DB

	schemaOnce sync.Once
	schemaErr  error
}

func NewPetDAO(db *sql.DB) *PetDAO {
	return &PetDAO{db: db}
}

func (d *PetDAO) ensureSchema() error {
	if d == nil || d.db == nil {
		return errors.New("pet dao requires a non-nil database")
	}
	d.schemaOnce.Do(func() {
		d.schemaErr = ensurePetTablesWithDB(d.db)
	})
	return d.schemaErr
}

// LoadAgentPersona 只读取 pet_state 和 pet_agent，供共享 Agent Hub 解析 canonical
// persona。设置页的完整快照包含历史、皮肤等较重表，不能把它们放进每条消息的
// 启动前置链路；缺少任一记录时返回空字段，由 BuildPetAgentPersona 补默认值。
func (d *PetDAO) LoadAgentPersona(ctx context.Context, petID string) (PetAgentPersona, error) {
	var persona PetAgentPersona
	if err := d.ensureSchema(); err != nil {
		return persona, err
	}
	petID = normalizePetID(petID)
	ctx = petContext(ctx)
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return persona, fmt.Errorf("begin pet persona read: %w", err)
	}
	defer rollbackPetTx(tx)

	var state PetState
	if found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT state_json FROM pet_state WHERE pet_id = ?`, petID), &state); err != nil {
		return persona, fmt.Errorf("load pet persona state: %w", err)
	} else if found {
		persona.PetName = strings.TrimSpace(state.Name)
	}

	var agent PetAgentConfig
	if found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT config_json FROM pet_agent WHERE pet_id = ?`, petID), &agent); err != nil {
		return persona, fmt.Errorf("load pet persona agent: %w", err)
	} else if found {
		persona.SystemPrompt = strings.TrimSpace(agent.SystemPrompt)
	}
	return persona, nil
}

func petContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func normalizePetID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return DefaultPetID
	}
	return id
}

func currentPetTimestamp() int64 {
	return time.Now().UnixMilli()
}

func marshalPetJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal pet json: %w", err)
	}
	return string(raw), nil
}

type petScanner interface {
	Scan(dest ...any) error
}

func scanPetJSON(scanner petScanner, target any) (bool, error) {
	var payload string
	if err := scanner.Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal([]byte(payload), target); err != nil {
		return false, fmt.Errorf("decode pet json: %w", err)
	}
	return true, nil
}

func scanPetRowJSON(rows *sql.Rows, target any) error {
	var payload string
	if err := rows.Scan(&payload); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(payload), target); err != nil {
		return fmt.Errorf("decode pet json row: %w", err)
	}
	return nil
}

func beginPetTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(petContext(ctx), nil)
	if err != nil {
		return nil, fmt.Errorf("begin pet transaction: %w", err)
	}
	return tx, nil
}

func commitPetTx(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pet transaction: %w", err)
	}
	return nil
}

func rollbackPetTx(tx *sql.Tx) {
	_ = tx.Rollback()
}

func snapshotPetID(snapshot PetMigrationSnapshot) string {
	if id := normalizePetID(snapshot.PetID); id != DefaultPetID || strings.TrimSpace(snapshot.PetID) != "" {
		return id
	}
	for _, candidate := range []string{
		petStateID(snapshot.State),
		petExperienceID(snapshot.Experience),
		petCareID(snapshot.Care),
		petAgentID(snapshot.Agent),
		petDreamConfigID(snapshot.DreamConfig),
		petWindowID(snapshot.Window),
		petSkinSelectionID(snapshot.SkinSelection),
	} {
		if strings.TrimSpace(candidate) != "" {
			return normalizePetID(candidate)
		}
	}
	return DefaultPetID
}

func petStateID(value *PetState) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func petExperienceID(value *PetExperience) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func petCareID(value *PetCareConfig) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func petAgentID(value *PetAgentConfig) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func petDreamConfigID(value *PetDreamConfig) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func petWindowID(value *PetWindowConfig) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func petSkinSelectionID(value *PetSkinSelection) string {
	if value == nil {
		return ""
	}
	return value.PetID
}

func setSnapshotPetIDs(snapshot *PetMigrationSnapshot, petID string) {
	snapshot.PetID = petID
	if snapshot.State != nil {
		snapshot.State.PetID = petID
	}
	if snapshot.Experience != nil {
		snapshot.Experience.PetID = petID
	}
	if snapshot.Care != nil {
		snapshot.Care.PetID = petID
	}
	if snapshot.Agent != nil {
		snapshot.Agent.PetID = petID
	}
	if snapshot.DreamConfig != nil {
		snapshot.DreamConfig.PetID = petID
	}
	if snapshot.Window != nil {
		snapshot.Window.PetID = petID
	}
	if snapshot.SkinSelection != nil {
		snapshot.SkinSelection.PetID = petID
	}
	for index := range snapshot.ExpLog {
		snapshot.ExpLog[index].PetID = petID
	}
	for index := range snapshot.PlanRecords {
		snapshot.PlanRecords[index].PetID = petID
	}
	for index := range snapshot.Skins {
		snapshot.Skins[index].PetID = petID
	}
	for index := range snapshot.Dreams {
		snapshot.Dreams[index].PetID = petID
	}
	for index := range snapshot.Memories {
		snapshot.Memories[index].PetID = petID
	}
}

// SaveSnapshot 在一个事务内保存所有非空切片；空切片不会触发删除，避免“部分快照”误删已有业务数据。
func (d *PetDAO) SaveSnapshot(ctx context.Context, snapshot PetMigrationSnapshot) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	petID := snapshotPetID(snapshot)
	setSnapshotPetIDs(&snapshot, petID)

	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := d.saveSnapshotTx(petContext(ctx), tx, snapshot); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) saveSnapshotTx(ctx context.Context, tx *sql.Tx, snapshot PetMigrationSnapshot) error {
	updatedAt := currentPetTimestamp()
	if snapshot.State != nil {
		payload, err := marshalPetJSON(*snapshot.State)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pet_state (pet_id, state_json, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(pet_id) DO UPDATE SET state_json = excluded.state_json, updated_at = excluded.updated_at
		`, snapshot.PetID, payload, updatedAt); err != nil {
			return fmt.Errorf("upsert pet state: %w", err)
		}
	}
	if snapshot.Experience != nil {
		payload, err := marshalPetJSON(*snapshot.Experience)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pet_experience (pet_id, experience_json, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(pet_id) DO UPDATE SET experience_json = excluded.experience_json, updated_at = excluded.updated_at
		`, snapshot.PetID, payload, updatedAt); err != nil {
			return fmt.Errorf("upsert pet experience: %w", err)
		}
	}
	for _, entry := range snapshot.ExpLog {
		if err := upsertPetExpLog(ctx, tx, snapshot.PetID, entry); err != nil {
			return err
		}
	}
	if snapshot.Care != nil {
		if err := upsertPetJSONConfig(ctx, tx, "pet_care", snapshot.PetID, *snapshot.Care, updatedAt); err != nil {
			return err
		}
	}
	if snapshot.Agent != nil {
		if err := upsertPetJSONConfig(ctx, tx, "pet_agent", snapshot.PetID, *snapshot.Agent, updatedAt); err != nil {
			return err
		}
	}
	if snapshot.DreamConfig != nil {
		if err := upsertPetJSONConfig(ctx, tx, "pet_dream_config", snapshot.PetID, *snapshot.DreamConfig, updatedAt); err != nil {
			return err
		}
	}
	if snapshot.Window != nil {
		if err := upsertPetJSONConfig(ctx, tx, "pet_window", snapshot.PetID, *snapshot.Window, updatedAt); err != nil {
			return err
		}
	}
	for _, record := range snapshot.PlanRecords {
		if err := upsertPetPlan(ctx, tx, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Skins {
		if err := upsertPetSkin(ctx, tx, record); err != nil {
			return err
		}
	}
	if snapshot.SkinSelection != nil {
		if err := upsertPetSkinSelection(ctx, tx, *snapshot.SkinSelection, updatedAt); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Dreams {
		if err := upsertPetDreamHistory(ctx, tx, record); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Memories {
		if err := upsertPetMemory(ctx, tx, record); err != nil {
			return err
		}
	}
	return nil
}

func upsertPetJSONConfig(ctx context.Context, tx *sql.Tx, table, petID string, value any, updatedAt int64) error {
	payload, err := marshalPetJSON(value)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (pet_id, config_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(pet_id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at
	`, table)
	if _, err := tx.ExecContext(ctx, query, petID, payload, updatedAt); err != nil {
		return fmt.Errorf("upsert %s: %w", table, err)
	}
	return nil
}

func upsertPetExpLog(ctx context.Context, tx *sql.Tx, petID string, entry PetExpLogEntry) error {
	entry.PetID = petID
	if strings.TrimSpace(entry.ID) == "" {
		return errors.New("pet experience log id is required")
	}
	payload, err := marshalPetJSON(entry)
	if err != nil {
		return err
	}
	premium := 0
	if entry.Premium {
		premium = 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pet_exp_log (pet_id, id, at, model, tokens, premium, exp, entry_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pet_id, id) DO UPDATE SET
		  at = excluded.at,
		  model = excluded.model,
		  tokens = excluded.tokens,
		  premium = excluded.premium,
		  exp = excluded.exp,
		  entry_json = excluded.entry_json
	`, petID, entry.ID, entry.At, entry.Model, entry.Tokens, premium, entry.Exp, payload); err != nil {
		return fmt.Errorf("upsert pet experience log: %w", err)
	}
	return nil
}

func upsertPetPlan(ctx context.Context, tx *sql.Tx, record PetPlanRecord) error {
	record.PetID = normalizePetID(record.PetID)
	if strings.TrimSpace(record.PlanID) == "" {
		return errors.New("pet plan id is required")
	}
	payload, err := marshalPetJSON(record)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pet_plans (pet_id, plan_id, version, title, created_at, updated_at, plan_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pet_id, plan_id) DO UPDATE SET
		  version = excluded.version,
		  title = excluded.title,
		  created_at = excluded.created_at,
		  updated_at = excluded.updated_at,
		  plan_json = excluded.plan_json
	`, record.PetID, record.PlanID, record.Version, record.Title, record.CreatedAt, record.UpdatedAt, payload); err != nil {
		return fmt.Errorf("upsert pet plan: %w", err)
	}
	return nil
}

func sanitizePetSkinManifest(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("null"), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode pet skin manifest: %w", err)
	}
	redactPetAPIKeys(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode pet skin manifest: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func redactPetAPIKeys(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key := range current {
			compact := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			if compact == "apikey" {
				delete(current, key)
				continue
			}
			redactPetAPIKeys(current[key])
		}
	case []any:
		for _, item := range current {
			redactPetAPIKeys(item)
		}
	}
}

func upsertPetSkin(ctx context.Context, tx *sql.Tx, record PetSkinRecord) error {
	record.PetID = normalizePetID(record.PetID)
	if strings.TrimSpace(record.SkinID) == "" {
		return errors.New("pet skin id is required")
	}
	manifest, err := sanitizePetSkinManifest(record.ManifestJSON)
	if err != nil {
		return err
	}
	record.ManifestJSON = manifest
	payload, err := marshalPetJSON(record)
	if err != nil {
		return err
	}
	updatedAt := currentPetTimestamp()
	if record.UpdatedAt != nil {
		updatedAt = *record.UpdatedAt
	} else if record.CreatedAt != nil {
		updatedAt = *record.CreatedAt
	}
	builtin := 0
	if record.Builtin {
		builtin = 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pet_skins (pet_id, skin_id, name, path, atlas_path, builtin, updated_at, skin_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pet_id, skin_id) DO UPDATE SET
		  name = excluded.name,
		  path = excluded.path,
		  atlas_path = excluded.atlas_path,
		  builtin = excluded.builtin,
		  updated_at = excluded.updated_at,
		  skin_json = excluded.skin_json
	`, record.PetID, record.SkinID, record.Name, record.Path, record.AtlasPath, builtin, updatedAt, payload); err != nil {
		return fmt.Errorf("upsert pet skin: %w", err)
	}
	return nil
}

func upsertPetSkinSelection(ctx context.Context, tx *sql.Tx, selection PetSkinSelection, updatedAt int64) error {
	selection.PetID = normalizePetID(selection.PetID)
	payload, err := marshalPetJSON(selection)
	if err != nil {
		return err
	}
	var activeSkinID any
	if selection.ActiveSkinID != nil {
		activeSkinID = *selection.ActiveSkinID
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pet_skin_selection (pet_id, active_skin_id, selection_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(pet_id) DO UPDATE SET
		  active_skin_id = excluded.active_skin_id,
		  selection_json = excluded.selection_json,
		  updated_at = excluded.updated_at
	`, selection.PetID, activeSkinID, payload, updatedAt); err != nil {
		return fmt.Errorf("upsert pet skin selection: %w", err)
	}
	return nil
}

func upsertPetDreamHistory(ctx context.Context, tx *sql.Tx, record PetDreamHistoryRecord) error {
	record.PetID = normalizePetID(record.PetID)
	if strings.TrimSpace(record.ID) == "" {
		return errors.New("pet dream history id is required")
	}
	payload, err := marshalPetJSON(record)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pet_dream_history (pet_id, id, created_at, record_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(pet_id, id) DO UPDATE SET
		  created_at = excluded.created_at,
		  record_json = excluded.record_json
	`, record.PetID, record.ID, record.CreatedAt, payload); err != nil {
		return fmt.Errorf("upsert pet dream history: %w", err)
	}
	return nil
}

func upsertPetMemory(ctx context.Context, tx *sql.Tx, record PetMemoryRecord) error {
	record.PetID = normalizePetID(record.PetID)
	if strings.TrimSpace(record.ID) == "" {
		return errors.New("pet memory id is required")
	}
	payload, err := marshalPetJSON(record)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO pet_memory (pet_id, id, date, created_at, updated_at, record_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(pet_id, id) DO UPDATE SET
		  date = excluded.date,
		  created_at = excluded.created_at,
		  updated_at = excluded.updated_at,
		  record_json = excluded.record_json
	`, record.PetID, record.ID, record.Date, record.CreatedAt, record.UpdatedAt, payload); err != nil {
		return fmt.Errorf("upsert pet memory: %w", err)
	}
	return nil
}

// AppendExpLog 使用 (petID, id) 作为幂等键；重复收到同一 usage 事件只更新原记录，不累加副本。
func (d *PetDAO) AppendExpLog(ctx context.Context, entry PetExpLogEntry) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	petID := normalizePetID(entry.PetID)
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := upsertPetExpLog(petContext(ctx), tx, petID, entry); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) ListExpLog(ctx context.Context, petID string, page, pageSize int) (PetExpLogPage, error) {
	var result PetExpLogPage
	if err := d.ensureSchema(); err != nil {
		return result, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = petDefaultPageSize
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	petID = normalizePetID(petID)
	ctx = petContext(ctx)

	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pet_exp_log WHERE pet_id = ?`, petID).Scan(&total); err != nil {
		return result, fmt.Errorf("count pet experience log: %w", err)
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	offset := (page - 1) * pageSize
	rows, err := d.db.QueryContext(ctx, `
		SELECT entry_json
		  FROM pet_exp_log
		 WHERE pet_id = ?
		 ORDER BY at DESC, id DESC
		 LIMIT ? OFFSET ?
	`, petID, pageSize, offset)
	if err != nil {
		return result, fmt.Errorf("list pet experience log: %w", err)
	}
	defer rows.Close()
	entries := make([]PetExpLogEntry, 0)
	for rows.Next() {
		var entry PetExpLogEntry
		if err := scanPetRowJSON(rows, &entry); err != nil {
			return result, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate pet experience log: %w", err)
	}
	result = PetExpLogPage{
		Entries:     entries,
		Page:        page,
		PageSize:    pageSize,
		Total:       total,
		TotalPages:  totalPages,
		HasNext:     page < totalPages,
		HasPrevious: page > 1 && totalPages > 0,
	}
	return result, nil
}

// LoadSnapshot 在一个只读事务中读取所有宠物表，避免界面同时看到不同时间点的配置组合。
func (d *PetDAO) LoadSnapshot(ctx context.Context, petID string) (PetMigrationSnapshot, error) {
	var snapshot PetMigrationSnapshot
	if err := d.ensureSchema(); err != nil {
		return snapshot, err
	}
	snapshot.PetID = normalizePetID(petID)
	ctx = petContext(ctx)
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, fmt.Errorf("begin pet snapshot read: %w", err)
	}
	defer rollbackPetTx(tx)

	if found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT state_json FROM pet_state WHERE pet_id = ?`, snapshot.PetID), &snapshot.State); err != nil {
		return snapshot, fmt.Errorf("load pet state: %w", err)
	} else if found {
		snapshot.State.PetID = snapshot.PetID
	}
	if found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT experience_json FROM pet_experience WHERE pet_id = ?`, snapshot.PetID), &snapshot.Experience); err != nil {
		return snapshot, fmt.Errorf("load pet experience: %w", err)
	} else if found {
		snapshot.Experience.PetID = snapshot.PetID
	}
	if snapshot.ExpLog, err = loadPetExpLogTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Care, err = loadPetJSONConfigTx[PetCareConfig](ctx, tx, "pet_care", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Agent, err = loadPetJSONConfigTx[PetAgentConfig](ctx, tx, "pet_agent", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.DreamConfig, err = loadPetJSONConfigTx[PetDreamConfig](ctx, tx, "pet_dream_config", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Window, err = loadPetJSONConfigTx[PetWindowConfig](ctx, tx, "pet_window", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.PlanRecords, err = loadPetPlansTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Skins, err = loadPetSkinsTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.SkinSelection, err = loadPetSkinSelectionTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Dreams, err = loadPetDreamHistoryTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Memories, err = loadPetMemoriesTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// LoadSettingsSnapshot 只读取设置页首屏需要的表。
// 历史记录和经验日志由对应页签按需分页读取，避免每次打开设置都解析整批 JSON。
func (d *PetDAO) LoadSettingsSnapshot(ctx context.Context, petID string) (PetMigrationSnapshot, error) {
	var snapshot PetMigrationSnapshot
	if err := d.ensureSchema(); err != nil {
		return snapshot, err
	}
	snapshot.PetID = normalizePetID(petID)
	ctx = petContext(ctx)
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, fmt.Errorf("begin pet settings snapshot read: %w", err)
	}
	defer rollbackPetTx(tx)

	if found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT state_json FROM pet_state WHERE pet_id = ?`, snapshot.PetID), &snapshot.State); err != nil {
		return snapshot, fmt.Errorf("load pet state: %w", err)
	} else if found {
		snapshot.State.PetID = snapshot.PetID
	}
	if found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT experience_json FROM pet_experience WHERE pet_id = ?`, snapshot.PetID), &snapshot.Experience); err != nil {
		return snapshot, fmt.Errorf("load pet experience: %w", err)
	} else if found {
		snapshot.Experience.PetID = snapshot.PetID
	}
	if snapshot.Care, err = loadPetJSONConfigTx[PetCareConfig](ctx, tx, "pet_care", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Agent, err = loadPetJSONConfigTx[PetAgentConfig](ctx, tx, "pet_agent", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.DreamConfig, err = loadPetJSONConfigTx[PetDreamConfig](ctx, tx, "pet_dream_config", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Window, err = loadPetJSONConfigTx[PetWindowConfig](ctx, tx, "pet_window", snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.Skins, err = loadPetSkinsTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	if snapshot.SkinSelection, err = loadPetSkinSelectionTx(ctx, tx, snapshot.PetID); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func loadPetExpLogTx(ctx context.Context, tx *sql.Tx, petID string) ([]PetExpLogEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT entry_json FROM pet_exp_log WHERE pet_id = ? ORDER BY at DESC, id DESC
	`, petID)
	if err != nil {
		return nil, fmt.Errorf("load pet experience log: %w", err)
	}
	defer rows.Close()
	entries := make([]PetExpLogEntry, 0)
	for rows.Next() {
		var entry PetExpLogEntry
		if err := scanPetRowJSON(rows, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pet experience log: %w", err)
	}
	return entries, nil
}

func loadPetJSONConfigTx[T any](ctx context.Context, tx *sql.Tx, table, petID string) (*T, error) {
	var value T
	found, err := scanPetJSON(tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT config_json FROM %s WHERE pet_id = ?`, table), petID), &value)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", table, err)
	}
	if !found {
		return nil, nil
	}
	return &value, nil
}

func loadPetPlansTx(ctx context.Context, tx *sql.Tx, petID string) ([]PetPlanRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT plan_json FROM pet_plans WHERE pet_id = ? ORDER BY updated_at DESC, plan_id DESC
	`, petID)
	if err != nil {
		return nil, fmt.Errorf("load pet plans: %w", err)
	}
	defer rows.Close()
	result := make([]PetPlanRecord, 0)
	for rows.Next() {
		var record PetPlanRecord
		if err := scanPetRowJSON(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pet plans: %w", err)
	}
	return result, nil
}

func loadPetSkinsTx(ctx context.Context, tx *sql.Tx, petID string) ([]PetSkinRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT skin_json FROM pet_skins WHERE pet_id = ? ORDER BY updated_at DESC, skin_id ASC
	`, petID)
	if err != nil {
		return nil, fmt.Errorf("load pet skins: %w", err)
	}
	defer rows.Close()
	result := make([]PetSkinRecord, 0)
	for rows.Next() {
		var record PetSkinRecord
		if err := scanPetRowJSON(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pet skins: %w", err)
	}
	return result, nil
}

func loadPetSkinSelectionTx(ctx context.Context, tx *sql.Tx, petID string) (*PetSkinSelection, error) {
	var selection PetSkinSelection
	found, err := scanPetJSON(tx.QueryRowContext(ctx, `SELECT selection_json FROM pet_skin_selection WHERE pet_id = ?`, petID), &selection)
	if err != nil {
		return nil, fmt.Errorf("load pet skin selection: %w", err)
	}
	if !found {
		return nil, nil
	}
	return &selection, nil
}

func loadPetDreamHistoryTx(ctx context.Context, tx *sql.Tx, petID string) ([]PetDreamHistoryRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT record_json FROM pet_dream_history WHERE pet_id = ? ORDER BY created_at DESC, id DESC
	`, petID)
	if err != nil {
		return nil, fmt.Errorf("load pet dream history: %w", err)
	}
	defer rows.Close()
	result := make([]PetDreamHistoryRecord, 0)
	for rows.Next() {
		var record PetDreamHistoryRecord
		if err := scanPetRowJSON(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pet dream history: %w", err)
	}
	return result, nil
}

func loadPetMemoriesTx(ctx context.Context, tx *sql.Tx, petID string) ([]PetMemoryRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT record_json FROM pet_memory WHERE pet_id = ? ORDER BY updated_at DESC, id DESC
	`, petID)
	if err != nil {
		return nil, fmt.Errorf("load pet memories: %w", err)
	}
	defer rows.Close()
	result := make([]PetMemoryRecord, 0)
	for rows.Next() {
		var record PetMemoryRecord
		if err := scanPetRowJSON(rows, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pet memories: %w", err)
	}
	return result, nil
}

// SaveState 等单表方法给 PetService 提供低耦合依赖；它们仍复用同一套 schema 和 upsert 规则。
func (d *PetDAO) SaveState(ctx context.Context, state PetState) error {
	return d.SaveSnapshot(ctx, PetMigrationSnapshot{PetID: state.PetID, State: &state})
}

func (d *PetDAO) LoadState(ctx context.Context, petID string) (*PetState, error) {
	snapshot, err := d.LoadSnapshot(ctx, petID)
	return snapshot.State, err
}

func (d *PetDAO) SaveExperience(ctx context.Context, experience PetExperience) error {
	return d.SaveSnapshot(ctx, PetMigrationSnapshot{PetID: experience.PetID, Experience: &experience})
}

func (d *PetDAO) LoadExperience(ctx context.Context, petID string) (*PetExperience, error) {
	snapshot, err := d.LoadSnapshot(ctx, petID)
	return snapshot.Experience, err
}

func (d *PetDAO) SaveCare(ctx context.Context, config PetCareConfig) error {
	return d.SaveSnapshot(ctx, PetMigrationSnapshot{PetID: config.PetID, Care: &config})
}

func (d *PetDAO) LoadCare(ctx context.Context, petID string) (*PetCareConfig, error) {
	snapshot, err := d.LoadSnapshot(ctx, petID)
	return snapshot.Care, err
}

func (d *PetDAO) SaveAgent(ctx context.Context, config PetAgentConfig) error {
	return d.SaveSnapshot(ctx, PetMigrationSnapshot{PetID: config.PetID, Agent: &config})
}

func (d *PetDAO) LoadAgent(ctx context.Context, petID string) (*PetAgentConfig, error) {
	snapshot, err := d.LoadSnapshot(ctx, petID)
	return snapshot.Agent, err
}

// LoadAgentModelReference 只查询 pet_agent 的模型、平台和 reasoning 字段，不加载完整宠物快照。
// 每次调用都从数据库取最新值，保证模型设置保存后不会被 runtime 内缓存遮住；
// provider 凭据、权限和其它设置不进入这个投影，避免形成第二份 Codex 配置源。
func (d *PetDAO) LoadAgentModelReference(ctx context.Context, petID string) (PetAgentModelReference, error) {
	var reference PetAgentModelReference
	if err := d.ensureSchema(); err != nil {
		return reference, err
	}

	petID = normalizePetID(petID)
	var payload string
	err := d.db.QueryRowContext(
		petContext(ctx),
		`SELECT config_json FROM pet_agent WHERE pet_id = ?`,
		petID,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return reference, nil
	}
	if err != nil {
		return reference, fmt.Errorf("load pet agent model reference: %w", err)
	}

	// 这里故意使用局部投影而不是 PetAgentConfig；provider ID 和其它字段即使
	// 后续扩展，也不能意外穿透到 Codex runtime 的请求参数。
	var config struct {
		ProviderPlatform string `json:"providerPlatform"`
		ModelID          string `json:"modelId"`
		ReasoningEffort  string `json:"reasoningEffort"`
	}
	if err := json.Unmarshal([]byte(payload), &config); err != nil {
		return reference, fmt.Errorf("decode pet agent model reference: %w", err)
	}

	reference.ProviderPlatform = strings.ToLower(strings.TrimSpace(config.ProviderPlatform))
	reference.ModelID = strings.TrimSpace(config.ModelID)
	reference.ReasoningEffort = PetReasoningEffort(strings.ToLower(strings.TrimSpace(config.ReasoningEffort)))
	return reference, nil
}

// Resolve 保留旧版仅按 projectFolder 读取的兼容入口；主聊天使用
// NewPetProjectWorkspaceResolver 从 ProjectManagerService 校验 projectId 后再取路径。
// 该方法仍只读取持久化数据，不接受调用方传入的路径。
func (d *PetDAO) Resolve(ctx context.Context, petID string) (string, error) {
	agent, err := d.LoadAgent(ctx, petID)
	if err != nil {
		return "", err
	}
	if agent == nil || agent.ProjectFolder == nil {
		return "", nil
	}
	return strings.TrimSpace(*agent.ProjectFolder), nil
}

func (d *PetDAO) SaveDreamConfig(ctx context.Context, config PetDreamConfig) error {
	return d.SaveSnapshot(ctx, PetMigrationSnapshot{PetID: config.PetID, DreamConfig: &config})
}

func (d *PetDAO) LoadDreamConfig(ctx context.Context, petID string) (*PetDreamConfig, error) {
	snapshot, err := d.LoadSnapshot(ctx, petID)
	return snapshot.DreamConfig, err
}

func (d *PetDAO) SaveWindow(ctx context.Context, config PetWindowConfig) error {
	return d.SaveSnapshot(ctx, PetMigrationSnapshot{PetID: config.PetID, Window: &config})
}

func (d *PetDAO) LoadWindow(ctx context.Context, petID string) (*PetWindowConfig, error) {
	snapshot, err := d.LoadSnapshot(ctx, petID)
	return snapshot.Window, err
}

// LoadHeartbeat 只读取心跳自己的配置和运行态，不把它混进完整宠物快照。
// 首次读取返回默认关闭配置，让页面可以直接编辑；真正的 schema 初始化仍由 DAO
// 统一负责，避免浏览器 bridge 和 Wails 入口各自维护一套建表逻辑。
func (d *PetDAO) LoadHeartbeat(ctx context.Context, petID string) (PetHeartbeatSnapshot, error) {
	if err := d.ensureSchema(); err != nil {
		return PetHeartbeatSnapshot{}, err
	}
	petID = normalizePetID(petID)
	var configPayload, runtimePayload string
	err := d.db.QueryRowContext(
		petContext(ctx),
		`SELECT config_json, runtime_json FROM pet_heartbeat WHERE pet_id = ?`,
		petID,
	).Scan(&configPayload, &runtimePayload)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultPetHeartbeatSnapshot(petID), nil
	}
	if err != nil {
		return PetHeartbeatSnapshot{}, fmt.Errorf("load pet heartbeat: %w", err)
	}

	var snapshot PetHeartbeatSnapshot
	if err := json.Unmarshal([]byte(configPayload), &snapshot.Config); err != nil {
		return PetHeartbeatSnapshot{}, fmt.Errorf("decode pet heartbeat config: %w", err)
	}
	if err := json.Unmarshal([]byte(runtimePayload), &snapshot.Runtime); err != nil {
		return PetHeartbeatSnapshot{}, fmt.Errorf("decode pet heartbeat runtime: %w", err)
	}
	snapshot.Config.PetID = petID
	return snapshot, nil
}

// SaveHeartbeat 与完整宠物快照分开提交，避免保存心跳状态时覆盖 PetService
// 刚刚推进的 hunger、away task 或 Agent 配置。这里只修正持久化分区 ID，不能调用
// normalizePetHeartbeatSnapshot：关闭配置下仍可能存在一个正在收尾的 running 任务。
func (d *PetDAO) SaveHeartbeat(ctx context.Context, snapshot PetHeartbeatSnapshot) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	petID := normalizePetID(snapshot.Config.PetID)
	snapshot.Config.PetID = petID
	configPayload, err := marshalPetJSON(snapshot.Config)
	if err != nil {
		return err
	}
	runtimePayload, err := marshalPetJSON(snapshot.Runtime)
	if err != nil {
		return err
	}

	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if _, err := tx.ExecContext(petContext(ctx), `
		INSERT INTO pet_heartbeat (pet_id, config_json, runtime_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(pet_id) DO UPDATE SET
		  config_json = excluded.config_json,
		  runtime_json = excluded.runtime_json,
		  updated_at = excluded.updated_at
	`, petID, configPayload, runtimePayload, currentPetTimestamp()); err != nil {
		return fmt.Errorf("upsert pet heartbeat: %w", err)
	}
	return commitPetTx(tx)
}

func (d *PetDAO) UpsertPlan(ctx context.Context, record PetPlanRecord) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := upsertPetPlan(petContext(ctx), tx, record); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) DeletePlan(ctx context.Context, petID, planID string) error {
	return d.deletePetRecord(ctx, `DELETE FROM pet_plans WHERE pet_id = ? AND plan_id = ?`, petID, planID, "delete pet plan")
}

func (d *PetDAO) ListPlans(ctx context.Context, petID string) ([]PetPlanRecord, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return nil, err
	}
	defer rollbackPetTx(tx)
	return loadPetPlansTx(petContext(ctx), tx, normalizePetID(petID))
}

func (d *PetDAO) UpsertSkin(ctx context.Context, record PetSkinRecord) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := upsertPetSkin(petContext(ctx), tx, record); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) DeleteSkin(ctx context.Context, petID, skinID string) error {
	return d.deletePetRecord(ctx, `DELETE FROM pet_skins WHERE pet_id = ? AND skin_id = ?`, petID, skinID, "delete pet skin")
}

func (d *PetDAO) ListSkins(ctx context.Context, petID string) ([]PetSkinRecord, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return nil, err
	}
	defer rollbackPetTx(tx)
	return loadPetSkinsTx(petContext(ctx), tx, normalizePetID(petID))
}

func (d *PetDAO) SaveSkinSelection(ctx context.Context, selection PetSkinSelection) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := upsertPetSkinSelection(petContext(ctx), tx, selection, currentPetTimestamp()); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) LoadSkinSelection(ctx context.Context, petID string) (*PetSkinSelection, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return nil, err
	}
	defer rollbackPetTx(tx)
	return loadPetSkinSelectionTx(petContext(ctx), tx, normalizePetID(petID))
}

func (d *PetDAO) UpsertDreamHistory(ctx context.Context, record PetDreamHistoryRecord) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := upsertPetDreamHistory(petContext(ctx), tx, record); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) DeleteDreamHistory(ctx context.Context, petID, id string) error {
	return d.deletePetRecord(ctx, `DELETE FROM pet_dream_history WHERE pet_id = ? AND id = ?`, petID, id, "delete pet dream history")
}

func (d *PetDAO) ListDreamHistory(ctx context.Context, petID string) ([]PetDreamHistoryRecord, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return nil, err
	}
	defer rollbackPetTx(tx)
	return loadPetDreamHistoryTx(petContext(ctx), tx, normalizePetID(petID))
}

func (d *PetDAO) UpsertMemory(ctx context.Context, record PetMemoryRecord) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := upsertPetMemory(petContext(ctx), tx, record); err != nil {
		return err
	}
	return commitPetTx(tx)
}

func (d *PetDAO) DeleteMemory(ctx context.Context, petID, id string) error {
	return d.deletePetRecord(ctx, `DELETE FROM pet_memory WHERE pet_id = ? AND id = ?`, petID, id, "delete pet memory")
}

func (d *PetDAO) ListMemories(ctx context.Context, petID string) ([]PetMemoryRecord, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return nil, err
	}
	defer rollbackPetTx(tx)
	return loadPetMemoriesTx(petContext(ctx), tx, normalizePetID(petID))
}

func (d *PetDAO) deletePetRecord(ctx context.Context, query string, petID, recordID, operation string) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	if strings.TrimSpace(recordID) == "" {
		return errors.New("pet record id is required")
	}
	ctx = petContext(ctx)
	if _, err := d.db.ExecContext(ctx, query, normalizePetID(petID), recordID); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func migrationDiagnosticID(migrationKey string, diagnostic PetMigrationDiagnostic) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%s\x00%s", migrationKey, diagnostic.Kind, diagnostic.Source, diagnostic.Key, diagnostic.RecordID, diagnostic.Message)
	return hex.EncodeToString(hash.Sum(nil))
}

// SaveMigrationMarker 是迁移状态的唯一写入口，marker_json 保留未来扩展字段，索引列便于运维查询。
func (d *PetDAO) SaveMigrationMarker(ctx context.Context, marker PetMigrationMarker) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	marker.MigrationKey = strings.TrimSpace(marker.MigrationKey)
	if marker.MigrationKey == "" {
		return errors.New("migration marker key is required")
	}
	if marker.StartedAt <= 0 {
		marker.StartedAt = currentPetTimestamp()
	}
	payload, err := marshalPetJSON(marker)
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(petContext(ctx), `
		INSERT INTO pet_migration_markers (
		  migration_key, version, status, source_fingerprint, started_at, completed_at, last_error, marker_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(migration_key) DO UPDATE SET
		  version = excluded.version,
		  status = excluded.status,
		  source_fingerprint = excluded.source_fingerprint,
		  started_at = excluded.started_at,
		  completed_at = excluded.completed_at,
		  last_error = excluded.last_error,
		  marker_json = excluded.marker_json
	`, marker.MigrationKey, marker.Version, marker.Status, marker.SourceFingerprint, marker.StartedAt, marker.CompletedAt, marker.LastError, payload); err != nil {
		return fmt.Errorf("save pet migration marker: %w", err)
	}
	return nil
}

func (d *PetDAO) LoadMigrationMarker(ctx context.Context, migrationKey string) (*PetMigrationMarker, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	var marker PetMigrationMarker
	found, err := scanPetJSON(d.db.QueryRowContext(petContext(ctx), `SELECT marker_json FROM pet_migration_markers WHERE migration_key = ?`, strings.TrimSpace(migrationKey)), &marker)
	if err != nil {
		return nil, fmt.Errorf("load pet migration marker: %w", err)
	}
	if !found {
		return nil, nil
	}
	return &marker, nil
}

func (d *PetDAO) GetMigrationMarker(ctx context.Context, migrationKey string) (*PetMigrationMarker, error) {
	return d.LoadMigrationMarker(ctx, migrationKey)
}

func (d *PetDAO) AppendMigrationDiagnostic(ctx context.Context, migrationKey string, diagnostic PetMigrationDiagnostic) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	return d.appendMigrationDiagnostic(ctx, nil, migrationKey, diagnostic)
}

func (d *PetDAO) appendMigrationDiagnostic(ctx context.Context, tx *sql.Tx, migrationKey string, diagnostic PetMigrationDiagnostic) error {
	migrationKey = strings.TrimSpace(migrationKey)
	if migrationKey == "" {
		return errors.New("migration diagnostic key is required")
	}
	payload, err := marshalPetJSON(diagnostic)
	if err != nil {
		return err
	}
	diagnosticID := migrationDiagnosticID(migrationKey, diagnostic)
	args := []any{
		diagnosticID,
		migrationKey,
		string(diagnostic.Kind),
		diagnostic.Source,
		diagnostic.Key,
		diagnostic.RecordID,
		diagnostic.Message,
		payload,
	}
	query := `
		INSERT INTO pet_migration_diagnostics (
		  diagnostic_id, migration_key, kind, source, diagnostic_key, record_id, message, diagnostic_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(diagnostic_id) DO UPDATE SET
		  kind = excluded.kind,
		  source = excluded.source,
		  diagnostic_key = excluded.diagnostic_key,
		  record_id = excluded.record_id,
		  message = excluded.message,
		  diagnostic_json = excluded.diagnostic_json
	`
	if tx != nil {
		if _, err := tx.ExecContext(petContext(ctx), query, args...); err != nil {
			return fmt.Errorf("save pet migration diagnostic: %w", err)
		}
		return nil
	}
	if _, err := d.db.ExecContext(petContext(ctx), query, args...); err != nil {
		return fmt.Errorf("save pet migration diagnostic: %w", err)
	}
	return nil
}

func (d *PetDAO) ReplaceMigrationDiagnostics(ctx context.Context, migrationKey string, diagnostics []PetMigrationDiagnostic) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	migrationKey = strings.TrimSpace(migrationKey)
	if migrationKey == "" {
		return errors.New("migration diagnostic key is required")
	}
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if _, err := tx.ExecContext(petContext(ctx), `DELETE FROM pet_migration_diagnostics WHERE migration_key = ?`, migrationKey); err != nil {
		return fmt.Errorf("clear pet migration diagnostics: %w", err)
	}
	for _, diagnostic := range diagnostics {
		if err := d.appendMigrationDiagnostic(ctx, tx, migrationKey, diagnostic); err != nil {
			return err
		}
	}
	return commitPetTx(tx)
}

func (d *PetDAO) ListMigrationDiagnostics(ctx context.Context, migrationKey string) ([]PetMigrationDiagnostic, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(petContext(ctx), `
		SELECT diagnostic_json
		  FROM pet_migration_diagnostics
		 WHERE migration_key = ?
		 ORDER BY diagnostic_id ASC
	`, strings.TrimSpace(migrationKey))
	if err != nil {
		return nil, fmt.Errorf("list pet migration diagnostics: %w", err)
	}
	defer rows.Close()
	result := make([]PetMigrationDiagnostic, 0)
	for rows.Next() {
		var diagnostic PetMigrationDiagnostic
		if err := scanPetRowJSON(rows, &diagnostic); err != nil {
			return nil, err
		}
		result = append(result, diagnostic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pet migration diagnostics: %w", err)
	}
	return result, nil
}

// saveSnapshotAndDiagnostics 让迁移的目标数据和诊断一起提交，避免“数据已导入但诊断没落盘”的半完成状态。
func (d *PetDAO) saveSnapshotAndDiagnostics(ctx context.Context, snapshot PetMigrationSnapshot, migrationKey string, diagnostics []PetMigrationDiagnostic) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	petID := snapshotPetID(snapshot)
	setSnapshotPetIDs(&snapshot, petID)
	tx, err := beginPetTx(ctx, d.db)
	if err != nil {
		return err
	}
	defer rollbackPetTx(tx)
	if err := d.saveSnapshotTx(petContext(ctx), tx, snapshot); err != nil {
		return err
	}
	if _, err := tx.ExecContext(petContext(ctx), `DELETE FROM pet_migration_diagnostics WHERE migration_key = ?`, migrationKey); err != nil {
		return fmt.Errorf("clear pet migration diagnostics: %w", err)
	}
	for _, diagnostic := range diagnostics {
		if err := d.appendMigrationDiagnostic(ctx, tx, migrationKey, diagnostic); err != nil {
			return err
		}
	}
	return commitPetTx(tx)
}
