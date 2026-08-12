package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	petMemoryLimit       = 100
	petMemoryFactLimit   = 120
	petMemoryPromptLimit = 30
)

var petMemoryDirectivePattern = regexp.MustCompile(`(?i)\[\[\s*(?:记住|remember)\s*[:：]\s*([^\]]+)\]\]`)

// PetMemoryRepository 是记忆服务与 SQLite DAO 之间的最小边界。
// 记忆已经迁移到目标数据库，因此运行态不再把 MEMORY.md 当作第二个事实源；
// 文件迁移只负责把旧内容导入 pet_memory 表。
type PetMemoryRepository interface {
	ListMemories(context.Context, string) ([]PetMemoryRecord, error)
	UpsertMemory(context.Context, PetMemoryRecord) error
	DeleteMemory(context.Context, string, string) error
}

var _ PetMemoryRepository = (*PetDAO)(nil)

// PetMemoryService 管理可编辑的长期记忆。服务层负责去重、容量和指令解析，
// DAO 只负责按宠物 ID 持久化记录，避免前端自行维护一份不一致的记忆列表。
type PetMemoryService struct {
	repository PetMemoryRepository
	petID      string
	now        func() time.Time
	mu         sync.Mutex
}

func NewPetMemoryService(repository PetMemoryRepository) *PetMemoryService {
	return NewPetMemoryServiceForPet(repository, DefaultPetID)
}

func NewPetMemoryServiceForPet(repository PetMemoryRepository, petID string) *PetMemoryService {
	petID = strings.TrimSpace(petID)
	if petID == "" {
		petID = DefaultPetID
	}
	return &PetMemoryService{
		repository: repository,
		petID:      petID,
		now:        time.Now,
	}
}

func (s *PetMemoryService) List() ([]PetMemoryRecord, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(context.Background())
}

// Append 从模型隐藏指令或设置页导入事实。相同文本不会重复写入，超出上限时
// 删除最旧记录，保证提示词大小可控，也避免数据库无限增长。
func (s *PetMemoryService) Append(texts []string) ([]PetMemoryRecord, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	records, err := s.listLocked(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(records))
	for _, record := range records {
		if normalized := normalizePetMemoryText(record.Text); normalized != "" {
			known[normalized] = struct{}{}
		}
	}

	now := s.currentTime()
	date := now.Format("2006-01-02")
	for _, text := range texts {
		text = normalizePetMemoryText(text)
		if text == "" {
			continue
		}
		if _, exists := known[text]; exists {
			continue
		}
		known[text] = struct{}{}
		records = append(records, PetMemoryRecord{
			PetID:     s.petID,
			ID:        "pet-memory:" + uuid.NewString(),
			Date:      date,
			Text:      text,
			CreatedAt: now.UnixMilli(),
			UpdatedAt: now.UnixMilli(),
		})
	}

	if len(records) > petMemoryLimit {
		records = records[len(records)-petMemoryLimit:]
	}
	for _, record := range records {
		if err := s.repository.UpsertMemory(ctx, record); err != nil {
			return nil, fmt.Errorf("保存宠物记忆失败: %w", err)
		}
	}
	return clonePetMemoryRecords(records), nil
}

func (s *PetMemoryService) Remove(id string) error {
	if err := s.validate(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("记忆 ID 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repository.DeleteMemory(context.Background(), s.petID, id); err != nil {
		return fmt.Errorf("删除宠物记忆失败: %w", err)
	}
	return nil
}

func (s *PetMemoryService) Clear() error {
	if err := s.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.listLocked(context.Background())
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := s.repository.DeleteMemory(context.Background(), s.petID, record.ID); err != nil {
			return fmt.Errorf("清空宠物记忆失败: %w", err)
		}
	}
	return nil
}

func BuildPetMemorySection(records []PetMemoryRecord) string {
	start := 0
	if len(records) > petMemoryPromptLimit {
		start = len(records) - petMemoryPromptLimit
	}
	lines := make([]string, 0, len(records)-start)
	for _, record := range records[start:] {
		text := normalizePetMemoryText(record.Text)
		if text == "" {
			continue
		}
		date := strings.TrimSpace(record.Date)
		if date == "" {
			date = "未知日期"
		}
		lines = append(lines, "- ["+date+"] "+text)
	}
	if len(lines) == 0 {
		lines = append(lines, "（还没有任何记忆）")
	}
	return strings.Join([]string{
		"【长期记忆】关于主人，你记得：",
		strings.Join(lines, "\n"),
		"",
		"【记忆规则】只有值得长期记住的称呼、喜好、习惯、正在做的事或重要约定才记录；在回复最后另起一行追加 [[记住: 一句话概括，不超过 40 字]]。这行不会显示给主人。",
	}, "\n")
}

// ExtractPetMemoryDirectives 从模型最终文本中提取隐藏记忆指令。
func ExtractPetMemoryDirectives(text string) []string {
	matches := petMemoryDirectivePattern.FindAllStringSubmatch(text, -1)
	values := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		value := normalizePetMemoryText(match[1])
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

// StripPetMemoryDirectives 用于气泡展示和流式 delta。未闭合的尾部指令也会被
// 删除，避免模型还没输出完时把内部协议短暂显示给用户。
func StripPetMemoryDirectives(text string) string {
	text = petMemoryDirectivePattern.ReplaceAllString(text, "")
	if index := strings.LastIndex(text, "[["); index >= 0 {
		if !strings.Contains(text[index:], "]]") {
			text = text[:index]
		}
	}
	return strings.TrimSpace(text)
}

func (s *PetMemoryService) listLocked(ctx context.Context) ([]PetMemoryRecord, error) {
	records, err := s.repository.ListMemories(ctx, s.petID)
	if err != nil {
		return nil, fmt.Errorf("读取宠物记忆失败: %w", err)
	}
	return clonePetMemoryRecords(records), nil
}

func (s *PetMemoryService) validate() error {
	if s == nil || s.repository == nil {
		return errors.New("宠物记忆仓库未配置")
	}
	if strings.TrimSpace(s.petID) == "" {
		return errors.New("宠物 ID 为空")
	}
	return nil
}

func (s *PetMemoryService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func normalizePetMemoryText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) > petMemoryFactLimit {
		value = string([]rune(value)[:petMemoryFactLimit])
	}
	return strings.TrimSpace(value)
}

func clonePetMemoryRecords(records []PetMemoryRecord) []PetMemoryRecord {
	result := make([]PetMemoryRecord, len(records))
	copy(result, records)
	return result
}
