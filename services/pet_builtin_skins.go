package services

import (
	"encoding/json"
	"errors"
	"strings"
)

// petBuiltinSkinManifest 只抽取设置页和 atlas 选择需要的字段；动画明细仍完整保存在
// ManifestJSON 中，避免为展示列表再维护一份容易漂移的 manifest 结构。
type petBuiltinSkinManifest struct {
	Name                       string           `json:"name"`
	Subject                    string           `json:"subject"`
	ModelID                    string           `json:"modelId"`
	CreatedAt                  *int64           `json:"createdAt"`
	UpdatedAt                  *int64           `json:"updatedAt"`
	AssetVersion               *int             `json:"assetVersion"`
	SpriteNormalizationVersion *int             `json:"spriteNormalizationVersion"`
	AtlasVersion               int              `json:"atlasVersion"`
	Atlas                      PetAtlasMetadata `json:"atlas"`
}

func mergeBuiltinPetSkins(petID string, persisted []PetSkinRecord) []PetSkinRecord {
	merged := make([]PetSkinRecord, 0, len(persisted)+len(builtinPetSkinIDs))
	seenBuiltin := make(map[string]struct{}, len(builtinPetSkinIDs))
	for _, skin := range persisted {
		if !isBuiltinPetSkinID(strings.TrimSpace(skin.SkinID)) {
			merged = append(merged, skin)
			continue
		}

		// 内置 ID 是产品资源 owner 的保留空间；即使旧数据库记录了过期路径，
		// 也要优先用随包 manifest 重建记录，避免设置页把旧路径误当成当前资产。
		seenBuiltin[skin.SkinID] = struct{}{}
		if canonical, err := loadBuiltinPetSkinRecord(petID, skin.SkinID); err == nil {
			merged = append(merged, canonical)
		} else {
			// 资源源暂不可用时保留旧记录，至少不让用户的皮肤选择在一次读取失败
			// 后消失；真正渲染仍由 loadBuiltinPetAtlas fail-closed。
			skin.PetID = petID
			skin.Builtin = true
			merged = append(merged, skin)
		}
	}

	for _, skinID := range builtinPetSkinIDs {
		if _, exists := seenBuiltin[skinID]; exists {
			continue
		}
		if canonical, err := loadBuiltinPetSkinRecord(petID, skinID); err == nil {
			merged = append(merged, canonical)
		}
	}
	return merged
}

func loadBuiltinPetSkinRecord(petID, skinID string) (PetSkinRecord, error) {
	if !isBuiltinPetSkinID(skinID) {
		return PetSkinRecord{}, errors.New("内置皮肤 ID 未知")
	}
	manifestBytes, err := loadBuiltinPetManifest(skinID)
	if err != nil {
		return PetSkinRecord{}, err
	}
	var manifest petBuiltinSkinManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return PetSkinRecord{}, err
	}
	if manifest.AtlasVersion != PetAtlasVersion || manifest.Atlas.Image == "" {
		return PetSkinRecord{}, errors.New("内置皮肤 manifest 不完整")
	}
	manifest.Atlas.AtlasVersion = manifest.AtlasVersion
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = skinID
	}
	return PetSkinRecord{
		PetID:                      petID,
		SkinID:                     skinID,
		Name:                       name,
		Subject:                    strings.TrimSpace(manifest.Subject),
		ModelID:                    strings.TrimSpace(manifest.ModelID),
		CreatedAt:                  manifest.CreatedAt,
		UpdatedAt:                  manifest.UpdatedAt,
		Builtin:                    true,
		AssetVersion:               manifest.AssetVersion,
		SpriteNormalizationVersion: manifest.SpriteNormalizationVersion,
		Atlas:                      manifest.Atlas,
		ManifestJSON:               append(json.RawMessage(nil), manifestBytes...),
	}, nil
}
