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
	"strconv"
	"strings"
)

type openCoworkPlugin struct {
	ID          string             `json:"id"`
	Type        string             `json:"type"`
	Name        string             `json:"name"`
	Enabled     bool               `json:"enabled"`
	Config      map[string]any     `json:"config"`
	ProviderID  *string            `json:"providerId"`
	Model       *string            `json:"model"`
	Tools       map[string]bool    `json:"tools"`
	Features    ChannelFeatures    `json:"features"`
	Permissions ChannelPermissions `json:"permissions"`
}

type openCoworkPluginEnvelope struct {
	Plugins []openCoworkPlugin `json:"plugins"`
}

// ImportOpenCoworkOnce 只把旧配置转成当前项目的模板，不沿用 OpenCowork 的项目 ID。
// 两个应用的项目事实源不同，直接复制 projectId 会产生“界面显示已绑定、运行时找不到目录”的假状态。
func (s *Store) ImportOpenCoworkOnce(path string) (ImportReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, applied, err := s.meta(openCoworkImportMarker); err != nil {
		return ImportReport{}, err
	} else if applied {
		return ImportReport{AlreadyApplied: 1}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := s.setMetaIfMissing(openCoworkImportMarker, "missing"); err != nil {
				return ImportReport{}, err
			}
			return ImportReport{}, nil
		}
		return ImportReport{}, fmt.Errorf("read OpenCowork channel config: %w", err)
	}
	var plugins []openCoworkPlugin
	if err := json.Unmarshal(data, &plugins); err != nil {
		var envelope openCoworkPluginEnvelope
		if envelopeErr := json.Unmarshal(data, &envelope); envelopeErr != nil {
			return ImportReport{}, fmt.Errorf("parse OpenCowork channel config: %w", err)
		}
		plugins = envelope.Plugins
	}
	// 同一平台通常有多个项目副本，只保留第一个配置完整的模板，避免旧项目 ID 污染新项目绑定。
	templates := make(map[string]openCoworkPlugin)
	report := ImportReport{}
	for _, plugin := range plugins {
		plugin.Type = strings.TrimSpace(plugin.Type)
		if plugin.Type == "" || !isBuiltinChannelType(plugin.Type) {
			report.Skipped++
			continue
		}
		current, exists := templates[plugin.Type]
		if !exists || configScore(plugin.Config) > configScore(current.Config) || (configScore(plugin.Config) == configScore(current.Config) && plugin.Enabled && !current.Enabled) {
			templates[plugin.Type] = plugin
		}
		report.Imported++
	}
	types := make([]string, 0, len(templates))
	for channelType := range templates {
		types = append(types, channelType)
	}
	sort.Strings(types)
	for _, channelType := range types {
		plugin := templates[channelType]
		config := stringifyConfig(plugin.Config)
		tools := plugin.Tools
		if tools == nil {
			tools = map[string]bool{}
		}
		features := plugin.Features
		if features == (ChannelFeatures{}) {
			features = defaultFeatures()
		}
		permissions := plugin.Permissions
		if permissions.ReadablePathPrefixes == nil {
			permissions = defaultPermissions()
		}
		configJSON, _ := json.Marshal(config)
		toolsJSON, _ := json.Marshal(tools)
		featuresJSON, _ := json.Marshal(features)
		permissionsJSON, _ := json.Marshal(permissions)
		_, err := s.db.Exec(`INSERT INTO channel_import_templates(type,name,config_json,provider_id,model,tools_json,features_json,permissions_json,imported_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(type) DO UPDATE SET name=excluded.name,config_json=excluded.config_json,provider_id=excluded.provider_id,model=excluded.model,tools_json=excluded.tools_json,features_json=excluded.features_json,permissions_json=excluded.permissions_json,imported_at=excluded.imported_at`, channelType, strings.TrimSpace(plugin.Name), string(configJSON), nullableString(plugin.ProviderID), nullableString(plugin.Model), string(toolsJSON), string(featuresJSON), string(permissionsJSON), nowMillis())
		if err != nil {
			return ImportReport{}, fmt.Errorf("save channel template: %w", err)
		}
	}
	report.Templates = len(types)
	if err := s.setMetaIfMissing(openCoworkImportMarker, strconv.FormatInt(nowMillis(), 10)); err != nil {
		return ImportReport{}, err
	}
	return report, nil
}

// EnsureBuiltinInstances 将旧版“每个项目一套平台实例”收敛为每个平台一个全局实例。
// 频道页面负责把这八个 active 实例绑定到项目；已退休的归档实例和旧重复实例直接删除，
// 不再把它们转换成第二种可见的频道生命周期。迁移在事务内完成，随后用唯一索引把
// “每个平台只能有一个内置实例”固化到数据库。
func (s *Store) EnsureBuiltinInstances() (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("channel store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, applied, err := s.meta(builtinConsolidationMarker)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin builtin channel migration: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	// 归档数据是不可恢复的历史副本；实例删除会依靠现有外键级联清除其
	// session、message 和 media，必须放在 canonical 收敛和幂等提前返回之前。
	if _, err := tx.Exec(`DELETE FROM channel_instances WHERE archived=1`); err != nil {
		return 0, fmt.Errorf("purge archived channel instances: %w", err)
	}

	templates, err := loadTemplateRecords(tx)
	if err != nil {
		return 0, err
	}
	instances, err := loadBuiltinInstances(tx)
	if err != nil {
		return 0, err
	}
	if applied && !builtinConsolidationNeeded(instances) {
		if err := ensureBuiltinActiveIndex(tx); err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit builtin channel index: %w", err)
		}
		rollback = false
		return 0, nil
	}

	created := 0
	for _, channelType := range BuiltinChannelTypes {
		canonicalID := canonicalBuiltinInstanceID(channelType)
		candidates := instances[channelType]
		canonicalExists := false
		for _, candidate := range candidates {
			if candidate.ID == canonicalID {
				canonicalExists = true
				break
			}
		}
		winner, found := chooseBuiltinInstance(candidates, canonicalID)
		if !found {
			winner, err = newBuiltinInstanceFromTemplate(channelType, templates[channelType])
			if err != nil {
				return created, err
			}
		}
		if !canonicalExists {
			created++
		}
		canonical := canonicalBuiltinInstance(winner, canonicalID, channelType, templates[channelType])
		if err := upsertInstanceWithExec(tx, canonical); err != nil {
			return created, fmt.Errorf("save canonical %s channel: %w", channelType, err)
		}

		for _, instance := range candidates {
			if instance.ID == canonicalID {
				continue
			}
			// 旧重复实例的历史属于已退休的项目级频道；不搬迁到 canonical
			// 实例，直接删除可以避免跨项目串历史，也不会再次生成归档入口。
			if _, err := tx.Exec(`DELETE FROM channel_instances WHERE id=?`, instance.ID); err != nil {
				return created, fmt.Errorf("delete duplicate %s channel %s: %w", channelType, instance.ID, err)
			}
		}
	}

	if _, err := tx.Exec(`INSERT INTO channel_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, builtinConsolidationMarker, fmt.Sprint(nowMillis())); err != nil {
		return created, fmt.Errorf("save builtin channel migration marker: %w", err)
	}
	if err := ensureBuiltinActiveIndex(tx); err != nil {
		return created, err
	}
	if err := tx.Commit(); err != nil {
		return created, fmt.Errorf("commit builtin channel migration: %w", err)
	}
	rollback = false
	return created, nil
}

type sqlQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func loadTemplateRecords(queryer sqlQueryer) (map[string]templateRecord, error) {
	rows, err := queryer.Query(`SELECT type,name,config_json,provider_id,model,tools_json,features_json,permissions_json FROM channel_import_templates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	templates := make(map[string]templateRecord)
	for rows.Next() {
		var record templateRecord
		if err := rows.Scan(&record.Type, &record.Name, &record.ConfigJSON, &record.ProviderID, &record.Model, &record.ToolsJSON, &record.FeaturesJSON, &record.PermissionsJSON); err != nil {
			return nil, err
		}
		templates[record.Type] = record
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return templates, nil
}

func loadBuiltinInstances(queryer sqlQueryer) (map[string][]ChannelInstance, error) {
	rows, err := queryer.Query(`SELECT id,type,name,enabled,builtin,config_json,created_at,project_id,provider_platform,provider_id,model,tools_json,features_json,permissions_json,status,last_error,updated_at FROM channel_instances WHERE builtin=1 AND archived=0 ORDER BY type ASC, updated_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	instances := make(map[string][]ChannelInstance)
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		instances[instance.Type] = append(instances[instance.Type], instance)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return instances, nil
}

func builtinConsolidationNeeded(instances map[string][]ChannelInstance) bool {
	for _, channelType := range BuiltinChannelTypes {
		canonicalID := canonicalBuiltinInstanceID(channelType)
		canonicalActive := false
		for _, instance := range instances[channelType] {
			if instance.ID == canonicalID {
				canonicalActive = true
				continue
			}
			return true
		}
		if !canonicalActive {
			return true
		}
	}
	return false
}

func chooseBuiltinInstance(candidates []ChannelInstance, canonicalID string) (ChannelInstance, bool) {
	if len(candidates) == 0 {
		return ChannelInstance{}, false
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		bestScore := instanceCompletenessScore(best)
		candidateScore := instanceCompletenessScore(candidate)
		if candidateScore > bestScore ||
			(candidateScore == bestScore && candidate.ID == canonicalID && best.ID != canonicalID) ||
			(candidateScore == bestScore && candidate.ID != canonicalID && best.ID != canonicalID && candidate.UpdatedAt > best.UpdatedAt) ||
			(candidateScore == bestScore && candidate.ID != canonicalID && best.ID != canonicalID && candidate.UpdatedAt == best.UpdatedAt && candidate.ID < best.ID) {
			best = candidate
		}
	}
	return best, true
}

func instanceCompletenessScore(instance ChannelInstance) int {
	score := 0
	for _, value := range instance.Config {
		if strings.TrimSpace(value) != "" {
			score++
		}
	}
	if strings.TrimSpace(instance.ProviderPlatform) != "" {
		score++
	}
	if instance.ProviderID != nil && strings.TrimSpace(*instance.ProviderID) != "" {
		score++
	}
	if instance.Model != nil && strings.TrimSpace(*instance.Model) != "" {
		score++
	}
	for _, enabled := range instance.Tools {
		if enabled {
			score++
		}
	}
	return score
}

func newBuiltinInstanceFromTemplate(channelType string, template templateRecord) (ChannelInstance, error) {
	instance := ChannelInstance{ID: canonicalBuiltinInstanceID(channelType), Type: channelType, Name: descriptorName(channelType), Builtin: true, Config: map[string]string{}, Status: "stopped"}
	if template.Type == "" {
		return normalizeInstance(instance), nil
	}
	if strings.TrimSpace(template.Name) != "" {
		instance.Name = strings.TrimSpace(template.Name)
	}
	if err := json.Unmarshal([]byte(template.ConfigJSON), &instance.Config); err != nil {
		return ChannelInstance{}, fmt.Errorf("decode %s channel config: %w", channelType, err)
	}
	if template.ProviderID.Valid {
		value := template.ProviderID.String
		instance.ProviderID = &value
	}
	if template.Model.Valid {
		value := template.Model.String
		instance.Model = &value
	}
	if err := json.Unmarshal([]byte(template.ToolsJSON), &instance.Tools); err != nil {
		return ChannelInstance{}, fmt.Errorf("decode %s channel tools: %w", channelType, err)
	}
	if err := json.Unmarshal([]byte(template.FeaturesJSON), &instance.Features); err != nil {
		return ChannelInstance{}, fmt.Errorf("decode %s channel features: %w", channelType, err)
	}
	if err := json.Unmarshal([]byte(template.PermissionsJSON), &instance.Permissions); err != nil {
		return ChannelInstance{}, fmt.Errorf("decode %s channel permissions: %w", channelType, err)
	}
	return normalizeInstance(instance), nil
}

func canonicalBuiltinInstance(winner ChannelInstance, canonicalID, channelType string, template templateRecord) ChannelInstance {
	if strings.TrimSpace(winner.Name) == "" {
		winner.Name = descriptorName(channelType)
	}
	if winner.Name == descriptorName(channelType) && strings.TrimSpace(template.Name) != "" && instanceCompletenessScore(winner) == 0 {
		winner.Name = strings.TrimSpace(template.Name)
	}
	winner.ID = canonicalID
	winner.Type = channelType
	winner.Builtin = true
	winner.Enabled = false
	winner.ProjectID = nil
	winner.Status = "stopped"
	winner.UpdatedAt = nowMillis()
	return normalizeInstance(winner)
}

func ensureBuiltinActiveIndex(exec sqlExecer) error {
	// 旧索引按 project_id 限制重复，和频道页面的全局 canonical 语义冲突；
	// 先撤掉旧 owner 和带 archived 谓词的旧索引，再创建不依赖已退休状态列的
	// 全局唯一约束，确保后续不会再出现同平台的第二个内置入口。
	if _, err := exec.Exec(`DROP INDEX IF EXISTS idx_channel_builtin_project_type`); err != nil {
		return fmt.Errorf("drop legacy builtin channel index: %w", err)
	}
	if _, err := exec.Exec(`DROP INDEX IF EXISTS idx_channel_builtin_active_type`); err != nil {
		return fmt.Errorf("drop legacy builtin active index: %w", err)
	}
	if _, err := exec.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_builtin_active_type ON channel_instances(type) WHERE builtin = 1`); err != nil {
		return fmt.Errorf("create builtin channel active index: %w", err)
	}
	return nil
}

type templateRecord struct {
	Type, Name, ConfigJSON                   string
	ProviderID, Model                        sql.NullString
	ToolsJSON, FeaturesJSON, PermissionsJSON string
}

func defaultOpenCoworkPluginsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".open-cowork", openCoworkPluginsFileName)
}
func isBuiltinChannelType(value string) bool {
	for _, candidate := range BuiltinChannelTypes {
		if value == candidate {
			return true
		}
	}
	return false
}
func descriptorName(channelType string) string {
	names := map[string]string{ChannelTypeFeishu: "Feishu Bot", ChannelTypeDingTalk: "DingTalk Bot", ChannelTypeWeCom: "WeCom Bot", ChannelTypeQQ: "QQ Bot", ChannelTypeWeixin: "WeChat Official", ChannelTypeTelegram: "Telegram Bot", ChannelTypeDiscord: "Discord Bot", ChannelTypeWhatsApp: "WhatsApp Bot"}
	return names[channelType]
}
func configScore(config map[string]any) int {
	score := 0
	for _, value := range config {
		if strings.TrimSpace(fmt.Sprint(value)) != "" {
			score++
		}
	}
	return score
}
func stringifyConfig(config map[string]any) map[string]string {
	result := map[string]string{}
	for key, value := range config {
		switch typed := value.(type) {
		case string:
			result[key] = typed
		case bool:
			result[key] = strconv.FormatBool(typed)
		case float64:
			result[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		default:
			if encoded, err := json.Marshal(typed); err == nil {
				result[key] = string(encoded)
			}
		}
	}
	return result
}
func canonicalBuiltinInstanceID(channelType string) string {
	sum := sha256.Sum256([]byte("codeswitch:builtin-channel:\x00" + channelType))
	return "builtin-" + hex.EncodeToString(sum[:])[:24]
}
