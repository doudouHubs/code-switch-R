package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const (
	PetAudioDefaultQueueCapacity = 8
	petAudioReadChunkSize        = 32 << 10
)

// PetAudioEventType 是语音流的稳定生命周期；chunk 的 Data 使用 []byte，序列化时自然变为 base64。
type PetAudioEventType string

const (
	PetAudioEventStarted   PetAudioEventType = "started"
	PetAudioEventChunk     PetAudioEventType = "chunk"
	PetAudioEventCompleted PetAudioEventType = "completed"
	PetAudioEventFailed    PetAudioEventType = "failed"
	PetAudioEventCancelled PetAudioEventType = "cancelled"
)

type PetAudioEvent struct {
	Type      PetAudioEventType `json:"type"`
	PetID     string            `json:"petId"`
	RequestID string            `json:"requestId"`
	Sequence  int64             `json:"sequence"`
	Data      []byte            `json:"data,omitempty"`
	MediaType string            `json:"mediaType,omitempty"`
	Format    string            `json:"format,omitempty"`
	Error     *PetAIEventError  `json:"error,omitempty"`
}

type PetAudioEventEmitter interface {
	EmitAudio(event PetAudioEvent) error
}

type PetAudioEventEmitterFunc func(PetAudioEvent) error

func (f PetAudioEventEmitterFunc) EmitAudio(event PetAudioEvent) error {
	if f == nil {
		return nil
	}
	return f(event)
}

type PetSpeechStartResult struct {
	RequestID string `json:"requestId"`
}

// StartSpeechStream 在 started 事件发出后异步执行语音流；API bridge 可立即返回 requestId，
// 主控随后通过 CancelSpeech 终止底层 HTTP 请求，而不是等整段音频生成完再拿结果。
func (s *PetAIService) StartSpeechStream(ctx context.Context, request PetSpeechRequest) (PetSpeechStartResult, error) {
	if s == nil {
		return PetSpeechStartResult{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	input, provider, requestCtx, cancel, state, err := s.preparePetSpeechStream(ctx, request, s.audioEmitter)
	if err != nil {
		return PetSpeechStartResult{}, err
	}
	if err := s.emitPetAudio(state, s.audioEmitter, PetAudioEvent{
		Type: PetAudioEventStarted, PetID: input.PetID, RequestID: input.RequestID,
	}); err != nil {
		s.releaseRequest(input.RequestID, state)
		cancel()
		return PetSpeechStartResult{}, err
	}
	go func() {
		defer s.releaseRequest(input.RequestID, state)
		defer cancel()
		_ = s.runPetSpeechStream(requestCtx, state, input, provider, s.audioEmitter)
	}()
	return PetSpeechStartResult{RequestID: input.RequestID}, nil
}

// StreamSpeech 是队列和非 Wails 宿主使用的同步 runner；事件按 chunk 到达，函数只在流完成、失败或取消后返回。
func (s *PetAIService) StreamSpeech(ctx context.Context, request PetSpeechRequest, emitter PetAudioEventEmitter) error {
	input, provider, requestCtx, cancel, state, err := s.preparePetSpeechStream(ctx, request, emitter)
	if err != nil {
		return err
	}
	defer s.releaseRequest(input.RequestID, state)
	defer cancel()
	if err := s.emitPetAudio(state, emitter, PetAudioEvent{
		Type: PetAudioEventStarted, PetID: input.PetID, RequestID: input.RequestID,
	}); err != nil {
		return err
	}
	return s.runPetSpeechStream(requestCtx, state, input, provider, emitter)
}

func (s *PetAIService) CancelSpeech(requestID string) error {
	return s.CancelChat(requestID)
}

func (s *PetAIService) preparePetSpeechStream(
	ctx context.Context,
	request PetSpeechRequest,
	emitter PetAudioEventEmitter,
) (PetSpeechRequest, petAIProviderRuntime, context.Context, context.CancelFunc, *petAIRequestState, error) {
	input, err := s.normalizeSpeechRequest(request)
	if err != nil {
		return PetSpeechRequest{}, petAIProviderRuntime{}, nil, nil, nil, err
	}
	if ctx == nil || input.RequestID == "" {
		return PetSpeechRequest{}, petAIProviderRuntime{}, nil, nil, nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if s == nil || s.providerReader == nil || s.transport == nil || emitter == nil {
		return PetSpeechRequest{}, petAIProviderRuntime{}, nil, nil, nil, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	provider, err := s.resolveProvider(ctx, input.Provider, PetCapabilityTTS)
	if err != nil {
		return PetSpeechRequest{}, petAIProviderRuntime{}, nil, nil, nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.options.Timeout)
	state := &petAIRequestState{cancel: cancel}
	if err := s.reserveRequest(input.RequestID, state); err != nil {
		cancel()
		return PetSpeechRequest{}, petAIProviderRuntime{}, nil, nil, nil, err
	}
	return input, provider, requestCtx, cancel, state, nil
}

func (s *PetAIService) runPetSpeechStream(
	ctx context.Context,
	state *petAIRequestState,
	input PetSpeechRequest,
	provider petAIProviderRuntime,
	emitter PetAudioEventEmitter,
) error {
	err := s.executePetSpeechStream(ctx, state, input, provider, emitter)
	if err != nil {
		classified := classifyPetAIExecutionError(err, ctx)
		if errors.Is(ctx.Err(), context.Canceled) || PetAIErrorCodeOf(classified) == string(PET_AI_REQUEST_CANCELLED) {
			_ = s.emitPetAudio(state, emitter, PetAudioEvent{
				Type: PetAudioEventCancelled, PetID: input.PetID, RequestID: input.RequestID,
			})
			return classified
		}
		if !isPetAIEventError(err) {
			_ = s.emitPetAudio(state, emitter, PetAudioEvent{
				Type: PetAudioEventFailed, PetID: input.PetID, RequestID: input.RequestID,
				Error: publicPetAIEventError(classified, ctx),
			})
		}
		return classified
	}
	if ctx.Err() != nil {
		_ = s.emitPetAudio(state, emitter, PetAudioEvent{
			Type: PetAudioEventCancelled, PetID: input.PetID, RequestID: input.RequestID,
		})
		return classifyPetAIContextError(ctx.Err())
	}
	if err := s.emitPetAudio(state, emitter, PetAudioEvent{
		Type: PetAudioEventCompleted, PetID: input.PetID, RequestID: input.RequestID,
	}); err != nil {
		return err
	}
	return nil
}

func (s *PetAIService) executePetSpeechStream(
	ctx context.Context,
	state *petAIRequestState,
	request PetSpeechRequest,
	provider petAIProviderRuntime,
	emitter PetAudioEventEmitter,
) error {
	mode, err := resolvePetSpeechMode(request.VoiceMode, provider)
	if err != nil {
		return err
	}
	chunk := func(data []byte, format PetAudioFormat, encoding string) error {
		if len(data) == 0 {
			return nil
		}
		return s.emitPetAudio(state, emitter, PetAudioEvent{
			Type: PetAudioEventChunk, PetID: request.PetID, RequestID: request.RequestID,
			Data: append([]byte(nil), data...), MediaType: format.MediaType, Format: encoding,
		})
	}
	switch mode {
	case PetVoiceSpeech:
		body, err := json.Marshal(map[string]any{
			"model": provider.model, "input": request.Text,
			"voice": request.Voice, "instructions": request.Instruction,
		})
		if err != nil || int64(len(body)) > s.options.MaxRequestBytes {
			return newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
		}
		endpoint, err := providerEndpoint(provider, "speech")
		if err != nil {
			return newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider speech endpoint 无效", nil)
		}
		response, err := s.doJSONRequest(ctx, provider, endpoint, body, "audio/*")
		if err != nil {
			return err
		}
		if response.Body == nil {
			return newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return s.upstreamStatusError(response)
		}
		contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
		if contentType == "" {
			contentType = "audio/mpeg"
		}
		format, err := ParsePetAudioMediaType(contentType)
		if err != nil {
			return newPetAIError(PET_AI_MEDIA_TYPE_INVALID, response.StatusCode, nil)
		}
		_, err = streamPetAudioBody(response.Body, format, s.options.MaxAudioBytes, func(data []byte) error {
			return chunk(data, format, format.MediaType)
		})
		return err
	case PetVoiceChat:
		body, err := buildOpenAIStreamingChatAudioBody(provider, request)
		if err != nil || int64(len(body)) > s.options.MaxRequestBytes {
			return newPetAIError(PET_AI_REQUEST_TOO_LARGE, 0, nil)
		}
		endpoint, err := providerEndpoint(provider, "chat")
		if err != nil {
			return newPetProviderError(PET_PROVIDER_CONFIG_INVALID, provider.reference, "provider chat audio endpoint 无效", nil)
		}
		if isPetAISpeechEndpoint(endpoint) {
			return newPetProviderError(PET_CAPABILITY_UNSUPPORTED, provider.reference, "chat audio 不支持 speech endpoint", nil)
		}
		response, err := s.doJSONRequest(ctx, provider, endpoint, body, "text/event-stream")
		if err != nil {
			return err
		}
		if response.Body == nil {
			return newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return s.upstreamStatusError(response)
		}
		contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
		if contentType != "" {
			mediaType, _, parseErr := mime.ParseMediaType(contentType)
			if parseErr != nil || strings.ToLower(mediaType) != "text/event-stream" {
				return newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
			}
		}
		format := PetAudioFormat{MediaType: "audio/pcm", PCM16: true, SampleRate: 24000}
		parser := NewPetPCM16Parser(s.options.MaxAudioBytes)
		received := false
		encodedLimit := s.options.MaxAudioBytes + s.options.MaxAudioBytes/2 + 8<<10
		err = scanPetAISSE(response.Body, encodedLimit, s.options.MaxSSELineBytes, s.options.MaxSSEEventBytes, func(event petAISSEEvent) error {
			data, eventFormat, err := parsePetOpenAIAudioDelta(event.Data, s.options.MaxAudioBytes)
			if err != nil {
				return err
			}
			if data == nil {
				return nil
			}
			if eventFormat != "" && eventFormat != "pcm" && eventFormat != "pcm16" {
				return newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
			}
			decoded, err := parser.Push(data)
			if err != nil {
				return err
			}
			if len(decoded) == 0 {
				return nil
			}
			received = true
			return chunk(decoded, format, "pcm16")
		})
		if err != nil && !errors.Is(err, errPetAISSEDone) {
			return err
		}
		if err := parser.Flush(); err != nil {
			return err
		}
		if !received {
			return newPetAIError(PET_AI_RESPONSE_INVALID, response.StatusCode, nil)
		}
		return nil
	default:
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
}

func buildOpenAIStreamingChatAudioBody(provider petAIProviderRuntime, request PetSpeechRequest) ([]byte, error) {
	model := strings.ToLower(strings.TrimSpace(provider.model))
	messages := make([]map[string]any, 0, 2)
	if strings.Contains(model, "mimo") {
		if request.Instruction != "" {
			messages = append(messages, map[string]any{"role": "user", "content": request.Instruction})
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": applyPetVoiceTag(provider.model, request.VoiceTag, request.Text)})
	} else {
		directive := "Read the following text aloud exactly as written. Do not add, omit or change anything."
		if request.Instruction != "" {
			directive += " Speaking style: " + request.Instruction
		}
		messages = append(messages, map[string]any{"role": "user", "content": directive + "\n\n" + request.Text})
	}
	audio := map[string]any{"format": "pcm16"}
	if request.Voice != "" {
		audio["voice"] = request.Voice
	}
	return json.Marshal(map[string]any{
		"model": provider.model, "modalities": []string{"text", "audio"},
		"messages": messages, "audio": audio, "stream": true,
	})
}

func parsePetOpenAIAudioDelta(data string, maxBytes int64) ([]byte, string, error) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return nil, "", nil
	}
	var payload struct {
		Choices []struct {
			Delta struct {
				Audio *struct {
					Data   *string `json:"data"`
					Format string  `json:"format"`
				} `json:"audio"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return nil, "", newPetAIError(PET_AI_SSE_INVALID, 0, nil)
	}
	if len(payload.Choices) == 0 || payload.Choices[0].Delta.Audio == nil || payload.Choices[0].Delta.Audio.Data == nil {
		return nil, "", nil
	}
	decoded, err := DecodePetAudioBase64(*payload.Choices[0].Delta.Audio.Data, maxBytes)
	if err != nil {
		return nil, "", err
	}
	return decoded, strings.ToLower(strings.TrimSpace(payload.Choices[0].Delta.Audio.Format)), nil
}

func (s *PetAIService) emitPetAudio(state *petAIRequestState, emitter PetAudioEventEmitter, event PetAudioEvent) error {
	if emitter == nil || state == nil {
		return newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	s.mu.Lock()
	state.sequence++
	event.Sequence = state.sequence
	s.mu.Unlock()
	if err := emitter.EmitAudio(event); err != nil {
		return newPetAIError(PET_AI_EVENT_ERROR, 0, nil)
	}
	return nil
}

type PetAudioFormat struct {
	MediaType  string
	PCM16      bool
	SampleRate int
}

// ParsePetAudioMediaType 只允许已知音频类型；未知类型直接失败，避免把文本、容器或任意二进制误送给播放器。
func ParsePetAudioMediaType(contentType string) (PetAudioFormat, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return PetAudioFormat{}, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return PetAudioFormat{}, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	format := PetAudioFormat{MediaType: mediaType}
	switch mediaType {
	case "audio/mpeg", "audio/mp3":
		format.MediaType = "audio/mpeg"
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		format.MediaType = "audio/wav"
	case "audio/ogg":
	case "audio/opus":
	case "audio/aac":
	case "audio/flac":
	case "audio/webm":
	case "audio/mp4", "audio/m4a":
		format.MediaType = "audio/mp4"
	case "audio/pcm", "audio/raw", "audio/l16":
		format.MediaType = "audio/pcm"
		format.PCM16 = true
	case "application/octet-stream":
		// 旧 speech provider 允许无 MIME 的二进制响应，保留这个兼容入口；
		// chat/SSE 不会使用它，避免把未知格式的结构化响应当作 PCM。
		format.MediaType = "application/octet-stream"
	default:
		return PetAudioFormat{}, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
	}
	if rate := strings.TrimSpace(params["rate"]); rate != "" {
		value, parseErr := strconv.Atoi(rate)
		if parseErr != nil {
			return PetAudioFormat{}, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
		}
		switch value {
		case 8000, 16000, 22050, 24000, 44100, 48000:
			format.SampleRate = value
		default:
			return PetAudioFormat{}, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
		}
	}
	return format, nil
}

// DecodePetAudioBase64 是 OpenAI-compatible audio_delta 的唯一 base64 边界。
// 禁止 data URL、空白和非标准填充，先按编码长度限流再分配解码缓冲区。
func DecodePetAudioBase64(value string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, " \t\r\n") || strings.Contains(value, ",") || strings.HasPrefix(strings.ToLower(value), "data:") {
		return nil, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	if int64(len(value)) > ((maxBytes+2)/3)*4 {
		return nil, newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) == 0 {
		return nil, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	if int64(len(decoded)) > maxBytes {
		return nil, newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	return decoded, nil
}

// PetPCM16Parser 处理跨网络 chunk 的半个 sample；结束时仍有残留字节必须报错，不能静默丢样本。
type PetPCM16Parser struct {
	maxBytes int64
	total    int64
	pending  []byte
}

func NewPetPCM16Parser(maxBytes int64) *PetPCM16Parser {
	return &PetPCM16Parser{maxBytes: maxBytes}
}

func (p *PetPCM16Parser) Push(data []byte) ([]byte, error) {
	if p == nil || p.maxBytes <= 0 {
		return nil, newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	if int64(len(data)) > p.maxBytes-p.total {
		return nil, newPetAIError(PET_AI_RESPONSE_TOO_LARGE, 0, nil)
	}
	p.total += int64(len(data))
	combined := make([]byte, 0, len(p.pending)+len(data))
	combined = append(combined, p.pending...)
	combined = append(combined, data...)
	usable := len(combined) - len(combined)%2
	if usable == 0 {
		p.pending = append(p.pending[:0], combined...)
		return nil, nil
	}
	result := append([]byte(nil), combined[:usable]...)
	p.pending = append(p.pending[:0], combined[usable:]...)
	return result, nil
}

func (p *PetPCM16Parser) Flush() error {
	if p == nil || len(p.pending) != 0 {
		return newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	return nil
}

type PetAudioQueueItem struct {
	RequestID string
	Run       func(context.Context) error
}

type petAudioQueueJob struct {
	item   PetAudioQueueItem
	ctx    context.Context
	cancel context.CancelFunc
}

// PetAudioQueue 是句子级单 worker 队列。容量同时包含当前播放任务和等待任务，防止“有限队列”实际多出一个并发音频。
type PetAudioQueue struct {
	mu       sync.Mutex
	capacity int
	items    []*petAudioQueueJob
	active   *petAudioQueueJob
	closed   bool
	wake     chan struct{}
	done     chan struct{}
}

func NewPetAudioQueue(capacity int) *PetAudioQueue {
	if capacity <= 0 {
		capacity = PetAudioDefaultQueueCapacity
	}
	queue := &PetAudioQueue{
		capacity: capacity,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go queue.worker()
	return queue
}

func (q *PetAudioQueue) Enqueue(ctx context.Context, item PetAudioQueueItem) error {
	if q == nil || ctx == nil || strings.TrimSpace(item.RequestID) == "" || item.Run == nil {
		return newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	if ctx.Err() != nil {
		return newPetAIError(PET_AI_REQUEST_CANCELLED, 0, ctx.Err())
	}
	jobCtx, cancel := context.WithCancel(ctx)
	job := &petAudioQueueJob{item: item, ctx: jobCtx, cancel: cancel}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		cancel()
		return newPetAIError(PET_AI_AUDIO_QUEUE_CLOSED, 0, nil)
	}
	if q.active != nil && q.active.item.RequestID == item.RequestID {
		cancel()
		return newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
	}
	for _, queued := range q.items {
		if queued.item.RequestID == item.RequestID {
			cancel()
			return newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil)
		}
	}
	if len(q.items)+boolInt(q.active != nil) >= q.capacity {
		cancel()
		return newPetAIError(PET_AI_AUDIO_QUEUE_FULL, 0, nil)
	}
	q.items = append(q.items, job)
	q.signal()
	return nil
}

func (q *PetAudioQueue) Cancel(requestID string) bool {
	if q == nil {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active != nil && q.active.item.RequestID == requestID {
		q.active.cancel()
		return true
	}
	for index, job := range q.items {
		if job.item.RequestID == requestID {
			job.cancel()
			q.items = append(q.items[:index], q.items[index+1:]...)
			return true
		}
	}
	return false
}

// Drain 取消当前句子并丢弃所有尚未启动句子；新句子仍可在 drain 后入队。
func (q *PetAudioQueue) Drain() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.active != nil {
		q.active.cancel()
	}
	for _, job := range q.items {
		job.cancel()
	}
	q.items = nil
	q.mu.Unlock()
	q.signal()
}

func (q *PetAudioQueue) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		if q.active != nil {
			q.active.cancel()
		}
		for _, job := range q.items {
			job.cancel()
		}
		q.items = nil
	}
	q.mu.Unlock()
	q.signal()
}

func (q *PetAudioQueue) Wait() {
	if q == nil {
		return
	}
	<-q.done
}

func (q *PetAudioQueue) worker() {
	defer close(q.done)
	for {
		q.mu.Lock()
		if len(q.items) == 0 {
			closed := q.closed
			q.mu.Unlock()
			if closed {
				return
			}
			<-q.wake
			continue
		}
		job := q.items[0]
		q.items = q.items[1:]
		q.active = job
		q.mu.Unlock()

		if job.ctx.Err() == nil {
			_ = job.item.Run(job.ctx)
		}
		job.cancel()
		q.mu.Lock()
		if q.active == job {
			q.active = nil
		}
		q.mu.Unlock()
	}
}

func (q *PetAudioQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func streamPetAudioBody(body io.Reader, format PetAudioFormat, maxBytes int64, onChunk func([]byte) error) (bool, error) {
	if body == nil || maxBytes <= 0 || onChunk == nil {
		return false, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	var parser *PetPCM16Parser
	if format.PCM16 {
		parser = NewPetPCM16Parser(maxBytes)
	}
	buffer := make([]byte, petAudioReadChunkSize)
	var emitted bool
	for {
		count, err := body.Read(buffer)
		if count > 0 {
			data := buffer[:count]
			if parser != nil {
				data, err = parser.Push(data)
				if err != nil {
					return emitted, err
				}
			}
			if len(data) > 0 {
				if err := onChunk(append([]byte(nil), data...)); err != nil {
					return emitted, err
				}
				emitted = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return emitted, newPetAIError(PET_AI_UPSTREAM_ERROR, 0, nil)
		}
	}
	if parser != nil {
		if err := parser.Flush(); err != nil {
			return emitted, err
		}
	}
	if !emitted {
		return false, newPetAIError(PET_AI_RESPONSE_INVALID, 0, nil)
	}
	return true, nil
}
