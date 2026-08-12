package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// 图片请求沿用 PetAIError 的安全投影，调用方按 code 分支，不读取上游正文。
// 这些错误码放在图片切片中，避免修改已有 pet_ai.go 的共享错误契约。
const (
	PET_IMAGE_INVALID_REQUEST        PetAIErrorCode = "PET_IMAGE_INVALID_REQUEST"
	PET_IMAGE_DEPENDENCY_UNAVAILABLE PetAIErrorCode = "PET_IMAGE_DEPENDENCY_UNAVAILABLE"
	PET_IMAGE_REQUEST_CANCELLED      PetAIErrorCode = "PET_IMAGE_REQUEST_CANCELLED"
	PET_IMAGE_TIMEOUT                PetAIErrorCode = "PET_IMAGE_TIMEOUT"
	PET_IMAGE_REQUEST_TOO_LARGE      PetAIErrorCode = "PET_IMAGE_REQUEST_TOO_LARGE"
	PET_IMAGE_RESPONSE_TOO_LARGE     PetAIErrorCode = "PET_IMAGE_RESPONSE_TOO_LARGE"
	PET_IMAGE_RESPONSE_INVALID       PetAIErrorCode = "PET_IMAGE_RESPONSE_INVALID"
	PET_IMAGE_REMOTE_URL_UNSUPPORTED PetAIErrorCode = "PET_IMAGE_REMOTE_URL_UNSUPPORTED"
	PET_IMAGE_UPSTREAM_ERROR         PetAIErrorCode = "PET_IMAGE_UPSTREAM_ERROR"
)

const (
	PetImageDefaultTimeout                    = PetAIDefaultTimeout
	PetImageDefaultSize                       = "1024x1024"
	PetImageMaxPromptLength                   = 8 << 10
	PetImageMaxCount                          = 4
	PetImageMaxSizeLength                     = 32
	PetImageMaxDimension                      = 4096
	PetImageDefaultMaxRequestBytes      int64 = 64 << 10
	PetImageDefaultMaxResponseBytes     int64 = 32 << 20
	PetImageDefaultMaxImageBytes        int64 = 16 << 20
	PetImageDefaultMaxMultipartBytes    int64 = PetImageDefaultMaxImageBytes + 64<<10
	PetImageMaxReferencePathLength            = 4 << 10
	PetImageMaxReferenceDimension             = 1024
	PetImageMaxReferenceDataURLMetadata       = 128
)

// PetImageErrorCodeOf 与 PetAIErrorCodeOf 保持相同的调用方式，同时保留已有
// provider 错误族（例如 PET_CAPABILITY_UNSUPPORTED）的稳定码。
func PetImageErrorCodeOf(err error) string {
	return PetAIErrorCodeOf(err)
}

// PetImageRequest 是梦境/聊天图片生成的窄协议。provider 只允许携带引用，
// 不允许调用方把 API key 或完整 provider 配置塞进请求。
type PetImageRequest struct {
	PetID          string               `json:"petId"`
	RequestID      string               `json:"requestId"`
	Provider       PetProviderReference `json:"provider"`
	Prompt         string               `json:"prompt"`
	Size           string               `json:"size,omitempty"`
	Count          int                  `json:"count,omitempty"`
	ReferenceImage *PetImageReference   `json:"referenceImage,omitempty"`
}

// PetImageReference 只允许当前皮肤的 idle 首帧作为身份参考。
// Data 是优先入口，承载已经裁出的单帧 bare base64 或 data URL；Path/SkinPath
// 是旧版受控本地路径协议，仅在 Data 为空时使用。两种入口禁止混用，避免调用方
// 通过另一套字段绕过当前入口的校验或把整张 atlas 当作身份图。
type PetImageReference struct {
	Path       string `json:"path"`
	SkinPath   string `json:"skinPath"`
	Data       string `json:"data,omitempty"`
	MediaType  string `json:"mediaType"`
	Pose       string `json:"pose,omitempty"`
	FrameIndex int    `json:"frameIndex,omitempty"`
}

// PetImageResult 只返回已在本地解码并验证过的图片字节；Wails 会把 []byte
// 编码为 JSON base64，前端不需要、也不能再根据远程 URL 发起第二跳请求。
type PetImageResult struct {
	Images [][]byte `json:"images"`
}

// PetImageOptions 控制图片请求、响应、单张图片和尺寸边界。零值使用保守默认值，
// 负值同样回到默认值，避免调用方误把无符号语义写成“无限制”。
type PetImageOptions struct {
	Timeout           time.Duration
	MaxPromptLength   int
	MaxCount          int
	MaxRequestBytes   int64
	MaxResponseBytes  int64
	MaxImageBytes     int64
	MaxMultipartBytes int64
	MaxDimension      int
	ReferenceRoot     string
}

type PetImageDependencies struct {
	ProviderReader PetAIProviderReader
	Transport      PetAIHTTPTransport
	Options        PetImageOptions
}

// PetImageService 只负责图片生成请求边界，不持有 provider owner 状态，也不负责
// 梦境归档。凭据只存在于一次请求构造和 RoundTrip 所需的短生命周期内。
type PetImageService struct {
	providerReader PetAIProviderReader
	transport      PetAIHTTPTransport
	options        PetImageOptions
}

func NewPetImageService(providerReader PetAIProviderReader, transport PetAIHTTPTransport) *PetImageService {
	return NewPetImageServiceWithDependencies(PetImageDependencies{
		ProviderReader: providerReader,
		Transport:      transport,
	})
}

func NewPetImageServiceWithOptions(
	providerReader PetAIProviderReader,
	transport PetAIHTTPTransport,
	options PetImageOptions,
) *PetImageService {
	return NewPetImageServiceWithDependencies(PetImageDependencies{
		ProviderReader: providerReader,
		Transport:      transport,
		Options:        options,
	})
}

func NewPetImageServiceWithDependencies(deps PetImageDependencies) *PetImageService {
	return &PetImageService{
		providerReader: deps.ProviderReader,
		transport:      deps.Transport,
		options:        normalizePetImageOptions(deps.Options),
	}
}

// GenerateImage 解析 provider 后调用 OpenAI-compatible images/generations。
// 整条链路使用带超时的 context，transport 返回的取消/超时不会被压成普通上游错误。
func (s *PetImageService) GenerateImage(ctx context.Context, request PetImageRequest) (PetImageResult, error) {
	if s == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	if ctx == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}

	normalized, err := normalizePetImageRequest(request, s.options)
	if err != nil {
		return PetImageResult{}, err
	}
	if s.providerReader == nil || s.transport == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_DEPENDENCY_UNAVAILABLE, 0, nil)
	}

	provider, err := s.resolveProvider(ctx, normalized.Provider)
	if err != nil {
		return PetImageResult{}, err
	}
	if ctx.Err() != nil {
		return PetImageResult{}, classifyPetImageContextError(ctx.Err())
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	result, err := s.executeImageRequest(requestCtx, provider, normalized)
	if err != nil {
		if requestCtx.Err() != nil {
			return PetImageResult{}, classifyPetImageContextError(requestCtx.Err())
		}
		return PetImageResult{}, err
	}
	return result, nil
}

func normalizePetImageOptions(options PetImageOptions) PetImageOptions {
	if options.Timeout <= 0 {
		options.Timeout = PetImageDefaultTimeout
	}
	if options.MaxPromptLength <= 0 {
		options.MaxPromptLength = PetImageMaxPromptLength
	}
	if options.MaxCount <= 0 {
		options.MaxCount = PetImageMaxCount
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = PetImageDefaultMaxRequestBytes
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = PetImageDefaultMaxResponseBytes
	}
	if options.MaxImageBytes <= 0 {
		options.MaxImageBytes = PetImageDefaultMaxImageBytes
	}
	if options.MaxMultipartBytes <= 0 {
		options.MaxMultipartBytes = PetImageDefaultMaxMultipartBytes
	}
	if options.MaxDimension <= 0 {
		options.MaxDimension = PetImageMaxDimension
	}
	if strings.TrimSpace(options.ReferenceRoot) == "" {
		if home, err := getUserHomeDir(); err == nil {
			options.ReferenceRoot = defaultPetSkinRoot(home)
		}
	}
	if strings.TrimSpace(options.ReferenceRoot) != "" {
		options.ReferenceRoot = filepath.Clean(strings.TrimSpace(options.ReferenceRoot))
	}
	return options
}

func normalizePetImageRequest(request PetImageRequest, options PetImageOptions) (PetImageRequest, error) {
	request.PetID = strings.TrimSpace(request.PetID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.PetID == "" || runeLen(request.PetID) > PetAIMaxPetIDLength || hasLineBreak(request.PetID) {
		return PetImageRequest{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if request.RequestID == "" || runeLen(request.RequestID) > PetAIMaxRequestIDLength || hasLineBreak(request.RequestID) {
		return PetImageRequest{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if request.Prompt == "" || runeLen(request.Prompt) > options.MaxPromptLength {
		return PetImageRequest{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}

	// 图片能力必须显式声明为 image，不能像文本入口那样把空 capability 猜成默认值，
	// 否则一个配置错误就可能把聊天 provider 当成图片 provider 调用。
	request.Provider.Capability = PetCapability(strings.ToLower(strings.TrimSpace(string(request.Provider.Capability))))
	if request.Provider.Capability != PetCapabilityImage {
		return PetImageRequest{}, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			request.Provider,
			"图片请求只支持 image capability",
			nil,
		)
	}
	provider, err := normalizePetAIReference(request.Provider, PetCapabilityImage)
	if err != nil {
		return PetImageRequest{}, err
	}
	request.Provider = provider

	if request.Count == 0 {
		request.Count = 1
	}
	if request.Count < 1 || request.Count > options.MaxCount {
		return PetImageRequest{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	size, err := normalizePetImageSize(request.Size)
	if err != nil {
		return PetImageRequest{}, err
	}
	request.Size = size
	if request.ReferenceImage != nil {
		if err := validatePetImageReferenceMetadata(*request.ReferenceImage, options); err != nil {
			return PetImageRequest{}, err
		}
	}
	return request, nil
}

func validatePetImageReferenceMetadata(reference PetImageReference, options PetImageOptions) error {
	data := strings.TrimSpace(reference.Data)
	if data != "" {
		if strings.TrimSpace(reference.Path) != "" || strings.TrimSpace(reference.SkinPath) != "" {
			return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		if err := validatePetImageReferencePose(reference); err != nil {
			return err
		}
		if strings.HasPrefix(strings.ToLower(data), "http:") || strings.HasPrefix(strings.ToLower(data), "https:") {
			return newPetAIError(PET_IMAGE_REMOTE_URL_UNSUPPORTED, 0, nil)
		}
		// 这里只检查 data URL 的声明和 base64 载荷形状；图片字节仍必须在
		// readPetImageReferenceData 中完整解码、验格式、验尺寸和验大小。
		if _, _, err := decodePetImageReferenceData(data, reference.MediaType, options.MaxImageBytes); err != nil {
			return err
		}
		return nil
	}

	path := strings.TrimSpace(reference.Path)
	skinPath := strings.TrimSpace(reference.SkinPath)
	if path == "" || skinPath == "" || strings.IndexByte(path, 0) >= 0 || strings.IndexByte(skinPath, 0) >= 0 ||
		hasLineBreak(path) || hasLineBreak(skinPath) || runeLen(path) > PetImageMaxReferencePathLength ||
		runeLen(skinPath) > PetImageMaxReferencePathLength || !filepath.IsAbs(path) || !filepath.IsAbs(skinPath) {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if options.ReferenceRoot == "" || !filepath.IsAbs(options.ReferenceRoot) {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}

	root := filepath.Clean(options.ReferenceRoot)
	skinPath = filepath.Clean(skinPath)
	path = filepath.Clean(path)
	if !petImagePathWithin(root, skinPath) || !petImagePathWithin(skinPath, path) || filepath.Dir(path) != skinPath {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	// atlas 是动作全集，不是身份参考；只允许调用方提交已经裁出的 idle 单帧。
	base := strings.ToLower(filepath.Base(path))
	if base == "atlas.png" || base == "atlas.next.png" || base == "pet.json" {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if err := validatePetImageReferencePose(reference); err != nil {
		return err
	}
	if normalizePetImageMediaType(reference.MediaType) == "" {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return validatePetImageReferencePath(root, skinPath, path)
}

func validatePetImageReferencePose(reference PetImageReference) error {
	pose := strings.ToLower(strings.TrimSpace(reference.Pose))
	if pose != "" && pose != "idle" || reference.FrameIndex != 0 {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return nil
}

func petImagePathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validatePetImageReferencePath(root, skinPath, path string) error {
	// 从 skinPath 向 root 回溯检查每个目录组件；若从 root 向下迭代，无法自然
	// 到达未知深度的皮肤目录，容易把合法路径误判为越界。
	for current := filepath.Clean(skinPath); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || (parent != root && !petImagePathWithin(root, parent)) {
			return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return nil
}

func normalizePetImageMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	default:
		return ""
	}
}

func normalizePetImageSize(size string) (string, error) {
	size = strings.TrimSpace(strings.ToLower(size))
	if size == "" {
		return PetImageDefaultSize, nil
	}
	if runeLen(size) > PetImageMaxSizeLength {
		return "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if size == "auto" {
		return size, nil
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if widthErr != nil || heightErr != nil || width < 1 || height < 1 || width > PetImageMaxDimension || height > PetImageMaxDimension {
		return "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return strconv.Itoa(width) + "x" + strconv.Itoa(height), nil
}

func (s *PetImageService) resolveProvider(
	ctx context.Context,
	reference PetProviderReference,
) (petAIProviderRuntime, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	config, err := s.providerReader.Read(resolveCtx, reference)
	if err != nil {
		if resolveCtx.Err() != nil {
			return petAIProviderRuntime{}, classifyPetImageContextError(resolveCtx.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return petAIProviderRuntime{}, classifyPetImageContextError(err)
		}
		if code := PetProviderErrorCodeOf(err); code != "" {
			return petAIProviderRuntime{}, newPetProviderError(code, reference, "provider 引用解析失败", nil)
		}
		return petAIProviderRuntime{}, newPetAIError(PET_IMAGE_UPSTREAM_ERROR, 0, nil)
	}
	if resolveCtx.Err() != nil {
		return petAIProviderRuntime{}, classifyPetImageContextError(resolveCtx.Err())
	}

	// 复用 AI 层已有配置投影：这里不读取 provider 文件、不复制 API key 解析，
	// 只把同一份临时 runtime 限定为 OpenAI-compatible 图片协议。
	provider, err := normalizePetAIProviderConfig(config, reference, PetCapabilityImage)
	if err != nil {
		if PetProviderErrorCodeOf(err) != "" {
			return petAIProviderRuntime{}, err
		}
		return petAIProviderRuntime{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			reference,
			"图片 provider 配置无效",
			nil,
		)
	}
	if provider.protocol != "openai" {
		return petAIProviderRuntime{}, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			reference,
			"图片生成只支持 OpenAI-compatible protocol",
			nil,
		)
	}
	return provider, nil
}

func (s *PetImageService) executeImageRequest(
	ctx context.Context,
	provider petAIProviderRuntime,
	request PetImageRequest,
) (PetImageResult, error) {
	if request.ReferenceImage != nil {
		return s.executePetImageEdit(ctx, provider, request, *request.ReferenceImage)
	}
	return s.executePetImageGeneration(ctx, provider, request)
}

func (s *PetImageService) executePetImageGeneration(
	ctx context.Context,
	provider petAIProviderRuntime,
	request PetImageRequest,
) (PetImageResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":           provider.model,
		"prompt":          request.Prompt,
		"size":            request.Size,
		"n":               request.Count,
		"response_format": "b64_json",
	})
	if err != nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if int64(len(body)) > s.options.MaxRequestBytes {
		return PetImageResult{}, newPetAIError(PET_IMAGE_REQUEST_TOO_LARGE, 0, nil)
	}

	endpoint, err := petImageEndpoint(provider)
	if err != nil {
		return PetImageResult{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			request.Provider,
			"图片 provider endpoint 无效",
			nil,
		)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	for key, value := range provider.headers {
		httpRequest.Header.Set(key, value)
	}
	// 认证 Header 由 AI 层统一实现，图片层不复制 API key 选择和协议映射。
	applyProviderAuth(httpRequest, provider)

	response, err := s.transport.RoundTrip(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return PetImageResult{}, classifyPetImageContextError(ctx.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return PetImageResult{}, classifyPetImageContextError(err)
		}
		return PetImageResult{}, newPetAIError(PET_IMAGE_UPSTREAM_ERROR, 0, nil)
	}
	if response == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// 非 2xx 只返回固定错误码和状态，不读取/回传上游正文，避免把 provider
		// 错误文案、URL 或 API key 片段暴露到 Wails 错误链。
		return PetImageResult{}, newPetAIError(PET_IMAGE_UPSTREAM_ERROR, response.StatusCode, nil)
	}
	if response.Body == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, response.StatusCode, nil)
	}
	if err := validatePetImageResponseContentType(response.Header.Get("Content-Type")); err != nil {
		return PetImageResult{}, err
	}
	responseBody, err := readPetImageResponseBody(response.Body, s.options.MaxResponseBytes)
	if err != nil {
		if ctx.Err() != nil {
			return PetImageResult{}, classifyPetImageContextError(ctx.Err())
		}
		return PetImageResult{}, err
	}
	return parsePetImageResponse(responseBody, request.Count, s.options)
}

type petImageReferencePayload struct {
	Bytes     []byte
	MediaType string
}

func (s *PetImageService) executePetImageEdit(
	ctx context.Context,
	provider petAIProviderRuntime,
	request PetImageRequest,
	reference PetImageReference,
) (PetImageResult, error) {
	payload, err := readPetImageReference(reference, s.options)
	if err != nil {
		return PetImageResult{}, err
	}

	endpoint, err := petImageEditEndpoint(provider)
	if err != nil {
		return PetImageResult{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			request.Provider,
			"图片 edit provider endpoint 无效",
			nil,
		)
	}
	body, contentType, err := buildPetImageEditMultipart(provider, request, payload, s.options.MaxMultipartBytes)
	if err != nil {
		return PetImageResult{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	httpRequest.ContentLength = int64(len(body))
	httpRequest.Header.Set("Content-Type", contentType)
	httpRequest.Header.Set("Accept", "application/json")
	for key, value := range provider.headers {
		httpRequest.Header.Set(key, value)
	}
	applyProviderAuth(httpRequest, provider)
	return s.parsePetImageHTTPResponse(ctx, httpRequest, request.Count)
}

func readPetImageReference(reference PetImageReference, options PetImageOptions) (petImageReferencePayload, error) {
	if err := validatePetImageReferenceMetadata(reference, options); err != nil {
		return petImageReferencePayload{}, err
	}
	if strings.TrimSpace(reference.Data) != "" {
		return readPetImageReferenceData(reference, options)
	}
	file, err := os.Open(filepath.Clean(reference.Path))
	if err != nil {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, options.MaxImageBytes+1))
	if err != nil {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if int64(len(data)) > options.MaxImageBytes {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_REQUEST_TOO_LARGE, 0, nil)
	}
	format, err := validatePetImageBytesAndFormat(data, options.MaxImageBytes, options.MaxDimension)
	if err != nil {
		if PetImageErrorCodeOf(err) == string(PET_IMAGE_RESPONSE_INVALID) {
			return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		return petImageReferencePayload{}, err
	}
	declared := normalizePetImageMediaType(reference.MediaType)
	actual := petImageFormatMediaType(format)
	if declared == "" || actual == "" || declared != actual {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return petImageReferencePayload{Bytes: data, MediaType: declared}, nil
}

func readPetImageReferenceData(reference PetImageReference, options PetImageOptions) (petImageReferencePayload, error) {
	data, declared, err := decodePetImageReferenceData(strings.TrimSpace(reference.Data), reference.MediaType, options.MaxImageBytes)
	if err != nil {
		return petImageReferencePayload{}, err
	}
	imageBytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if int64(len(imageBytes)) > options.MaxImageBytes {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_REQUEST_TOO_LARGE, 0, nil)
	}
	format, err := validatePetImageBytesAndFormat(imageBytes, options.MaxImageBytes, minPetImageReferenceDimension(options.MaxDimension))
	if err != nil {
		if PetImageErrorCodeOf(err) == string(PET_IMAGE_RESPONSE_TOO_LARGE) {
			return petImageReferencePayload{}, newPetAIError(PET_IMAGE_REQUEST_TOO_LARGE, 0, nil)
		}
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	actual := petImageFormatMediaType(format)
	if declared == "" || actual == "" || declared != actual {
		return petImageReferencePayload{}, newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return petImageReferencePayload{Bytes: imageBytes, MediaType: declared}, nil
}

func decodePetImageReferenceData(value, declaredMediaType string, maxBytes int64) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	declared := normalizePetImageMediaType(declaredMediaType)
	encoded := value
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		comma := strings.IndexByte(value, ',')
		if comma <= len("data:") {
			return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		metadata := value[len("data:"):comma]
		if len(metadata) > PetImageMaxReferenceDataURLMetadata {
			return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		parts := strings.Split(metadata, ";")
		if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-1], "base64") {
			return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		dataURLMediaType := normalizePetImageMediaType(parts[0])
		if dataURLMediaType == "" {
			return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		if declared != "" && declared != dataURLMediaType {
			return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
		declared = dataURLMediaType
		encoded = value[comma+1:]
	} else if strings.Contains(value, ",") {
		return "", "", newPetAIError(PET_IMAGE_REMOTE_URL_UNSUPPORTED, 0, nil)
	}
	if declared == "" || encoded == "" {
		return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if maxBytes <= 0 || int64(len(encoded)) > petImageBase64EncodedLength(maxBytes) {
		return "", "", newPetAIError(PET_IMAGE_REQUEST_TOO_LARGE, 0, nil)
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	return encoded, declared, nil
}

func petImageBase64EncodedLength(maxBytes int64) int64 {
	groups := maxBytes / 3
	if maxBytes%3 != 0 {
		groups++
	}
	if groups > (1 << 61) {
		return int64(^uint64(0) >> 1)
	}
	return groups * 4
}

func minPetImageReferenceDimension(maxDimension int) int {
	if maxDimension <= 0 || maxDimension > PetImageMaxReferenceDimension {
		return PetImageMaxReferenceDimension
	}
	return maxDimension
}

func buildPetImageEditMultipart(
	provider petAIProviderRuntime,
	request PetImageRequest,
	reference petImageReferencePayload,
	maxBytes int64,
) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	// images/edits 使用 multipart 字段，而不是把 reference bytes 塞进 JSON；这与
	// OpenCowork worker 的 image 字段语义一致，也避免 data URL 在不同 provider 间漂移。
	fields := []struct {
		name  string
		value string
	}{
		{name: "model", value: provider.model},
		{name: "prompt", value: request.Prompt},
		{name: "size", value: request.Size},
		{name: "n", value: strconv.Itoa(request.Count)},
		{name: "response_format", value: "b64_json"},
	}
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return nil, "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="image"; filename="`+petImageReferenceFileName(reference.MediaType)+`"`)
	header.Set("Content-Type", reference.MediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if _, err := part.Write(reference.Bytes); err != nil {
		return nil, "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if err := writer.Close(); err != nil {
		return nil, "", newPetAIError(PET_IMAGE_INVALID_REQUEST, 0, nil)
	}
	if maxBytes <= 0 || int64(body.Len()) > maxBytes {
		return nil, "", newPetAIError(PET_IMAGE_REQUEST_TOO_LARGE, 0, nil)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func petImageReferenceFileName(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return "idle.jpg"
	case "image/gif":
		return "idle.gif"
	case "image/webp":
		return "idle.webp"
	default:
		return "idle.png"
	}
}

func (s *PetImageService) parsePetImageHTTPResponse(
	ctx context.Context,
	request *http.Request,
	requestedCount int,
) (PetImageResult, error) {
	response, err := s.transport.RoundTrip(request)
	if err != nil {
		if ctx.Err() != nil {
			return PetImageResult{}, classifyPetImageContextError(ctx.Err())
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return PetImageResult{}, classifyPetImageContextError(err)
		}
		return PetImageResult{}, newPetAIError(PET_IMAGE_UPSTREAM_ERROR, 0, nil)
	}
	if response == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return PetImageResult{}, newPetAIError(PET_IMAGE_UPSTREAM_ERROR, response.StatusCode, nil)
	}
	if response.Body == nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, response.StatusCode, nil)
	}
	if err := validatePetImageResponseContentType(response.Header.Get("Content-Type")); err != nil {
		return PetImageResult{}, err
	}
	responseBody, err := readPetImageResponseBody(response.Body, s.options.MaxResponseBytes)
	if err != nil {
		if ctx.Err() != nil {
			return PetImageResult{}, classifyPetImageContextError(ctx.Err())
		}
		return PetImageResult{}, err
	}
	return parsePetImageResponse(responseBody, requestedCount, s.options)
}

func petImageEndpoint(provider petAIProviderRuntime) (string, error) {
	return petImageEndpointForRoute(provider, "images/generations")
}

func petImageEditEndpoint(provider petAIProviderRuntime) (string, error) {
	return petImageEndpointForRoute(provider, "images/edits")
}

func petImageEndpointForRoute(provider petAIProviderRuntime, route string) (string, error) {
	base := strings.TrimSpace(provider.baseURL)
	if base == "" {
		base = "https://api.openai.com"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid provider base url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("unsupported provider url scheme")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	if provider.apiEndpoint != "" {
		path, err := joinPetAIEndpointPath(parsed.Path, provider.apiEndpoint)
		if err != nil {
			return "", err
		}
		path = replacePetImageEndpointRoute(path, route)
		parsed.Path = path
		parsed.RawPath = ""
		return parsed.String(), nil
	}
	// Provider 的 APIURL 通常是 host 或 /v1 基础路径；已经是图片路由时只替换
	// generations/edits，避免把完整 endpoint 再拼接一遍。
	lowerPath := strings.ToLower(parsed.Path)
	if strings.HasSuffix(lowerPath, "/images/generations") || strings.HasSuffix(lowerPath, "/images/edits") {
		parsed.Path = replacePetImageEndpointRoute(parsed.Path, route)
	} else if strings.HasSuffix(lowerPath, "/v1") || strings.HasSuffix(lowerPath, "/v1beta") {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + route
	} else {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/" + route
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}

func replacePetImageEndpointRoute(path, route string) string {
	path = strings.TrimRight(path, "/")
	lower := strings.ToLower(path)
	for _, suffix := range []string{"/images/generations", "/images/edits"} {
		if strings.HasSuffix(lower, suffix) {
			return path[:len(path)-len(suffix)] + "/" + route
		}
	}
	return path
}

func validatePetImageResponseContentType(contentType string) error {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	return nil
}

func readPetImageResponseBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, newPetAIError(PET_IMAGE_RESPONSE_TOO_LARGE, 0, nil)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, newPetAIError(PET_IMAGE_UPSTREAM_ERROR, 0, nil)
	}
	if int64(len(data)) > maxBytes {
		return nil, newPetAIError(PET_IMAGE_RESPONSE_TOO_LARGE, 0, nil)
	}
	return data, nil
}

type petImageResponse struct {
	Data []petImageResponseItem `json:"data"`
}

type petImageResponseItem struct {
	B64JSON string `json:"b64_json"`
	URL     string `json:"url"`
}

func parsePetImageResponse(body []byte, requestedCount int, options PetImageOptions) (PetImageResult, error) {
	var response petImageResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	if len(response.Data) == 0 || len(response.Data) > requestedCount {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}

	images := make([][]byte, 0, len(response.Data))
	for _, item := range response.Data {
		encoded := strings.TrimSpace(item.B64JSON)
		if encoded != "" {
			imageBytes, err := decodePetImageBytes(encoded, options)
			if err != nil {
				return PetImageResult{}, err
			}
			images = append(images, imageBytes)
			continue
		}
		if strings.TrimSpace(item.URL) != "" {
			// 只接受 b64_json：任意 URL 可能触发 SSRF、跨域重定向或第二套认证，
			// 且无法在当前服务边界内保证下载大小和最终图片格式。这里拒绝而不跟随。
			return PetImageResult{}, newPetAIError(PET_IMAGE_REMOTE_URL_UNSUPPORTED, 0, nil)
		}
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	if len(images) == 0 {
		return PetImageResult{}, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	return PetImageResult{Images: images}, nil
}

func decodePetImageBytes(encoded string, options PetImageOptions) ([]byte, error) {
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > options.MaxImageBytes {
		return nil, newPetAIError(PET_IMAGE_RESPONSE_TOO_LARGE, 0, nil)
	}
	imageBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	if int64(len(imageBytes)) > options.MaxImageBytes {
		return nil, newPetAIError(PET_IMAGE_RESPONSE_TOO_LARGE, 0, nil)
	}
	if err := validatePetImageBytes(imageBytes, options.MaxImageBytes, options.MaxDimension); err != nil {
		return nil, err
	}
	return imageBytes, nil
}

func validatePetImageBytes(imageBytes []byte, maxBytes int64, maxDimension int) error {
	_, err := validatePetImageBytesAndFormat(imageBytes, maxBytes, maxDimension)
	return err
}

func validatePetImageBytesAndFormat(imageBytes []byte, maxBytes int64, maxDimension int) (string, error) {
	if len(imageBytes) == 0 || int64(len(imageBytes)) > maxBytes {
		return "", newPetAIError(PET_IMAGE_RESPONSE_TOO_LARGE, 0, nil)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxDimension || config.Height > maxDimension {
		return "", newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	switch format {
	case "png", "jpeg", "gif", "bmp", "webp":
	default:
		return "", newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	// DecodeConfig 只检查头部；再完整解码一次，拒绝截断或伪造的图片 bytes，
	// 同时用上面的尺寸上限控制解码器可能申请的内存规模。
	if _, _, err := image.Decode(bytes.NewReader(imageBytes)); err != nil {
		return "", newPetAIError(PET_IMAGE_RESPONSE_INVALID, 0, nil)
	}
	return format, nil
}

func petImageFormatMediaType(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func classifyPetImageContextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return newPetAIError(PET_IMAGE_REQUEST_CANCELLED, 0, err)
	case errors.Is(err, context.DeadlineExceeded):
		return newPetAIError(PET_IMAGE_TIMEOUT, 0, err)
	default:
		return newPetAIError(PET_IMAGE_UPSTREAM_ERROR, 0, nil)
	}
}
