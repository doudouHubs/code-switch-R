package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	petMigrationSettingsFile = "settings.json"
	petMigrationDreamDBFile  = "pet-dreams.db"
	petMigrationPetsDir      = "pets"
	petMigrationDreamsDir    = "pet-dreams"
	petMigrationMemoryFile   = "MEMORY.md"
	petMigrationMaxJSONBytes = 32 << 20
)

// PetMigrationOptions 控制旧 OpenCowork 数据源和目标宠物。
// SourceRoot 为空时默认读取当前用户的 ~/.open-cowork；测试和多用户迁移可显式传入目录。
type PetMigrationOptions struct {
	SourceRoot string
	PetID      string
	Now        func() int64
}

// PetMigrator 只读访问旧数据，并把结果交给 PetDAO 事务化落盘。
// 迁移器不持有任何 API 凭据，也不会调用旧应用的写接口。
type PetMigrator struct {
	dao        *PetDAO
	sourceRoot string
	petID      string
	now        func() int64
}

// NewPetMigrator 创建 OpenCowork 宠物迁移器。
func NewPetMigrator(dao *PetDAO, sourceRoot string) *PetMigrator {
	return &PetMigrator{
		dao:        dao,
		sourceRoot: strings.TrimSpace(sourceRoot),
		petID:      DefaultPetID,
		now:        func() int64 { return time.Now().UnixMilli() },
	}
}

// MigrateOpenCoworkPet 是启动流程使用的便捷入口。
func MigrateOpenCoworkPet(ctx context.Context, dao *PetDAO, options PetMigrationOptions) (PetMigrationReport, error) {
	migrator := NewPetMigrator(dao, options.SourceRoot)
	if strings.TrimSpace(options.PetID) != "" {
		migrator.petID = strings.TrimSpace(options.PetID)
	}
	if options.Now != nil {
		migrator.now = options.Now
	}
	return migrator.Migrate(ctx)
}

// Migrate 执行一次可重入迁移。source fingerprint 未变化且 marker 已完成时直接返回，
// 这样应用每次启动都可以调用而不会重复扫描、重复写入或覆盖用户在目标端的新状态。
func (m *PetMigrator) Migrate(ctx context.Context) (PetMigrationReport, error) {
	var report PetMigrationReport
	if m == nil || m.dao == nil {
		return report, errors.New("pet migrator requires a non-nil dao")
	}
	ctx = petContext(ctx)
	sourceRoot, err := m.resolveSourceRoot()
	if err != nil {
		return report, err
	}
	petID := normalizePetID(m.petID)
	fingerprint, err := fingerprintPetMigrationSource(sourceRoot)
	if err != nil {
		return report, fmt.Errorf("fingerprint OpenCowork pet source: %w", err)
	}
	report = PetMigrationReport{
		MigrationKey:      OpenCoworkPetMigrationKey,
		Version:           OpenCoworkPetMigrationVersion,
		SourceRoot:        sourceRoot,
		SourceFingerprint: fingerprint,
		Diagnostics:       make([]PetMigrationDiagnostic, 0),
	}

	marker, err := m.dao.LoadMigrationMarker(ctx, OpenCoworkPetMigrationKey)
	if err != nil {
		return report, err
	}
	if marker != nil &&
		marker.Status == "completed" &&
		marker.Version >= OpenCoworkPetMigrationVersion &&
		marker.SourceFingerprint == fingerprint {
		report.AlreadyApplied = true
		diagnostics, diagnosticErr := m.dao.ListMigrationDiagnostics(ctx, OpenCoworkPetMigrationKey)
		if diagnosticErr == nil {
			report.Diagnostics = diagnostics
			report = summarizePetMigrationReport(report)
		}
		return report, nil
	}

	startedAt := m.currentTime()
	running := PetMigrationMarker{
		MigrationKey:      OpenCoworkPetMigrationKey,
		Version:           OpenCoworkPetMigrationVersion,
		Status:            "running",
		SourceFingerprint: fingerprint,
		StartedAt:         startedAt,
	}
	if err := m.dao.SaveMigrationMarker(ctx, running); err != nil {
		return report, fmt.Errorf("start pet migration: %w", err)
	}

	snapshot, diagnostics, imported, skipped, err := m.readSnapshot(ctx, sourceRoot, petID)
	if err != nil {
		return m.fail(ctx, report, startedAt, err)
	}
	report.Diagnostics = diagnostics
	report.Imported = imported
	report.Skipped = skipped
	report = summarizePetMigrationReport(report)

	// 目标数据与诊断必须在同一事务内提交；否则一半数据成功、下一次启动又无法解释
	// 上一次结果，最终会形成比源数据更难收拾的双重状态。
	if err := m.dao.saveSnapshotAndDiagnostics(ctx, snapshot, OpenCoworkPetMigrationKey, diagnostics); err != nil {
		return m.fail(ctx, report, startedAt, fmt.Errorf("save migrated pet snapshot: %w", err))
	}
	completedAt := m.currentTime()
	completed := PetMigrationMarker{
		MigrationKey:      OpenCoworkPetMigrationKey,
		Version:           OpenCoworkPetMigrationVersion,
		Status:            "completed",
		SourceFingerprint: fingerprint,
		StartedAt:         startedAt,
		CompletedAt:       &completedAt,
	}
	if err := m.dao.SaveMigrationMarker(ctx, completed); err != nil {
		return m.fail(ctx, report, startedAt, fmt.Errorf("complete pet migration: %w", err))
	}
	return report, nil
}

func (m *PetMigrator) fail(ctx context.Context, report PetMigrationReport, startedAt int64, cause error) (PetMigrationReport, error) {
	message := cause.Error()
	failed := PetMigrationMarker{
		MigrationKey:      OpenCoworkPetMigrationKey,
		Version:           OpenCoworkPetMigrationVersion,
		Status:            "failed",
		SourceFingerprint: report.SourceFingerprint,
		StartedAt:         startedAt,
		LastError:         message,
	}
	_ = m.dao.SaveMigrationMarker(ctx, failed)
	return report, cause
}

func (m *PetMigrator) resolveSourceRoot() (string, error) {
	root := strings.TrimSpace(m.sourceRoot)
	if root == "" {
		home, err := getUserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".open-cowork")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve OpenCowork source root: %w", err)
	}
	if root == "." || !filepath.IsAbs(root) {
		return "", errors.New("OpenCowork source root must be an absolute path")
	}
	return root, nil
}

func (m *PetMigrator) currentTime() int64 {
	if m != nil && m.now != nil {
		if value := m.now(); value > 0 {
			return value
		}
	}
	return time.Now().UnixMilli()
}

type petMigrationReader struct {
	root        string
	petID       string
	now         int64
	diagnostics []PetMigrationDiagnostic
	imported    int
	skipped     int
}

func (m *PetMigrator) readSnapshot(ctx context.Context, root, petID string) (PetMigrationSnapshot, []PetMigrationDiagnostic, int, int, error) {
	reader := &petMigrationReader{
		root:        root,
		petID:       petID,
		now:         m.currentTime(),
		diagnostics: make([]PetMigrationDiagnostic, 0),
	}
	snapshot := PetMigrationSnapshot{PetID: petID}

	settings, err := reader.readSettings()
	if err != nil {
		return snapshot, reader.diagnostics, reader.imported, reader.skipped, err
	}
	reader.readStateAndExperience(settings, &snapshot)
	reader.readConfigs(settings, &snapshot)
	reader.readSkins(&snapshot)
	reader.readMemories(&snapshot)
	reader.readDreamHistory(ctx, &snapshot)
	return snapshot, reader.diagnostics, reader.imported, reader.skipped, nil
}

func (r *petMigrationReader) readSettings() (map[string]any, error) {
	path := filepath.Join(r.root, petMigrationSettingsFile)
	value, err := readPetMigrationJSONFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.add(PetMigrationDiagnosticMissing, path, "", "", "旧 settings.json 不存在，保留目标默认配置")
			return map[string]any{}, nil
		}
		r.add(PetMigrationDiagnosticIO, path, "", "", err.Error())
		return map[string]any{}, nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		r.add(PetMigrationDiagnosticInvalid, path, "", "", "settings.json 顶层必须是 JSON 对象")
		return map[string]any{}, nil
	}
	return root, nil
}

func (r *petMigrationReader) readStateAndExperience(settings map[string]any, snapshot *PetMigrationSnapshot) {
	if raw, ok := settings[OpenCoworkPetStateKey]; ok {
		value, err := decodePetMigrationStore(raw)
		if err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetStateKey, "", err.Error())
		} else if state, err := r.parseState(value); err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetStateKey, "", err.Error())
		} else {
			snapshot.State = &state
			r.imported++
		}
	}
	if raw, ok := settings[OpenCoworkPetExperienceKey]; ok {
		value, err := decodePetMigrationStore(raw)
		if err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetExperienceKey, "", err.Error())
		} else if experience, logs, err := r.parseExperience(value); err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetExperienceKey, "", err.Error())
		} else {
			snapshot.Experience = &experience
			snapshot.ExpLog = append(snapshot.ExpLog, logs...)
			r.imported++
			r.imported += len(logs)
		}
	}
}

func (r *petMigrationReader) parseState(value map[string]any) (PetState, error) {
	state := DefaultPetStateAt(r.now)
	state.PetID = r.petID
	if name, ok := petMigrationString(value, "name"); ok {
		state.Name = strings.TrimSpace(name)
	}
	if number, ok := petMigrationNumber(value, "hunger"); ok {
		state.Hunger = number
	}
	if number, ok := petMigrationNumber(value, "cleanliness"); ok {
		state.Cleanliness = number
	}
	if number, ok := petMigrationNumber(value, "mood"); ok {
		state.Mood = number
	}
	if number, ok := petMigrationNumber(value, "growth"); ok {
		state.Growth = number
	}
	if number, ok := petMigrationNumber(value, "coins"); ok {
		// 旧版金币在经验兑换后允许小数，而目标契约使用整数金币；采用四舍五入，
		// 同时不把小数部分静默当成额外经验或凭据字段。
		state.Coins = petMigrationInt64(number)
	}
	if boolean, ok := petMigrationBool(value, "sleeping"); ok {
		state.Sleeping = boolean
	}
	if number, ok := petMigrationNumber(value, "sleepEndsAt"); ok {
		state.SleepEndsAt = petMigrationInt64(number)
	}
	if number, ok := petMigrationNumber(value, "lastTickAt"); ok {
		state.LastTickAt = petMigrationInt64(number)
	}
	if number, ok := petMigrationNumber(value, "adoptedAt"); ok {
		state.AdoptedAt = petMigrationInt64(number)
	}
	if number, ok := petMigrationNumber(value, "lastMilestoneDays"); ok {
		state.LastMilestoneDays = petMigrationInt64(number)
	}
	if text, ok := petMigrationString(value, "proactiveDate"); ok {
		state.ProactiveDate = text
	}
	if number, ok := petMigrationNumber(value, "proactiveCount"); ok {
		state.ProactiveCount = petMigrationInt64(number)
	}
	if number, ok := petMigrationNumber(value, "lastProactiveAt"); ok {
		state.LastProactiveAt = petMigrationInt64(number)
	}
	if number, ok := petMigrationNumber(value, "coinCreditedExp"); ok {
		state.CoinCreditedExp = number
	}
	if text, ok := petMigrationString(value, "lastDailyBonusDate"); ok {
		state.LastDailyBonusDate = text
	}
	if raw, ok := value["awayTask"]; ok && raw != nil {
		taskValue, taskOK := raw.(map[string]any)
		if !taskOK {
			return PetState{}, errors.New("awayTask 必须是对象或 null")
		}
		taskKind, kindOK := petMigrationString(taskValue, "kind")
		started, startedOK := petMigrationNumber(taskValue, "startedAt")
		ends, endsOK := petMigrationNumber(taskValue, "endsAt")
		if !kindOK || !startedOK || !endsOK || !IsPetAwayKind(PetAwayKind(taskKind)) {
			return PetState{}, errors.New("awayTask 字段无效")
		}
		state.AwayTask = &PetAwayTask{
			Kind:      PetAwayKind(taskKind),
			StartedAt: petMigrationInt64(started),
			EndsAt:    petMigrationInt64(ends),
		}
	}
	state = NormalizePetState(state, r.now)
	return state, nil
}

func (r *petMigrationReader) parseExperience(value map[string]any) (PetExperience, []PetExpLogEntry, error) {
	experience := PetExperience{PetID: r.petID}
	if number, ok := petMigrationNumber(value, "totalExp"); ok {
		experience.TotalExp = number
	}
	if number, ok := petMigrationNumber(value, "totalTokens"); ok {
		experience.TotalTokens = petMigrationInt64(number)
	}
	logs := make([]PetExpLogEntry, 0)
	rawLogs, ok := value["log"]
	if !ok || rawLogs == nil {
		return experience, logs, nil
	}
	entries, ok := rawLogs.([]any)
	if !ok {
		return PetExperience{}, nil, errors.New("经验日志必须是数组")
	}
	for index, raw := range entries {
		record, ok := raw.(map[string]any)
		if !ok {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetExperienceKey, strconv.Itoa(index), "经验日志条目必须是对象")
			r.skipped++
			continue
		}
		entry, err := r.parseExpLog(record)
		if err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetExperienceKey, strconv.Itoa(index), err.Error())
			r.skipped++
			continue
		}
		logs = append(logs, entry)
	}
	sort.SliceStable(logs, func(left, right int) bool {
		if logs[left].At != logs[right].At {
			return logs[left].At > logs[right].At
		}
		return logs[left].ID > logs[right].ID
	})
	return experience, logs, nil
}

func (r *petMigrationReader) parseExpLog(value map[string]any) (PetExpLogEntry, error) {
	id, ok := petMigrationString(value, "id")
	if !ok || strings.TrimSpace(id) == "" {
		return PetExpLogEntry{}, errors.New("经验日志缺少 id")
	}
	exp, ok := petMigrationNumber(value, "exp")
	if !ok || !isPositiveFinitePetMigrationNumber(exp) {
		return PetExpLogEntry{}, errors.New("经验日志 exp 必须是正数")
	}
	tokens := int64(0)
	if number, exists := petMigrationNumber(value, "tokens"); exists {
		if number < 0 || number > float64(math.MaxInt64) {
			return PetExpLogEntry{}, errors.New("经验日志 tokens 超出范围")
		}
		tokens = petMigrationInt64(number)
	}
	at := r.now
	if number, exists := petMigrationNumber(value, "at"); exists {
		at = petMigrationInt64(number)
	}
	premium, _ := petMigrationBool(value, "premium")
	model, _ := petMigrationString(value, "model")
	return PetExpLogEntry{
		ID:      strings.TrimSpace(id),
		PetID:   r.petID,
		At:      at,
		Model:   strings.TrimSpace(model),
		Tokens:  tokens,
		Premium: premium,
		Exp:     exp,
	}, nil
}

func (r *petMigrationReader) readConfigs(settings map[string]any, snapshot *PetMigrationSnapshot) {
	if raw, ok := settings[OpenCoworkPetCareKey]; ok {
		if value, err := decodePetMigrationStore(raw); err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetCareKey, "", err.Error())
		} else {
			config := PetCareConfig{PetID: r.petID, AutoCareThreshold: PetAutoCareDefaultThreshold}
			if boolean, exists := petMigrationBool(value, "autoCareEnabled"); exists {
				config.AutoCareEnabled = boolean
			}
			if number, exists := petMigrationNumber(value, "autoCareThreshold"); exists {
				config.AutoCareThreshold = NormalizePetCareThreshold(number)
			}
			snapshot.Care = &config
			r.imported++
		}
	}
	if raw, ok := settings[OpenCoworkPetAgentKey]; ok {
		if value, err := decodePetMigrationStore(raw); err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetAgentKey, "", err.Error())
		} else {
			config := r.parseAgent(value)
			snapshot.Agent = &config
			r.imported++
			if (config.ProviderID == nil) != (config.ModelID == nil) {
				r.add(PetMigrationDiagnosticReference, petMigrationSettingsFile, OpenCoworkPetAgentKey, "", "providerId 与 modelId 必须成对存在，未自动替换为其它模型")
			}
		}
	}
	if raw, ok := settings[OpenCoworkPetDreamConfigKey]; ok {
		if value, err := decodePetMigrationStore(raw); err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetDreamConfigKey, "", err.Error())
		} else {
			config := PetDreamConfig{
				PetID:                    r.petID,
				DreamEnabled:             true,
				SleepTalkMinLength:       PetDreamDefaultSleepTalkLength,
				BubbleMinDurationSeconds: PetDreamDefaultBubbleDurationSeconds,
			}
			if boolean, exists := petMigrationBool(value, "dreamEnabled"); exists {
				config.DreamEnabled = boolean
			}
			if text, exists := petMigrationString(value, "prompt"); exists {
				config.Prompt = text
			}
			if text, exists := petMigrationString(value, "keywords"); exists {
				config.Keywords = text
			}
			if number, exists := petMigrationNumber(value, "sleepTalkMinLength"); exists {
				config.SleepTalkMinLength = NormalizePetDreamLength(number, PetDreamDefaultSleepTalkLength, PetDreamMinSleepTalkLength, PetDreamMaxSleepTalkLength)
			}
			if number, exists := petMigrationNumber(value, "bubbleMinDurationSeconds"); exists {
				config.BubbleMinDurationSeconds = NormalizePetDreamLength(number, PetDreamDefaultBubbleDurationSeconds, PetDreamMinBubbleDurationSeconds, PetDreamMaxBubbleDurationSeconds)
			}
			snapshot.DreamConfig = &config
			r.imported++
		}
	}
	if raw, ok := settings[OpenCoworkPetEnabledKey]; ok {
		enabled, valid := petMigrationBoolValue(raw)
		if !valid {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetEnabledKey, "", "petDesktopEnabled 必须是布尔值")
		} else {
			snapshot.Window = &PetWindowConfig{PetID: r.petID, Enabled: enabled}
			r.imported++
		}
	}
	if raw, ok := settings[OpenCoworkPetSkinsKey]; ok {
		if value, err := decodePetMigrationStore(raw); err != nil {
			r.add(PetMigrationDiagnosticInvalid, petMigrationSettingsFile, OpenCoworkPetSkinsKey, "", err.Error())
		} else if active, exists := petMigrationString(value, "activeSkinId"); exists && strings.TrimSpace(active) != "" {
			active = strings.TrimSpace(active)
			snapshot.SkinSelection = &PetSkinSelection{PetID: r.petID, ActiveSkinID: &active}
			r.imported++
		} else {
			snapshot.SkinSelection = &PetSkinSelection{PetID: r.petID}
			r.imported++
		}
	}
}

func (r *petMigrationReader) parseAgent(value map[string]any) PetAgentConfig {
	config := PetAgentConfig{
		PetID:         r.petID,
		ProactiveFreq: PetProactiveLow,
		QuietStart:    22,
		QuietEnd:      9,
		VoiceMode:     PetVoiceAuto,
	}
	config.ProviderPlatform = petMigrationStringPointer(value, "providerPlatform")
	config.ProviderID = petMigrationStringPointer(value, "providerId")
	config.ModelID = petMigrationStringPointer(value, "modelId")
	config.SystemPrompt, _ = petMigrationString(value, "systemPrompt")
	config.ProjectID = petMigrationStringPointer(value, "projectId")
	config.ProjectName = petMigrationStringPointer(value, "projectName")
	config.ProjectFolder = petMigrationStringPointer(value, "projectFolder")
	config.Proactive, _ = petMigrationBool(value, "proactive")
	if text, ok := petMigrationString(value, "proactiveFreq"); ok {
		config.ProactiveFreq = PetProactiveFrequency(text)
	}
	if number, ok := petMigrationNumber(value, "quietStart"); ok {
		config.QuietStart = int(petMigrationInt64(number))
	}
	if number, ok := petMigrationNumber(value, "quietEnd"); ok {
		config.QuietEnd = int(petMigrationInt64(number))
	}
	config.VoiceEnabled, _ = petMigrationBool(value, "voiceEnabled")
	config.VoiceProviderID = petMigrationStringPointer(value, "voiceProviderId")
	config.VoiceModelID = petMigrationStringPointer(value, "voiceModelId")
	config.Voice, _ = petMigrationString(value, "voice")
	if text, ok := petMigrationString(value, "voiceMode"); ok {
		config.VoiceMode = PetVoiceMode(text)
	}
	config.VoiceInstruction, _ = petMigrationString(value, "voiceInstruction")
	config.VoiceTag, _ = petMigrationString(value, "voiceTag")
	return normalizeAgentConfig(config, r.petID)
}

func (r *petMigrationReader) readSkins(snapshot *PetMigrationSnapshot) {
	petsRoot := filepath.Join(r.root, petMigrationPetsDir)
	entries, err := os.ReadDir(petsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.add(PetMigrationDiagnosticMissing, petsRoot, "", "", "旧宠物皮肤目录不存在")
			return
		}
		r.add(PetMigrationDiagnosticIO, petsRoot, "", "", err.Error())
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "." || entry.Name() == ".." {
			continue
		}
		dir := filepath.Join(petsRoot, entry.Name())
		manifestPath := filepath.Join(dir, "pet.json")
		raw, err := readPetMigrationJSONFileBytes(manifestPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				r.add(PetMigrationDiagnosticMissing, manifestPath, "", entry.Name(), "皮肤缺少 pet.json")
			} else {
				r.add(PetMigrationDiagnosticIO, manifestPath, "", entry.Name(), err.Error())
			}
			r.skipped++
			continue
		}
		manifest, ok := raw.(map[string]any)
		if !ok {
			r.add(PetMigrationDiagnosticInvalid, manifestPath, "", entry.Name(), "pet.json 顶层必须是对象")
			r.skipped++
			continue
		}
		atlasValue, atlasOK := manifest["atlas"].(map[string]any)
		imageName, imageOK := petMigrationString(atlasValue, "image")
		if !atlasOK || !imageOK || !isSafePetMigrationFileName(imageName) {
			r.add(PetMigrationDiagnosticInvalid, manifestPath, "atlas.image", entry.Name(), "atlas.image 必须是当前皮肤目录内的文件名")
			r.skipped++
			continue
		}
		atlasPath := filepath.Join(dir, imageName)
		imageInfo, imageErr := os.Lstat(atlasPath)
		if imageErr != nil || imageInfo.Mode()&os.ModeSymlink != 0 || !imageInfo.Mode().IsRegular() {
			r.add(PetMigrationDiagnosticMissing, atlasPath, "atlas.image", entry.Name(), "皮肤 atlas 文件不存在或不是普通文件")
			r.skipped++
			continue
		}
		record := PetSkinRecord{
			PetID:     r.petID,
			SkinID:    entry.Name(),
			Name:      strings.TrimSpace(petMigrationStringDefault(manifest, "name", entry.Name())),
			Path:      dir,
			AtlasPath: atlasPath,
			Subject:   petMigrationStringDefault(manifest, "subject", ""),
			ModelID:   petMigrationStringDefault(manifest, "modelId", ""),
			Builtin:   petMigrationBoolDefault(manifest, "builtin", false),
			Atlas: PetAtlasMetadata{
				AtlasVersion: petMigrationIntDefault(atlasValue, "atlasVersion", petMigrationIntDefault(manifest, "atlasVersion", 1)),
				Image:        imageName,
				Width:        petMigrationIntDefault(atlasValue, "width", 0),
				Height:       petMigrationIntDefault(atlasValue, "height", 0),
				Anchor:       petMigrationStringDefault(atlasValue, "anchor", "bottom-center"),
				Layout:       petMigrationStringDefault(atlasValue, "layout", "action-rows"),
			},
			ManifestJSON: json.RawMessage(readPetMigrationJSONBytes(raw)),
		}
		record.CreatedAt = petMigrationInt64Pointer(manifest, "createdAt")
		record.UpdatedAt = petMigrationInt64Pointer(manifest, "updatedAt")
		record.AssetVersion = petMigrationIntPointer(manifest, "assetVersion")
		record.SpriteNormalizationVersion = petMigrationIntPointer(manifest, "spriteNormalizationVersion")
		snapshot.Skins = append(snapshot.Skins, record)
		r.imported++
	}
}

func (r *petMigrationReader) readMemories(snapshot *PetMigrationSnapshot) {
	candidates := []string{
		filepath.Join(r.root, petMigrationMemoryFile),
		filepath.Join(r.root, petMigrationPetsDir, petMigrationMemoryFile),
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			r.add(PetMigrationDiagnosticIO, path, "", "", readErr.Error())
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(string(raw), string([]byte{0xEF, 0xBB, 0xBF})))
		if text == "" {
			continue
		}
		date := petMigrationMemoryDate(text, info.ModTime())
		createdAt := info.ModTime().UnixMilli()
		if createdAt <= 0 {
			createdAt = r.now
		}
		recordID := "legacy-memory:" + filepath.Base(filepath.Dir(path))
		snapshot.Memories = append(snapshot.Memories, PetMemoryRecord{
			PetID:     r.petID,
			ID:        recordID + ":" + filepath.Base(path),
			Date:      date,
			Text:      text,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		})
		r.imported++
	}
}

func (r *petMigrationReader) readDreamHistory(ctx context.Context, snapshot *PetMigrationSnapshot) {
	dbPath := filepath.Join(r.root, petMigrationDreamDBFile)
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			r.add(PetMigrationDiagnosticMissing, dbPath, "", "", "旧梦境数据库不存在")
		} else {
			r.add(PetMigrationDiagnosticIO, dbPath, "", "", err.Error())
		}
	} else if err := r.readDreamDB(ctx, dbPath, snapshot); err != nil {
		r.add(PetMigrationDiagnosticDatabase, dbPath, "", "", err.Error())
	}
	r.readDreamArchive(snapshot)
	sort.SliceStable(snapshot.Dreams, func(left, right int) bool {
		if snapshot.Dreams[left].CreatedAt != snapshot.Dreams[right].CreatedAt {
			return snapshot.Dreams[left].CreatedAt > snapshot.Dreams[right].CreatedAt
		}
		return snapshot.Dreams[left].ID > snapshot.Dreams[right].ID
	})
}

func (r *petMigrationReader) readDreamDB(ctx context.Context, path string, snapshot *PetMigrationSnapshot) error {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	query := `
		SELECT id, created_at, title, creative_prompt, effective_prompt, keywords_json,
		       theme_id, theme_label, dream_text, sleep_talk, emotion, self_appears, image_file_name
		  FROM pet_dream_records
		 ORDER BY created_at DESC, id DESC
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	knownIDs := make(map[string]struct{}, len(snapshot.Dreams))
	for _, record := range snapshot.Dreams {
		knownIDs[record.ID] = struct{}{}
	}
	for rows.Next() {
		var (
			id, title, creativePrompt, effectivePrompt, keywordsJSON string
			themeID, themeLabel, sleepTalk, emotion, imageFileName   sql.NullString
			createdAt                                                int64
			dreamText                                                string
			selfAppears                                              sql.NullInt64
		)
		if err := rows.Scan(&id, &createdAt, &title, &creativePrompt, &effectivePrompt, &keywordsJSON,
			&themeID, &themeLabel, &dreamText, &sleepTalk, &emotion, &selfAppears, &imageFileName); err != nil {
			return err
		}
		if strings.TrimSpace(id) == "" || strings.TrimSpace(dreamText) == "" {
			r.add(PetMigrationDiagnosticInvalid, path, "", id, "梦境记录缺少 id 或 dream_text")
			r.skipped++
			continue
		}
		keywords, err := parsePetMigrationKeywords(keywordsJSON)
		if err != nil {
			r.add(PetMigrationDiagnosticInvalid, path, "keywords_json", id, err.Error())
			r.skipped++
			continue
		}
		record := PetDreamHistoryRecord{
			PetID:           r.petID,
			ID:              id,
			CreatedAt:       createdAt,
			Title:           title,
			CreativePrompt:  creativePrompt,
			EffectivePrompt: effectivePrompt,
			Keywords:        keywords,
			Dream:           dreamText,
			SleepTalk:       sleepTalk.String,
		}
		if themeID.Valid {
			record.ThemeID = &themeID.String
		}
		if themeLabel.Valid {
			record.ThemeLabel = &themeLabel.String
		}
		if emotion.Valid {
			value := PetDreamEmotion(emotion.String)
			if IsPetDreamEmotion(value) {
				record.Emotion = &value
			}
		}
		if selfAppears.Valid {
			value := selfAppears.Int64 != 0
			record.SelfAppears = &value
		}
		if imageFileName.Valid {
			record.ImagePath = r.resolveDreamImage(imageFileName.String, path, id)
		}
		if _, exists := knownIDs[id]; exists {
			r.skipped++
			continue
		}
		knownIDs[id] = struct{}{}
		snapshot.Dreams = append(snapshot.Dreams, record)
		r.imported++
	}
	return rows.Err()
}

func (r *petMigrationReader) readDreamArchive(snapshot *PetMigrationSnapshot) {
	archiveRoot := filepath.Join(r.root, petMigrationDreamsDir)
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			r.add(PetMigrationDiagnosticIO, archiveRoot, "", "", err.Error())
		}
		return
	}
	knownIDs := make(map[string]struct{}, len(snapshot.Dreams))
	for _, record := range snapshot.Dreams {
		knownIDs[record.ID] = struct{}{}
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".txt" {
			continue
		}
		path := filepath.Join(archiveRoot, entry.Name())
		info, err := entry.Info()
		if err != nil {
			r.add(PetMigrationDiagnosticIO, path, "", "", err.Error())
			continue
		}
		if info.Size() > 64<<10 {
			r.add(PetMigrationDiagnosticInvalid, path, "", "", "旧梦境文本超过 64 KiB")
			r.skipped++
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			r.add(PetMigrationDiagnosticIO, path, "", "", err.Error())
			continue
		}
		dream := strings.TrimSpace(strings.TrimPrefix(string(raw), string([]byte{0xEF, 0xBB, 0xBF})))
		if dream == "" {
			r.skipped++
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		id := "legacy:" + stem
		if _, exists := knownIDs[id]; exists {
			r.skipped++
			continue
		}
		createdAt := info.ModTime().UnixMilli()
		if timestamp, parseErr := strconv.ParseInt(strings.SplitN(stem, "-", 2)[0], 10, 64); parseErr == nil && timestamp > 0 {
			createdAt = timestamp
		}
		record := PetDreamHistoryRecord{
			PetID:     r.petID,
			ID:        id,
			CreatedAt: createdAt,
			Title:     derivePetDreamHistoryTitle(dream, createdAt),
			Dream:     dream,
			ImagePath: r.findArchiveImage(archiveRoot, stem),
		}
		snapshot.Dreams = append(snapshot.Dreams, record)
		knownIDs[id] = struct{}{}
		r.imported++
	}
}

func (r *petMigrationReader) resolveDreamImage(name, dbPath, recordID string) *string {
	name = strings.TrimSpace(name)
	if name == "" || !isSafePetMigrationFileName(name) || !isPetDreamImageExtension(name) {
		r.add(PetMigrationDiagnosticInvalid, dbPath, "image_file_name", recordID, "梦境图片文件名无效")
		return nil
	}
	path := filepath.Join(r.root, petMigrationDreamsDir, name)
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		r.add(PetMigrationDiagnosticMissing, path, "image_file_name", recordID, "梦境图片文件不存在或不是普通文件")
		return nil
	}
	return &path
}

func (r *petMigrationReader) findArchiveImage(root, stem string) *string {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".avif"} {
		path := filepath.Join(root, stem+ext)
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() {
			return &path
		}
	}
	return nil
}

func (r *petMigrationReader) add(kind PetMigrationDiagnosticKind, source, key, recordID, message string) {
	r.diagnostics = append(r.diagnostics, PetMigrationDiagnostic{
		Kind:     kind,
		Source:   source,
		Key:      key,
		RecordID: recordID,
		Message:  message,
	})
}

func decodePetMigrationStore(raw any) (map[string]any, error) {
	value := raw
	for attempt := 0; attempt < 3; attempt++ {
		if text, ok := value.(string); ok {
			decoded, err := decodePetMigrationJSON([]byte(strings.TrimPrefix(text, string([]byte{0xEF, 0xBB, 0xBF}))))
			if err != nil {
				return nil, fmt.Errorf("持久化 JSON 字符串解析失败: %w", err)
			}
			value = decoded
			continue
		}
		break
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("持久化值必须是对象或 JSON 对象字符串")
	}
	if state, exists := object["state"]; exists {
		if text, ok := state.(string); ok {
			decoded, err := decodePetMigrationJSON([]byte(text))
			if err != nil {
				return nil, fmt.Errorf("state 包装解析失败: %w", err)
			}
			state = decoded
		}
		if stateObject, ok := state.(map[string]any); ok {
			return stateObject, nil
		}
		return nil, errors.New("state 包装必须是对象")
	}
	return object, nil
}

func readPetMigrationJSONFile(path string) (any, error) {
	raw, err := readPetMigrationJSONFileBytes(path)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func readPetMigrationJSONFileBytes(path string) (any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file: %s", path)
	}
	if info.Size() > petMigrationMaxJSONBytes {
		return nil, fmt.Errorf("JSON file exceeds %d bytes: %s", petMigrationMaxJSONBytes, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodePetMigrationJSON(raw)
}

func decodePetMigrationJSON(raw []byte) (any, error) {
	raw = bytesTrimUTF8BOM(raw)
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("JSON 包含多余内容")
	}
	return value, nil
}

func bytesTrimUTF8BOM(raw []byte) []byte {
	return []byte(strings.TrimPrefix(string(raw), string([]byte{0xEF, 0xBB, 0xBF})))
}

func readPetMigrationJSONBytes(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func petMigrationString(value map[string]any, key string) (string, bool) {
	raw, ok := value[key]
	if !ok || raw == nil {
		return "", false
	}
	text, ok := raw.(string)
	return strings.TrimSpace(text), ok
}

func petMigrationStringPointer(value map[string]any, key string) *string {
	text, ok := petMigrationString(value, key)
	if !ok || text == "" {
		return nil
	}
	return &text
}

func petMigrationStringDefault(value map[string]any, key, fallback string) string {
	if text, ok := petMigrationString(value, key); ok && text != "" {
		return text
	}
	return fallback
}

func petMigrationNumber(value map[string]any, key string) (float64, bool) {
	raw, ok := value[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch current := raw.(type) {
	case json.Number:
		number, err := current.Float64()
		return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	case float64:
		return current, !math.IsNaN(current) && !math.IsInf(current, 0)
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(current), 64)
		return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	default:
		return 0, false
	}
}

func petMigrationBool(value map[string]any, key string) (bool, bool) {
	return petMigrationBoolValue(value[key])
}

func petMigrationBoolValue(raw any) (bool, bool) {
	value, ok := raw.(bool)
	return value, ok
}

func petMigrationInt64(number float64) int64 {
	if number >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if number <= float64(math.MinInt64) {
		return math.MinInt64
	}
	return int64(math.Round(number))
}

func petMigrationInt64Pointer(value map[string]any, key string) *int64 {
	number, ok := petMigrationNumber(value, key)
	if !ok {
		return nil
	}
	converted := petMigrationInt64(number)
	return &converted
}

func petMigrationIntPointer(value map[string]any, key string) *int {
	number, ok := petMigrationNumber(value, key)
	if !ok {
		return nil
	}
	converted := int(petMigrationInt64(number))
	return &converted
}

func petMigrationIntDefault(value map[string]any, key string, fallback int) int {
	number, ok := petMigrationNumber(value, key)
	if !ok {
		return fallback
	}
	converted := petMigrationInt64(number)
	if converted < 0 || converted > math.MaxInt32 {
		return fallback
	}
	return int(converted)
}

func petMigrationBoolDefault(value map[string]any, key string, fallback bool) bool {
	if result, ok := petMigrationBool(value, key); ok {
		return result
	}
	return fallback
}

func isPositiveFinitePetMigrationNumber(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func parsePetMigrationKeywords(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("keywords_json 不是字符串数组: %w", err)
	}
	return normalizePetDreamKeywords(values), nil
}

func petMigrationMemoryDate(text string, modified time.Time) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 12 && strings.HasPrefix(line, "- [") {
			end := strings.Index(line[3:], "]")
			if end >= 0 {
				date := line[3 : 3+end]
				if len(date) == len("2006-01-02") {
					return date
				}
			}
		}
	}
	if modified.IsZero() {
		return time.Now().Format("2006-01-02")
	}
	return modified.Format("2006-01-02")
}

func isSafePetMigrationFileName(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" &&
		filepath.Base(value) == value &&
		!strings.ContainsAny(value, "/\\") &&
		value != "." &&
		value != ".."
}

func fingerprintPetMigrationSource(root string) (string, error) {
	hash := sha256.New()
	paths := make([]string, 0)
	addPath := func(path string) error {
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				_, _ = io.WriteString(hash, "missing:"+path+"\n")
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_, _ = io.WriteString(hash, "symlink:"+path+"\n")
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		paths = append(paths, path)
		return nil
	}
	if err := addPath(filepath.Join(root, petMigrationSettingsFile)); err != nil {
		return "", err
	}
	if err := addPath(filepath.Join(root, petMigrationDreamDBFile)); err != nil {
		return "", err
	}
	for _, directory := range []string{
		filepath.Join(root, petMigrationPetsDir),
		filepath.Join(root, petMigrationDreamsDir),
	} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			return addPath(path)
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, relative+"\n")
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, "\n")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func summarizePetMigrationReport(report PetMigrationReport) PetMigrationReport {
	report.Missing = 0
	report.Failed = 0
	report.MissingReferences = 0
	for _, diagnostic := range report.Diagnostics {
		switch diagnostic.Kind {
		case PetMigrationDiagnosticMissing:
			report.Missing++
		case PetMigrationDiagnosticReference:
			report.MissingReferences++
		case PetMigrationDiagnosticInvalid, PetMigrationDiagnosticIO, PetMigrationDiagnosticDatabase:
			report.Failed++
		}
	}
	return report
}
