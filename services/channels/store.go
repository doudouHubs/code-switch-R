package channels

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

const (
	channelSchemaVersion       = "3"
	openCoworkImportMarker     = "opencowork-plugins-v1"
	builtinConsolidationMarker = "builtin-instances-v2"
	openCoworkPluginsFileName  = "plugins.json"
)

type Store struct {
	db   *sql.DB
	path string
	mu   sync.RWMutex
}

func OpenDefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home: %w", err)
	}
	return OpenStore(filepath.Join(home, ".code-switch", "channels.db"))
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("channel database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create channel database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open channel database: %w", err)
	}
	store := &Store{db: db, path: path}
	if err := store.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) configure() error {
	if s == nil || s.db == nil {
		return errors.New("channel store is unavailable")
	}
	for _, statement := range []string{
		"PRAGMA busy_timeout = 30000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("configure channel sqlite: %w", err)
		}
	}
	return s.ensureSchema()
}

func (s *Store) ensureSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS channel_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS channel_instances (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 0,
			builtin INTEGER NOT NULL DEFAULT 0,
			archived INTEGER NOT NULL DEFAULT 0,
			config_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL,
			project_id TEXT,
			provider_platform TEXT NOT NULL DEFAULT '',
			provider_id TEXT,
			model TEXT,
			tools_json TEXT NOT NULL DEFAULT '{}',
			features_json TEXT NOT NULL DEFAULT '{}',
			permissions_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'stopped',
			last_error TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_instances_project ON channel_instances(project_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS channel_import_templates (
			type TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			config_json TEXT NOT NULL DEFAULT '{}',
			provider_id TEXT,
			model TEXT,
			tools_json TEXT NOT NULL DEFAULT '{}',
			features_json TEXT NOT NULL DEFAULT '{}',
			permissions_json TEXT NOT NULL DEFAULT '{}',
			imported_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS channel_sessions (
			id TEXT PRIMARY KEY,
			instance_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			chat_name TEXT NOT NULL DEFAULT '',
			sender_id TEXT NOT NULL DEFAULT '',
			sender_name TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL,
			working_folder TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY(instance_id) REFERENCES channel_instances(id) ON DELETE CASCADE,
			UNIQUE(instance_id, chat_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_channel_sessions_instance ON channel_sessions(instance_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS channel_messages (
			id TEXT PRIMARY KEY,
			instance_id TEXT NOT NULL,
			session_id TEXT,
			external_id TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL DEFAULT '',
			sender_name TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			images_json TEXT NOT NULL DEFAULT '[]',
			audio_json TEXT,
			timestamp INTEGER NOT NULL,
			raw TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			FOREIGN KEY(instance_id) REFERENCES channel_instances(id) ON DELETE CASCADE,
			FOREIGN KEY(session_id) REFERENCES channel_sessions(id) ON DELETE SET NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_messages_external
			ON channel_messages(instance_id, external_id) WHERE external_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_channel_messages_session ON channel_messages(session_id, timestamp ASC, id ASC)`,
		`CREATE TABLE IF NOT EXISTS channel_media (
			id TEXT PRIMARY KEY,
			message_id TEXT NOT NULL,
			instance_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			media_type TEXT NOT NULL,
			file_name TEXT NOT NULL DEFAULT '',
			data BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(message_id) REFERENCES channel_messages(id) ON DELETE CASCADE,
			FOREIGN KEY(instance_id) REFERENCES channel_instances(id) ON DELETE CASCADE
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("ensure channel schema: %w", err)
		}
	}
	// 旧版数据库已创建 channel_instances，但没有 provider_platform；SQLite
	// 的 CREATE TABLE IF NOT EXISTS 不会补列，因此必须显式做一次幂等迁移。
	if err := s.ensureColumn("channel_instances", "provider_platform", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// 归档字段用于收敛旧的项目副本，但不删除其会话和消息；旧数据库必须显式补列。
	if err := s.ensureColumn("channel_instances", "archived", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_channel_instances_archived ON channel_instances(archived, updated_at DESC)`); err != nil {
		return fmt.Errorf("create archived channel index: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO channel_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "schema-version", channelSchemaVersion); err != nil {
		return fmt.Errorf("save channel schema version: %w", err)
	}
	return nil
}

func (s *Store) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect channel schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan channel schema: %w", err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read channel schema: %w", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil {
		return fmt.Errorf("migrate channel schema: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) setMetaIfMissing(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO channel_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, key, value)
	return err
}

func (s *Store) meta(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM channel_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertInstanceWithExec(exec sqlExecer, instance ChannelInstance) error {
	instance = normalizeInstance(instance)
	if instance.Enabled && (instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "") {
		return errors.New("an enabled channel must be bound to a project")
	}
	if instance.Archived && (instance.Enabled || instance.Status == "running") {
		return errors.New("an archived channel must remain stopped")
	}
	configJSON, err := json.Marshal(instance.Config)
	if err != nil {
		return fmt.Errorf("encode channel config: %w", err)
	}
	toolsJSON, err := json.Marshal(instance.Tools)
	if err != nil {
		return fmt.Errorf("encode channel tools: %w", err)
	}
	featuresJSON, err := json.Marshal(instance.Features)
	if err != nil {
		return fmt.Errorf("encode channel features: %w", err)
	}
	permissionsJSON, err := json.Marshal(instance.Permissions)
	if err != nil {
		return fmt.Errorf("encode channel permissions: %w", err)
	}
	_, err = exec.Exec(`
		INSERT INTO channel_instances(
			id, type, name, enabled, builtin, archived, config_json, created_at, project_id,
			provider_platform, provider_id, model, tools_json, features_json, permissions_json, status,
			last_error, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			type=excluded.type, name=excluded.name, enabled=excluded.enabled,
			builtin=excluded.builtin, archived=excluded.archived, config_json=excluded.config_json,
			project_id=excluded.project_id, provider_platform=excluded.provider_platform, provider_id=excluded.provider_id,
			model=excluded.model, tools_json=excluded.tools_json,
			features_json=excluded.features_json, permissions_json=excluded.permissions_json,
			status=excluded.status, last_error=excluded.last_error, updated_at=excluded.updated_at`,
		instance.ID, instance.Type, instance.Name, boolInt(instance.Enabled), boolInt(instance.Builtin), boolInt(instance.Archived),
		string(configJSON), instance.CreatedAt, nullableString(instance.ProjectID), instance.ProviderPlatform, nullableString(instance.ProviderID),
		nullableString(instance.Model), string(toolsJSON), string(featuresJSON), string(permissionsJSON),
		instance.Status, instance.LastError, instance.UpdatedAt,
	)
	return err
}

func (s *Store) upsertInstanceLocked(instance ChannelInstance) error {
	return upsertInstanceWithExec(s.db, instance)
}

func (s *Store) UpsertInstance(instance ChannelInstance) error {
	if strings.TrimSpace(instance.ID) == "" || strings.TrimSpace(instance.Type) == "" {
		return errors.New("channel instance id and type are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureMutableInstanceLocked(instance.ID); err != nil {
		return err
	}
	return s.upsertInstanceLocked(instance)
}

func (s *Store) ListInstances() ([]ChannelInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id,type,name,enabled,builtin,archived,config_json,created_at,project_id,provider_platform,provider_id,model,tools_json,features_json,permissions_json,status,last_error,updated_at FROM channel_instances ORDER BY archived ASC, builtin DESC, name ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	instances := make([]ChannelInstance, 0)
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return instances, nil
}

func (s *Store) GetInstance(id string) (ChannelInstance, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`SELECT id,type,name,enabled,builtin,archived,config_json,created_at,project_id,provider_platform,provider_id,model,tools_json,features_json,permissions_json,status,last_error,updated_at FROM channel_instances WHERE id = ?`, strings.TrimSpace(id))
	instance, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelInstance{}, false, nil
	}
	if err != nil {
		return ChannelInstance{}, false, err
	}
	return instance, true, nil
}

func (s *Store) DeleteInstance(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureMutableInstanceLocked(id); err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM channel_instances WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ensureMutableInstanceLocked 是持久化层最后一道只读边界。
// API 和 Manager 都有对应的业务校验，但消息、媒体和测试替身也可能直接写 Store；
// 在这里统一拦截 archived 实例，避免某个新调用方绕过上层又污染历史快照。
func (s *Store) ensureMutableInstanceLocked(id string) error {
	var archived int
	err := s.db.QueryRow(`SELECT archived FROM channel_instances WHERE id = ?`, strings.TrimSpace(id)).Scan(&archived)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if archived != 0 {
		return errors.New("archived channel is read-only")
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanInstance(row scanner) (ChannelInstance, error) {
	var (
		instance                                             ChannelInstance
		enabled, builtin, archived                           int
		configJSON, toolsJSON, featuresJSON, permissionsJSON string
		projectID, providerID, model                         sql.NullString
	)
	if err := row.Scan(&instance.ID, &instance.Type, &instance.Name, &enabled, &builtin, &archived, &configJSON, &instance.CreatedAt, &projectID, &instance.ProviderPlatform, &providerID, &model, &toolsJSON, &featuresJSON, &permissionsJSON, &instance.Status, &instance.LastError, &instance.UpdatedAt); err != nil {
		return ChannelInstance{}, err
	}
	instance.Enabled = enabled != 0
	instance.Builtin = builtin != 0
	instance.Archived = archived != 0
	if projectID.Valid && projectID.String != "" {
		value := projectID.String
		instance.ProjectID = &value
	}
	if providerID.Valid && providerID.String != "" {
		value := providerID.String
		instance.ProviderID = &value
	}
	if model.Valid && model.String != "" {
		value := model.String
		instance.Model = &value
	}
	if err := json.Unmarshal([]byte(configJSON), &instance.Config); err != nil {
		return ChannelInstance{}, err
	}
	if err := json.Unmarshal([]byte(toolsJSON), &instance.Tools); err != nil {
		return ChannelInstance{}, err
	}
	if err := json.Unmarshal([]byte(featuresJSON), &instance.Features); err != nil {
		return ChannelInstance{}, err
	}
	if err := json.Unmarshal([]byte(permissionsJSON), &instance.Permissions); err != nil {
		return ChannelInstance{}, err
	}
	return normalizeInstance(instance), nil
}

func (s *Store) UpsertSession(session ChannelSession) error {
	if strings.TrimSpace(session.InstanceID) == "" || strings.TrimSpace(session.ChatID) == "" || strings.TrimSpace(session.ProjectID) == "" || strings.TrimSpace(session.WorkingFolder) == "" {
		return errors.New("channel session requires instance, chat and bound project")
	}
	if session.ID == "" {
		session.ID = sessionKey(session.InstanceID, session.ChatID)
	}
	if session.CreatedAt == 0 {
		session.CreatedAt = nowMillis()
	}
	if session.UpdatedAt == 0 {
		session.UpdatedAt = session.CreatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureMutableInstanceLocked(session.InstanceID); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO channel_sessions(id,instance_id,chat_id,chat_name,sender_id,sender_name,project_id,working_folder,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(instance_id,chat_id) DO UPDATE SET
			chat_name=excluded.chat_name,sender_id=excluded.sender_id,sender_name=excluded.sender_name,
			project_id=excluded.project_id,working_folder=excluded.working_folder,updated_at=excluded.updated_at`,
		session.ID, session.InstanceID, session.ChatID, session.ChatName, session.SenderID, session.SenderName,
		session.ProjectID, session.WorkingFolder, session.CreatedAt, session.UpdatedAt)
	return err
}

func (s *Store) GetSession(instanceID, chatID string) (ChannelSession, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`SELECT id,instance_id,chat_id,chat_name,sender_id,sender_name,project_id,working_folder,created_at,updated_at FROM channel_sessions WHERE instance_id=? AND chat_id=?`, instanceID, chatID)
	var session ChannelSession
	err := row.Scan(&session.ID, &session.InstanceID, &session.ChatID, &session.ChatName, &session.SenderID, &session.SenderName, &session.ProjectID, &session.WorkingFolder, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelSession{}, false, nil
	}
	if err != nil {
		return ChannelSession{}, false, err
	}
	return session, true, nil
}

func (s *Store) ListSessions(instanceID string) ([]ChannelSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id,instance_id,chat_id,chat_name,sender_id,sender_name,project_id,working_folder,created_at,updated_at FROM channel_sessions WHERE instance_id=? ORDER BY updated_at DESC`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ChannelSession, 0)
	for rows.Next() {
		var session ChannelSession
		if err := rows.Scan(&session.ID, &session.InstanceID, &session.ChatID, &session.ChatName, &session.SenderID, &session.SenderName, &session.ProjectID, &session.WorkingFolder, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, session)
	}
	return result, rows.Err()
}

func (s *Store) AppendMessage(message ChannelMessage) error {
	_, err := s.appendMessage(message)
	return err
}

// AppendMessageIfNew 在外部消息 ID 唯一约束下返回真实插入结果。
// webhook/long-poll provider 可能重复投递同一事件，只有首次插入才允许进入 Agent。
func (s *Store) AppendMessageIfNew(message ChannelMessage) (bool, error) {
	return s.appendMessage(message)
}

func (s *Store) appendMessage(message ChannelMessage) (bool, error) {
	if strings.TrimSpace(message.InstanceID) == "" || strings.TrimSpace(message.Role) == "" || strings.TrimSpace(message.ChatID) == "" {
		return false, errors.New("channel message requires instance, role and chat")
	}
	if message.ID == "" {
		message.ID = sessionKey(message.InstanceID, message.ExternalID, fmt.Sprint(message.Timestamp), message.Role)
	}
	if message.Timestamp == 0 {
		message.Timestamp = nowMillis()
	}
	imagesJSON, err := json.Marshal(message.Images)
	if err != nil {
		return false, err
	}
	var audioJSON any
	if message.Audio != nil {
		data, err := json.Marshal(message.Audio)
		if err != nil {
			return false, err
		}
		audioJSON = string(data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureMutableInstanceLocked(message.InstanceID); err != nil {
		return false, err
	}
	result, err := s.db.Exec(`
		INSERT INTO channel_messages(id,instance_id,session_id,external_id,role,chat_id,sender_id,sender_name,content,images_json,audio_json,timestamp,raw,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`,
		message.ID, message.InstanceID, nullableStringValue(message.SessionID), message.ExternalID, message.Role, message.ChatID,
		message.SenderID, message.SenderName, message.Content, string(imagesJSON), audioJSON, message.Timestamp, message.Raw, nowMillis())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) GetSessionByID(id string) (ChannelSession, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`SELECT id,instance_id,chat_id,chat_name,sender_id,sender_name,project_id,working_folder,created_at,updated_at FROM channel_sessions WHERE id=?`, strings.TrimSpace(id))
	var session ChannelSession
	err := row.Scan(&session.ID, &session.InstanceID, &session.ChatID, &session.ChatName, &session.SenderID, &session.SenderName, &session.ProjectID, &session.WorkingFolder, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelSession{}, false, nil
	}
	if err != nil {
		return ChannelSession{}, false, err
	}
	return session, true, nil
}

func (s *Store) ListMessages(sessionID string, limit int) ([]ChannelMessage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT id,instance_id,COALESCE(session_id,''),external_id,role,chat_id,sender_id,sender_name,content,images_json,audio_json,timestamp,raw FROM channel_messages WHERE session_id=? ORDER BY timestamp DESC,id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ChannelMessage, 0)
	for rows.Next() {
		var message ChannelMessage
		var imagesJSON, audioJSON sql.NullString
		if err := rows.Scan(&message.ID, &message.InstanceID, &message.SessionID, &message.ExternalID, &message.Role, &message.ChatID, &message.SenderID, &message.SenderName, &message.Content, &imagesJSON, &audioJSON, &message.Timestamp, &message.Raw); err != nil {
			return nil, err
		}
		if imagesJSON.Valid {
			_ = json.Unmarshal([]byte(imagesJSON.String), &message.Images)
		}
		if audioJSON.Valid && audioJSON.String != "" {
			var audio ChannelMedia
			if json.Unmarshal([]byte(audioJSON.String), &audio) == nil {
				message.Audio = &audio
			}
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Timestamp < result[j].Timestamp })
	return result, nil
}

func (s *Store) SaveMedia(messageID, instanceID string, media ChannelMedia) error {
	if media.ID == "" {
		media.ID = sessionKey(messageID, media.Kind, media.FileName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureMutableInstanceLocked(instanceID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO channel_media(id,message_id,instance_id,kind,media_type,file_name,data,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,media_type=excluded.media_type,file_name=excluded.file_name`, media.ID, messageID, instanceID, media.Kind, media.MediaType, media.FileName, media.Data, nowMillis())
	return err
}

func sessionKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "channel-" + hex.EncodeToString(hash.Sum(nil))[:32]
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableStringValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}
