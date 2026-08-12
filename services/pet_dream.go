package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	petDreamHistoryDefaultPage = 1
)

// PetDreamHistoryRepository 是梦境历史服务需要的最小历史存储边界。
// DAO 可以继续拥有更多宠物表操作，但 Dream Agent 不依赖那些无关能力。
type PetDreamHistoryRepository interface {
	UpsertDreamHistory(context.Context, PetDreamHistoryRecord) error
	ListDreamHistory(context.Context, string) ([]PetDreamHistoryRecord, error)
	DeleteDreamHistory(context.Context, string, string) error
}

// PetDreamStateRepository 是情绪协调所需的最小宠物状态存储边界。
// 将它与历史仓储拆开，测试历史分页时不必伪造整套宠物 DAO。
type PetDreamStateRepository interface {
	LoadSnapshot(context.Context, string) (PetMigrationSnapshot, error)
	SaveSnapshot(context.Context, PetMigrationSnapshot) error
}

// PetDreamRepository 方便真实 DAO 一次性满足历史和情绪两条窄边界。
type PetDreamRepository interface {
	PetDreamHistoryRepository
	PetDreamStateRepository
}

// PetDreamErrorCode 是梦境服务的稳定错误分类；调用方应按 Code 分支，
// 不要依赖中文 Message 文案。
type PetDreamErrorCode string

const (
	PetDreamErrorInvalidService PetDreamErrorCode = "invalid_service"
	PetDreamErrorInvalidDream   PetDreamErrorCode = "invalid_dream"
	PetDreamErrorInvalidEmotion PetDreamErrorCode = "invalid_emotion"
	PetDreamErrorInvalidImage   PetDreamErrorCode = "invalid_image_path"
	PetDreamErrorInvalidID      PetDreamErrorCode = "invalid_id"
	PetDreamErrorMissingState   PetDreamErrorCode = "missing_state"
	PetDreamErrorRepository     PetDreamErrorCode = "repository_error"
)

// PetDreamError 是可被 errors.As 检查的结构化错误。
// cause 只用于保留底层仓储错误的 errors.Is 能力，不改变对外稳定的 Code。
type PetDreamError struct {
	Code    PetDreamErrorCode `json:"code"`
	Field   string            `json:"field,omitempty"`
	Path    string            `json:"path,omitempty"`
	Message string            `json:"message"`
	cause   error
}

// PetDreamValidationError 是兼容调用方语义的别名；历史服务的输入错误和
// 路径错误都使用同一份结构，避免再维护一套几乎相同的错误类型。
type PetDreamValidationError = PetDreamError

func (e *PetDreamError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Field != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.Field, e.Message)
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *PetDreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is 让 errors.Is 可以按稳定错误码判断，不要求调用方匹配具体文案。
func (e *PetDreamError) Is(target error) bool {
	other, ok := target.(*PetDreamError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

// PetDreamErrorCodeOf 返回错误链中最外层梦境错误的稳定分类。
func PetDreamErrorCodeOf(err error) PetDreamErrorCode {
	var dreamErr *PetDreamError
	if errors.As(err, &dreamErr) && dreamErr != nil {
		return dreamErr.Code
	}
	return ""
}

// IsPetDreamErrorCode 判断错误是否属于指定梦境错误分类。
func IsPetDreamErrorCode(err error, code PetDreamErrorCode) bool {
	return PetDreamErrorCodeOf(err) == code
}

// PetDreamService 负责梦境历史的输入归一化、分页和睡眠情绪协调。
// 锁只保护同一个服务实例内的读改写顺序，跨进程一致性仍由 DAO 事务负责。
type PetDreamService struct {
	repository      PetDreamHistoryRepository
	stateRepository PetDreamStateRepository
	petID           string
	archiveDir      string
	now             func() int64
	mu              sync.Mutex
}

// NewPetDreamService 创建默认宠物的梦境服务。state 参数可省略；当历史仓储
// 自身也实现 PetDreamStateRepository（例如 PetDAO）时，会自动复用它。
func NewPetDreamService(repository PetDreamHistoryRepository, state ...PetDreamStateRepository) *PetDreamService {
	return newPetDreamService(repository, DefaultPetID, "", state...)
}

// NewPetDreamServiceForPet 为多宠物调用方提供显式宠物 ID 入口。
func NewPetDreamServiceForPet(repository PetDreamHistoryRepository, petID string, state ...PetDreamStateRepository) *PetDreamService {
	return newPetDreamService(repository, petID, "", state...)
}

// NewPetDreamServiceWithArchive 为需要校验图片归档目录的调用方提供构造入口。
func NewPetDreamServiceWithArchive(repository PetDreamHistoryRepository, archiveDir string, state ...PetDreamStateRepository) *PetDreamService {
	return newPetDreamService(repository, DefaultPetID, archiveDir, state...)
}

// NewPetDreamServiceForPetWithArchive 同时配置宠物 ID 和图片归档目录。
func NewPetDreamServiceForPetWithArchive(repository PetDreamHistoryRepository, petID, archiveDir string, state ...PetDreamStateRepository) *PetDreamService {
	return newPetDreamService(repository, petID, archiveDir, state...)
}

func newPetDreamService(repository PetDreamHistoryRepository, petID, archiveDir string, state ...PetDreamStateRepository) *PetDreamService {
	petID = strings.TrimSpace(petID)
	if petID == "" {
		petID = DefaultPetID
	}
	var stateRepository PetDreamStateRepository
	for _, candidate := range state {
		if candidate != nil {
			stateRepository = candidate
			break
		}
	}
	if stateRepository == nil {
		if candidate, ok := repository.(PetDreamStateRepository); ok {
			stateRepository = candidate
		}
	}
	return &PetDreamService{
		repository:      repository,
		stateRepository: stateRepository,
		petID:           petID,
		archiveDir:      strings.TrimSpace(archiveDir),
		now:             func() int64 { return time.Now().UnixMilli() },
	}
}

// SaveHistory 归一化并保存一条梦境。写入口必须拒绝空梦境；旧记录的兼容性
// 只放在读取路径，避免新的脏数据继续污染归档。
func (s *PetDreamService) SaveHistory(record PetDreamHistoryRecord) error {
	if err := s.validateHistoryService(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized, err := normalizePetDreamHistoryForSave(record, s.petID, s.currentTime(), s.archiveDir)
	if err != nil {
		return err
	}
	if err := s.repository.UpsertDreamHistory(context.Background(), normalized); err != nil {
		return newPetDreamRepositoryError("保存梦境历史失败", err)
	}
	return nil
}

// ListHistoryPage 按 createdAt DESC、id DESC 返回分页结果，排序规则与源 DAO
// 的 SQL ORDER BY 一致。pageSize 为 0 表示未传值，使用源端默认 20；负数
// 仍按源端的 Math.max(1, ...) 收敛到 1，正数最大限制为 50。
func (s *PetDreamService) ListHistoryPage(page, pageSize int) (PetDreamHistoryPage, error) {
	if err := s.validateHistoryService(); err != nil {
		return PetDreamHistoryPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	page, pageSize = NormalizePetDreamHistoryPage(page, pageSize)
	rows, err := s.repository.ListDreamHistory(context.Background(), s.petID)
	if err != nil {
		return PetDreamHistoryPage{}, newPetDreamRepositoryError("读取梦境历史失败", err)
	}
	records := make([]PetDreamHistoryRecord, 0, len(rows))
	for _, row := range rows {
		normalized, normalizeErr := normalizePetDreamHistoryForList(row, s.petID, s.archiveDir)
		if normalizeErr != nil {
			return PetDreamHistoryPage{}, normalizeErr
		}
		records = append(records, normalized)
	}

	// 仓储当前返回 SQL 排序，但服务层仍固定排序，保证 fake、旧 DAO 或未来
	// 其它存储实现不会改变分页顺序；先排序再切页也避免跨页重复或遗漏。
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].CreatedAt != records[j].CreatedAt {
			return records[i].CreatedAt > records[j].CreatedAt
		}
		return records[i].ID > records[j].ID
	})

	total := len(records)
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	result := PetDreamHistoryPage{
		Records:     make([]PetDreamHistoryRecord, 0),
		Page:        page,
		PageSize:    pageSize,
		Total:       total,
		TotalPages:  totalPages,
		HasNext:     page < totalPages,
		HasPrevious: page > 1 && totalPages > 0,
	}
	// 页码可能远大于总页数，不能先计算 (page-1)*pageSize，避免 int 溢出。
	if page <= totalPages {
		start := (page - 1) * pageSize
		end := start + pageSize
		if end > total {
			end = total
		}
		result.Records = append(result.Records, records[start:end]...)
	}
	return result, nil
}

// DeleteHistory 删除当前宠物的一条梦境。空 ID 在进入仓储前直接拒绝，避免
// 把“删除全部”之类的语义错误下沉到 DAO 查询层。
func (s *PetDreamService) DeleteHistory(id string) error {
	if err := s.validateHistoryService(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return newPetDreamValidationError(PetDreamErrorInvalidID, "id", "梦境 ID 不能为空", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repository.DeleteDreamHistory(context.Background(), s.petID, id); err != nil {
		return newPetDreamRepositoryError("删除梦境历史失败", err)
	}
	return nil
}

// ApplyEmotion 只在宠物当前处于睡眠状态时调用规则层并持久化快照。
// 清醒状态直接返回，不写库，避免梦境回放意外修改白天心情或覆盖并发状态。
func (s *PetDreamService) ApplyEmotion(emotion PetDreamEmotion) error {
	if err := s.validateHistoryService(); err != nil {
		return err
	}
	if !IsPetDreamEmotion(emotion) {
		return newPetDreamValidationError(PetDreamErrorInvalidEmotion, "emotion", "梦境情绪不在允许范围内", nil)
	}
	if s.stateRepository == nil {
		return newPetDreamValidationError(PetDreamErrorMissingState, "state", "梦境情绪缺少宠物状态仓储", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	snapshot, err := s.stateRepository.LoadSnapshot(ctx, s.petID)
	if err != nil {
		return newPetDreamRepositoryError("读取宠物状态失败", err)
	}
	if snapshot.State == nil {
		return newPetDreamValidationError(PetDreamErrorMissingState, "state", "宠物快照缺少 state", nil)
	}
	if !snapshot.State.Sleeping {
		return nil
	}

	// 睡眠门禁必须在调用规则函数之前判断；PetApplyDreamEmotion 自身也有
	// 防御判断，但这里显式保留协调层的业务边界，便于保证清醒路径零写入。
	next := PetApplyDreamEmotion(*snapshot.State, emotion)
	snapshot.PetID = s.petID
	snapshot.State = &next
	if err := s.stateRepository.SaveSnapshot(ctx, snapshot); err != nil {
		return newPetDreamRepositoryError("保存梦境情绪失败", err)
	}
	return nil
}

// NormalizePetDreamHistoryRecord 对外提供与 SaveHistory 相同的字段归一化，
// 便于非服务调用方在进入其它持久化边界前复用规则。图片字段仍需提供归档目录
// 时才能进行完整的路径校验，因此推荐写入直接走 SaveHistory。
func NormalizePetDreamHistoryRecord(record PetDreamHistoryRecord, petID string) (PetDreamHistoryRecord, error) {
	return normalizePetDreamHistoryForSave(record, petID, time.Now().UnixMilli(), "")
}

// NormalizePetDreamHistoryPage 收敛分页边界，与 renderer IPC 请求归一化保持一致。
func NormalizePetDreamHistoryPage(page, pageSize int) (int, int) {
	if page < petDreamHistoryDefaultPage {
		page = petDreamHistoryDefaultPage
	}
	if pageSize == 0 {
		pageSize = PetDreamHistoryPageSize
	} else if pageSize < 0 {
		pageSize = 1
	} else if pageSize > PetDreamHistoryMaxPageSize {
		pageSize = PetDreamHistoryMaxPageSize
	}
	return page, pageSize
}

// NormalizePetDreamImagePath 只返回可持久化的 basename。裸 basename 不需要
// 访问磁盘；绝对路径则必须位于 archive 根目录的第一层，且不能含父目录段。
func NormalizePetDreamImagePath(value, archiveDir string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsRune(value, '\x00') || hasPetDreamParentPathSegment(value) {
		return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片路径包含非法穿越片段", nil)
	}

	// 源端最终只持久化 archive 内的文件名；直接传 basename 是最常见的
	// 入口，也避免在 archive 尚未创建时为了校验而产生无意义的文件系统依赖。
	if isBareFileName(value) {
		if !isPetDreamImageExtension(value) {
			return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片扩展名不受支持", nil)
		}
		return filepath.Base(value), nil
	}

	if archiveDir == "" || !filepath.IsAbs(value) {
		return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片必须是 archive 内 basename", nil)
	}
	archiveRoot, err := filepath.Abs(archiveDir)
	if err != nil {
		return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片归档目录无效", err)
	}
	resolvedPath, err := filepath.Abs(value)
	if err != nil {
		return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片路径无效", err)
	}
	if !isSingleArchiveFile(archiveRoot, resolvedPath) {
		return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片必须位于 archive 第一层", nil)
	}
	base := filepath.Base(resolvedPath)
	if !isPetDreamImageExtension(base) {
		return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片扩展名不受支持", nil)
	}

	// 文件已存在时再检查真实路径，拦截 archive 内符号链接指向外部的情况；
	// 不存在的目标仍可保存，因为图片可能在记录落库后才完成生成。
	if realPath, realErr := filepath.EvalSymlinks(resolvedPath); realErr == nil {
		realRoot := archiveRoot
		if evaluatedRoot, rootErr := filepath.EvalSymlinks(archiveRoot); rootErr == nil {
			realRoot = evaluatedRoot
		}
		if !isSingleArchiveFile(realRoot, realPath) {
			return "", newPetDreamValidationError(PetDreamErrorInvalidImage, "imagePath", "梦境图片真实路径越出 archive", nil)
		}
	}
	return base, nil
}

// NormalizePetDreamImageFileName 是与源 DAO 命名一致的便捷别名。
func NormalizePetDreamImageFileName(value, archiveDir string) (string, error) {
	return NormalizePetDreamImagePath(value, archiveDir)
}

func (s *PetDreamService) validateHistoryService() error {
	if s == nil {
		return newPetDreamValidationError(PetDreamErrorInvalidService, "service", "梦境服务为空", nil)
	}
	if s.repository == nil {
		return newPetDreamValidationError(PetDreamErrorInvalidService, "repository", "梦境仓储未配置", nil)
	}
	if strings.TrimSpace(s.petID) == "" {
		return newPetDreamValidationError(PetDreamErrorInvalidService, "petId", "宠物 ID 为空", nil)
	}
	return nil
}

func (s *PetDreamService) currentTime() int64 {
	now := int64(0)
	if s != nil && s.now != nil {
		now = s.now()
	}
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	return now
}

func normalizePetDreamHistoryForSave(record PetDreamHistoryRecord, petID string, now int64, archiveDir string) (PetDreamHistoryRecord, error) {
	petID = strings.TrimSpace(petID)
	if petID == "" {
		petID = DefaultPetID
	}
	now = normalizePetDreamTime(now)
	record.PetID = petID
	record.ID = strings.TrimSpace(record.ID)
	if record.ID == "" {
		record.ID = newPetDreamHistoryID(now)
	}
	record.Dream = strings.TrimSpace(record.Dream)
	if record.Dream == "" {
		return PetDreamHistoryRecord{}, newPetDreamValidationError(PetDreamErrorInvalidDream, "dream", "梦境内容不能为空", nil)
	}
	record.SleepTalk = strings.TrimSpace(record.SleepTalk)
	if record.CreatedAt <= 0 {
		record.CreatedAt = now
	}
	record.Title = normalizePetDreamHistoryTitle(record.Title, record.Dream, record.CreatedAt)
	normalizePetDreamHistoryFields(&record)
	if record.ImagePath != nil {
		imageName, err := NormalizePetDreamImagePath(*record.ImagePath, archiveDir)
		if err != nil {
			return PetDreamHistoryRecord{}, err
		}
		if imageName == "" {
			record.ImagePath = nil
		} else {
			record.ImagePath = stringPointer(imageName)
		}
	}
	return record, nil
}

func normalizePetDreamHistoryForList(record PetDreamHistoryRecord, petID, archiveDir string) (PetDreamHistoryRecord, error) {
	record.PetID = petID
	record.ID = strings.TrimSpace(record.ID)
	record.Dream = strings.TrimSpace(record.Dream)
	record.SleepTalk = strings.TrimSpace(record.SleepTalk)
	record.Title = normalizePetDreamHistoryTitle(record.Title, record.Dream, record.CreatedAt)
	normalizePetDreamHistoryFields(&record)
	if record.ImagePath != nil {
		imageName, err := NormalizePetDreamImagePath(*record.ImagePath, archiveDir)
		if err != nil {
			// 历史中的图片可能已被删除或来自旧版本；像源端 resolve 一样
			// 对不安全字段采取 fail-closed，保留文字历史而不是暴露危险路径。
			record.ImagePath = nil
		} else if imageName == "" {
			record.ImagePath = nil
		} else {
			record.ImagePath = stringPointer(imageName)
		}
	}
	return record, nil
}

func normalizePetDreamHistoryFields(record *PetDreamHistoryRecord) {
	if record == nil {
		return
	}
	record.Keywords = normalizePetDreamKeywords(record.Keywords)
	if record.Emotion != nil {
		emotion := *record.Emotion
		if !IsPetDreamEmotion(emotion) {
			record.Emotion = nil
		} else {
			record.Emotion = &emotion
		}
	}
	record.ThemeID = normalizeOptionalText(record.ThemeID)
	record.ThemeLabel = normalizeOptionalText(record.ThemeLabel)
	if record.SelfAppears != nil {
		selfAppears := *record.SelfAppears
		record.SelfAppears = &selfAppears
	}
	if record.ImagePath != nil {
		imagePath := strings.TrimSpace(*record.ImagePath)
		if imagePath == "" {
			record.ImagePath = nil
		} else {
			record.ImagePath = &imagePath
		}
	}
}

func normalizePetDreamKeywords(values []string) []string {
	keywords := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		keywords = append(keywords, value)
	}
	return keywords
}

func normalizePetDreamHistoryTitle(value, dream string, createdAt int64) string {
	title := strings.TrimSpace(value)
	if title != "" && utf8.RuneCountInString(title) <= PetDreamHistoryTitleMaxLength {
		return title
	}
	return derivePetDreamHistoryTitle(dream, createdAt)
}

// derivePetDreamHistoryTitle 与 shared/pet-dream-history.ts 保持同一确定性规则：
// 先压缩空白、取首句，再按 Unicode code point 截断，避免中文或 emoji 被拆开。
func derivePetDreamHistoryTitle(dream string, createdAt int64) string {
	normalized := strings.Join(strings.Fields(dream), " ")
	firstSentence := normalized
	if index := strings.IndexFunc(normalized, isPetDreamSentenceEnd); index >= 0 {
		firstSentence = normalized[:index]
	}
	firstSentence = strings.TrimSpace(firstSentence)
	if firstSentence != "" {
		runes := []rune(firstSentence)
		if len(runes) > PetDreamHistoryTitleMaxLength {
			runes = runes[:PetDreamHistoryTitleMaxLength]
		}
		return string(runes)
	}
	return time.UnixMilli(createdAt).UTC().Format("2006-01-02")
}

func isPetDreamSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '.', '!', '?', '\n':
		return true
	default:
		return false
	}
}

func normalizeOptionalText(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func newPetDreamHistoryID(now int64) string {
	return "pet-dream:" + strconv.FormatInt(now, 36) + ":" + uuid.NewString()
}

func normalizePetDreamTime(now int64) int64 {
	if now <= 0 {
		return time.Now().UnixMilli()
	}
	return now
}

func stringPointer(value string) *string {
	return &value
}

func newPetDreamValidationError(code PetDreamErrorCode, field, message string, cause error) *PetDreamError {
	return &PetDreamError{Code: code, Field: field, Path: field, Message: message, cause: cause}
}

func newPetDreamRepositoryError(message string, cause error) *PetDreamError {
	return &PetDreamError{Code: PetDreamErrorRepository, Message: message, cause: cause}
}

func isBareFileName(value string) bool {
	return filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

func isPetDreamImageExtension(value string) bool {
	switch strings.ToLower(filepath.Ext(value)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".avif":
		return true
	default:
		return false
	}
}

func hasPetDreamParentPathSegment(value string) bool {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' })
	for _, part := range parts {
		if part == ".." {
			return true
		}
	}
	return false
}

func isSingleArchiveFile(archiveRoot, candidate string) bool {
	relativePath, err := filepath.Rel(archiveRoot, candidate)
	if err != nil || relativePath == "." || relativePath == "" || filepath.IsAbs(relativePath) {
		return false
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return false
	}
	return !strings.ContainsAny(relativePath, `/\\`)
}
