package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
)

const petTranscriptionMaxInputBytes int64 = 16 << 20

// PetTranscriptionRequest 是桌宠录音转写的窄契约。音频只以短生命周期的 bare
// base64 进入 Go，provider 引用仍由 PetAIService 解析，前端不会接触凭据。
type PetTranscriptionRequest struct {
	PetID     string               `json:"petId"`
	Provider  PetProviderReference `json:"provider"`
	Data      string               `json:"data"`
	MediaType string               `json:"mediaType"`
	FileName  string               `json:"fileName,omitempty"`
}

type PetTranscriptionResult struct {
	Text string `json:"text"`
}

// PetSpeechSelectionReader 是应用级设置 owner 的窄接口；它只返回 provider/model
// 引用，不把凭据或 provider 实体复制到宠物服务。
type PetSpeechSelectionReader interface {
	GetSpeechProviderSelection() (PetSpeechProviderSelection, error)
}

// TranscribeAudio 使用 OpenAI-compatible audio/transcriptions 端点，把浏览器
// MediaRecorder 生成的 webm/opus 音频转换为聊天文本。它不复用 TTS capability：
// 转写是输入能力，错误时必须明确返回，不能静默把音频当成普通聊天文本。
func (s *PetAIService) TranscribeAudio(ctx context.Context, request PetTranscriptionRequest) (PetTranscriptionResult, error) {
	if s == nil || s.providerReader == nil || s.transport == nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	if ctx == nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}

	normalized, audio, mediaType, fileName, err := normalizePetTranscriptionRequest(request)
	if err != nil {
		return PetTranscriptionResult{}, err
	}
	providerReference := normalized.Provider
	if s.speechSelectionReader != nil {
		providerReference, err = resolveConfiguredSpeechProviderReference(ctx, s.speechSelectionReader)
		if err != nil {
			return PetTranscriptionResult{}, err
		}
	} else {
		// 兼容直接构造 PetAIService 的旧调用方，但能力仍固定为 transcription，
		// 绝不信任请求携带的 chat/TTS capability。
		providerReference.Capability = PetCapabilityTranscription
	}
	provider, err := s.resolveProvider(ctx, providerReference, PetCapabilityTranscription)
	if err != nil {
		return PetTranscriptionResult{}, err
	}
	if provider.protocol != "openai" {
		return PetTranscriptionResult{}, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			providerReference,
			"当前 provider 不支持语音转写能力",
			nil,
		)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", provider.model); err != nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, fileName))
	fileHeader.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if _, err := part.Write(audio); err != nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	if err := writer.Close(); err != nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}
	if int64(body.Len()) > s.options.MaxRequestBytes {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
	}

	endpoint, err := providerEndpoint(provider, "transcription")
	if err != nil {
		return PetTranscriptionResult{}, newPetProviderError(PET_PROVIDER_CONFIG_INVALID, normalized.Provider, "provider transcription endpoint 无效", nil)
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, &body)
	if err != nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	httpRequest.Header.Set("Accept", "application/json")
	for key, value := range provider.headers {
		httpRequest.Header.Set(key, value)
	}
	applyProviderAuth(httpRequest, provider)
	response, err := s.transport.RoundTrip(httpRequest)
	if err != nil {
		if requestCtx.Err() != nil {
			return PetTranscriptionResult{}, classifyPetAIContextError(requestCtx.Err())
		}
		return PetTranscriptionResult{}, newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
	}
	if response == nil || response.Body == nil {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return PetTranscriptionResult{}, s.upstreamStatusError(response)
	}
	data, err := readLimitedBody(response.Body, s.options.MaxResponseBytes)
	if err != nil {
		return PetTranscriptionResult{}, err
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || strings.TrimSpace(payload.Text) == "" {
		return PetTranscriptionResult{}, newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
	}
	return PetTranscriptionResult{Text: strings.TrimSpace(payload.Text)}, nil
}

func resolveConfiguredSpeechProviderReference(
	ctx context.Context,
	reader PetSpeechSelectionReader,
) (PetProviderReference, error) {
	if ctx == nil {
		return PetProviderReference{}, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if err := ctx.Err(); err != nil {
		return PetProviderReference{}, classifyPetAIContextError(err)
	}
	selection, err := reader.GetSpeechProviderSelection()
	if err != nil {
		return PetProviderReference{}, newPetProviderError(
			PET_UPSTREAM_ERROR,
			PetProviderReference{Capability: PetCapabilityTranscription},
			"读取语音识别配置失败",
			nil,
		)
	}
	platform := optionalStringValue(selection.Platform)
	providerID := optionalStringValue(selection.ProviderID)
	model := strings.TrimSpace(selection.ModelID)
	if platform == "" || providerID == "" || model == "" {
		// 源项目没有 speech provider/model 时不会回退到聊天模型；保留独立错误码，
		// 让前端显示明确的未配置提示。
		return PetProviderReference{}, newPetProviderError(
			PET_SPEECH_NOT_CONFIGURED,
			PetProviderReference{Platform: platform, ProviderID: providerID, Model: model, Capability: PetCapabilityTranscription},
			"语音识别 provider/model 未配置",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return PetProviderReference{}, classifyPetAIContextError(err)
	}
	return PetProviderReference{
		Platform:     platform,
		ProviderID:   providerID,
		Model:        model,
		Capability:   PetCapabilityTranscription,
		AutoFallback: false,
	}, nil
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func normalizePetTranscriptionRequest(request PetTranscriptionRequest) (PetTranscriptionRequest, []byte, string, string, error) {
	request.PetID = strings.TrimSpace(request.PetID)
	request.Data = strings.TrimSpace(request.Data)
	request.MediaType = strings.TrimSpace(strings.ToLower(request.MediaType))
	if request.PetID == "" || runeLen(request.PetID) > PetAIMaxPetIDLength || hasLineBreak(request.PetID) {
		return PetTranscriptionRequest{}, nil, "", "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if request.Data == "" || strings.Contains(request.Data, ",") || strings.ContainsAny(request.Data, " \t\r\n") {
		return PetTranscriptionRequest{}, nil, "", "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	audio, err := base64.StdEncoding.DecodeString(request.Data)
	if err != nil || len(audio) == 0 || int64(len(audio)) > petTranscriptionMaxInputBytes {
		return PetTranscriptionRequest{}, nil, "", "", newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	mediaType, err := normalizePetTranscriptionMediaType(request.MediaType)
	if err != nil {
		return PetTranscriptionRequest{}, nil, "", "", err
	}
	fileName := filepath.Base(strings.TrimSpace(request.FileName))
	if fileName == "." || fileName == "" || hasLineBreak(fileName) || strings.ContainsRune(fileName, 0) {
		fileName = "voice-input.webm"
	}
	fileName = sanitizePetTranscriptionFileName(fileName)
	request.FileName = fileName
	return request, audio, mediaType, fileName, nil
}

func sanitizePetTranscriptionFileName(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '-' || character == '_' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 || builder.String() == "." || builder.String() == ".." {
		return "voice-input.webm"
	}
	return builder.String()
}

func normalizePetTranscriptionMediaType(value string) (string, error) {
	if value == "" {
		return "audio/webm", nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.HasPrefix(mediaType, "audio/") {
		return "", newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, errors.New("audio media type is invalid"))
	}
	return mediaType, nil
}
