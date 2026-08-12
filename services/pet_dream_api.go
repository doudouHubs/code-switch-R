package services

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const petDreamImageMaxBytes = 16 << 20

// PetDreamAPIService 是 Wails 与 PetDreamService 之间的薄适配器。
// 它只负责把 petId、归档目录和后端仓储组装起来，不重复梦境归一化规则。
type PetDreamAPIService struct {
	repository      PetDreamHistoryRepository
	stateRepository PetDreamStateRepository
	archiveRoot     string
	initErr         error
}

func NewPetDreamAPIService(repository PetDreamHistoryRepository, state ...PetDreamStateRepository) *PetDreamAPIService {
	home, err := getUserHomeDir()
	service := &PetDreamAPIService{repository: repository, initErr: err}
	if err == nil {
		service.archiveRoot = defaultPetDreamArchiveRoot(home)
	}
	for _, candidate := range state {
		if candidate != nil {
			service.stateRepository = candidate
			break
		}
	}
	if service.stateRepository == nil {
		if candidate, ok := repository.(PetDreamStateRepository); ok {
			service.stateRepository = candidate
		}
	}
	return service
}

func (s *PetDreamAPIService) ListHistory(petID string, page, pageSize int) (PetDreamHistoryPage, error) {
	dreamService, err := s.serviceForPet(petID)
	if err != nil {
		return PetDreamHistoryPage{}, err
	}
	return dreamService.ListHistoryPage(page, pageSize)
}

func (s *PetDreamAPIService) SaveHistory(petID string, record PetDreamHistoryRecord) error {
	dreamService, err := s.serviceForPet(petID)
	if err != nil {
		return err
	}
	return dreamService.SaveHistory(record)
}

func (s *PetDreamAPIService) DeleteHistory(petID, id string) error {
	dreamService, err := s.serviceForPet(petID)
	if err != nil {
		return err
	}
	return dreamService.DeleteHistory(id)
}

func (s *PetDreamAPIService) ApplyEmotion(petID string, emotion PetDreamEmotion) error {
	dreamService, err := s.serviceForPet(petID)
	if err != nil {
		return err
	}
	return dreamService.ApplyEmotion(emotion)
}

// StoreImage 只接收生成器已经解码的图片字节，返回 basename；绝对路径永远不
// 进入前端协议，避免把用户目录结构暴露给桌宠窗口。
func (s *PetDreamAPIService) StoreImage(petID, mediaType string, data []byte) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > petDreamImageMaxBytes {
		return "", errors.New("梦境图片大小不在允许范围内")
	}
	extension, ok := petDreamImageExtension(mediaType)
	if !ok {
		return "", fmt.Errorf("不支持的梦境图片类型 %q", mediaType)
	}
	petID = normalizePetID(petID)
	// 源项目使用扁平的 pet-dreams 归档目录；文件名由 UUID 生成，不需要再按 petId
	// 套目录。数据库仍按 petId 隔离记录，路径层只保存 basename，避免迁移后旧图片失联。
	dir := s.archiveRoot
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建梦境图片目录失败: %w", err)
	}
	name := "dream-" + uuid.NewString() + extension
	path := filepath.Join(dir, name)
	if err := atomicWriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("保存梦境图片失败: %w", err)
	}
	return name, nil
}

// ReadImage 返回受控 data URL，避免 renderer 通过 file:// 或绝对路径读取本地文件。
func (s *PetDreamAPIService) ReadImage(petID, name string) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	petID = normalizePetID(petID)
	archiveDir := s.archiveRoot
	fileName, err := NormalizePetDreamImagePath(name, archiveDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(archiveDir, fileName)
	if !pathWithin(archiveDir, path) {
		return "", errors.New("梦境图片路径越过归档目录")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > petDreamImageMaxBytes {
		return "", errors.New("梦境图片不是受控普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mediaType := petDreamMediaType(filepath.Ext(fileName))
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (s *PetDreamAPIService) serviceForPet(petID string) (*PetDreamService, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	petID = strings.TrimSpace(petID)
	if petID == "" {
		return nil, errors.New("petId 不能为空")
	}
	return NewPetDreamServiceForPetWithArchive(
		s.repository,
		petID,
		s.archiveRoot,
		s.stateRepository,
	), nil
}

func (s *PetDreamAPIService) validate() error {
	if s == nil || s.repository == nil {
		return errors.New("梦境仓库未配置")
	}
	if s.initErr != nil {
		return fmt.Errorf("梦境资源目录不可用: %w", s.initErr)
	}
	if strings.TrimSpace(s.archiveRoot) == "" || !filepath.IsAbs(s.archiveRoot) {
		return errors.New("梦境资源目录必须是绝对路径")
	}
	return nil
}

func petDreamImageExtension(mediaType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return ".png", true
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	default:
		return "", false
	}
}

func petDreamMediaType(extension string) string {
	switch strings.ToLower(extension) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/png"
	}
}
