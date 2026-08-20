package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// 18100 已经由 provider relay 占用；浏览器 bridge 使用相邻端口，避免和代理
	// 共用一个 HTTP server，也避免把 provider API 意外暴露到浏览器调用面。
	DefaultPetBrowserBridgeAddr = "127.0.0.1:18101"
	petBrowserBridgePath        = "/api/pet/call"
	petBrowserBridgeHealthPath  = "/api/pet/health"
	petBrowserBridgeEventsPath  = "/api/pet/events"
	petBrowserMaxBodyBytes      = 32 << 20
)

type PetBrowserBridgeOptions struct {
	Addr string
}

// PetBrowserBridgeDependencies 只接收宠物页面已经使用的服务适配器。
// 不把 application.App 或数据库对象传进来，确保浏览器通信仍然经过业务服务的
// 校验、锁和路径清洗，不会因为增加 HTTP 入口而复制一套数据访问逻辑。
type PetBrowserBridgeDependencies struct {
	Pet         *PetService
	Memory      *PetMemoryService
	Dream       *PetDreamAPIService
	AI          *PetAIAPIService
	Image       *PetImageAPIService
	Media       *PetMediaAPIService
	Studio      *PetStudioAPIService
	Window      *PetWindowAPI
	Provider    *ProviderService
	Gemini      *GeminiService
	Project     *ProjectManagerService
	AppSettings *AppSettingsService
	Scheduler   *PetSchedulerAPI
	Events      *PetBrowserEventHub
}

type PetBrowserEvent struct {
	Name string      `json:"name"`
	Data interface{} `json:"data,omitempty"`
}

// PetBrowserEventHub 只负责把已有运行时事件广播给浏览器，不持有宠物状态。
// 慢浏览器会丢弃单条通知，下一次快照读取仍能得到最终状态，不能反过来阻塞
// 自动照料、AI 流式请求或桌面端事件循环。
type PetBrowserEventHub struct {
	mu      sync.RWMutex
	nextID  uint64
	clients map[uint64]chan PetBrowserEvent
}

func newPetBrowserBridgeToken() string {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand 在受支持的桌面系统上不会失败；兜底值只用于继续启动并让
		// health 接口返回可诊断错误，而不是因为随机源异常把整个桌面应用打死。
		return fmt.Sprintf("pet-bridge-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func NewPetBrowserEventHub() *PetBrowserEventHub {
	return &PetBrowserEventHub{clients: make(map[uint64]chan PetBrowserEvent)}
}

func (h *PetBrowserEventHub) Publish(name string, data interface{}) {
	if h == nil || strings.TrimSpace(name) == "" {
		return
	}
	event := PetBrowserEvent{Name: name, Data: data}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, client := range h.clients {
		select {
		case client <- event:
		default:
		}
	}
}

func (h *PetBrowserEventHub) subscribe(ctx context.Context) <-chan PetBrowserEvent {
	channel := make(chan PetBrowserEvent, 32)
	if h == nil {
		close(channel)
		return channel
	}
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	if h.clients == nil {
		h.clients = make(map[uint64]chan PetBrowserEvent)
	}
	h.clients[id] = channel
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if current, ok := h.clients[id]; ok {
			delete(h.clients, id)
			close(current)
		}
		h.mu.Unlock()
	}()
	return channel
}

type PetBrowserBridge struct {
	deps   PetBrowserBridgeDependencies
	server *http.Server
	addr   string
	close  chan struct{}
	token  string
}

type petBrowserCallRequest struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

type petBrowserCallResponse struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func NewPetBrowserBridge(deps PetBrowserBridgeDependencies, options ...PetBrowserBridgeOptions) *PetBrowserBridge {
	addr := DefaultPetBrowserBridgeAddr
	if len(options) > 0 && strings.TrimSpace(options[0].Addr) != "" {
		addr = strings.TrimSpace(options[0].Addr)
	}
	return &PetBrowserBridge{deps: deps, addr: addr, token: newPetBrowserBridgeToken()}
}

func (b *PetBrowserBridge) Addr() string {
	if b == nil {
		return ""
	}
	return b.addr
}

func (b *PetBrowserBridge) Start() error {
	if b == nil {
		return errors.New("宠物浏览器 bridge 为空")
	}
	if b.server != nil {
		return nil
	}

	listener, err := net.Listen("tcp", b.addr)
	if err != nil {
		return fmt.Errorf("监听宠物浏览器 bridge %s 失败: %w", b.addr, err)
	}
	b.addr = listener.Addr().String()
	b.close = make(chan struct{})
	b.server = &http.Server{
		Handler:           b,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       35 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		if err := b.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// server.Serve 的错误没有调用方可以同步接收；写 stderr 保留启动后的
			// 运行时故障证据，避免浏览器只看到“Failed to fetch”而无法追因。
			_, _ = fmt.Fprintf(os.Stderr, "pet browser bridge stopped: %v\n", err)
		}
		close(b.close)
	}()
	return nil
}

func (b *PetBrowserBridge) Stop(ctx context.Context) error {
	if b == nil || b.server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := b.server.Shutdown(ctx)
	b.server = nil
	return err
}

func (b *PetBrowserBridge) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodOptions {
		b.writeCORS(writer, request)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if !b.allowOrigin(request.Header.Get("Origin")) {
		b.writeError(writer, http.StatusForbidden, "浏览器来源不在本机 bridge 允许范围内")
		return
	}
	b.writeCORS(writer, request)

	switch request.URL.Path {
	case petBrowserBridgeHealthPath:
		if request.Method != http.MethodGet {
			b.writeError(writer, http.StatusMethodNotAllowed, "health 只支持 GET")
			return
		}
		b.writeJSON(writer, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"service": "codeswitch-pet",
			"addr":    b.addr,
			"token":   b.token,
		})
	case petBrowserBridgeEventsPath:
		if request.Method != http.MethodGet {
			b.writeError(writer, http.StatusMethodNotAllowed, "events 只支持 GET")
			return
		}
		if request.URL.Query().Get("token") != b.token {
			b.writeError(writer, http.StatusUnauthorized, "宠物 bridge token 无效")
			return
		}
		b.handleEvents(writer, request)
	case petBrowserBridgePath:
		if request.Method != http.MethodPost {
			b.writeError(writer, http.StatusMethodNotAllowed, "call 只支持 POST")
			return
		}
		b.handleCall(writer, request)
	default:
		b.writeError(writer, http.StatusNotFound, "宠物 bridge 路径不存在")
	}
}

func (b *PetBrowserBridge) handleCall(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("X-CodeSwitch-Pet-Token") != b.token {
		b.writeError(writer, http.StatusUnauthorized, "宠物 bridge token 无效")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, petBrowserMaxBodyBytes)
	defer request.Body.Close()
	var input petBrowserCallRequest
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&input); err != nil {
		b.writeError(writer, http.StatusBadRequest, "bridge 请求 JSON 无效: "+err.Error())
		return
	}
	input.Method = strings.TrimSpace(input.Method)
	if input.Method == "" {
		b.writeError(writer, http.StatusBadRequest, "bridge method 不能为空")
		return
	}
	value, err := b.dispatch(request.Context(), input.Method, input.Args)
	if err != nil {
		b.writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	b.writeJSON(writer, http.StatusOK, petBrowserCallResponse{OK: true, Data: value})
}

func (b *PetBrowserBridge) handleEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		b.writeError(writer, http.StatusInternalServerError, "当前 HTTP server 不支持 SSE")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(": connected\n\n"))
	flusher.Flush()
	for event := range b.requireEvents().subscribe(request.Context()) {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if _, err := writer.Write(append([]byte("data: "), append(data, '\n', '\n')...)); err != nil {
			return
		}
		flusher.Flush()
	}
}

func (b *PetBrowserBridge) dispatch(ctx context.Context, method string, args []json.RawMessage) (interface{}, error) {
	// 这里只列出宠物页面需要的白名单；通用 Wails methodName 转发会把整个桌面
	// 应用的本地文件、进程和系统能力暴露给网页，风险和维护成本都不划算。
	switch method {
	case "codeswitch/services.PetService.GetSnapshot":
		var petID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		return b.requirePet().GetSnapshot(petID)
	case "codeswitch/services.PetService.GetRuntimeSnapshot":
		var petID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		return b.requirePet().GetRuntimeSnapshot(petID)
	case "codeswitch/services.PetService.GetAtlas":
		var petID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		return b.requirePet().GetAtlas(petID)
	case "codeswitch/services.PetService.PerformAction":
		var petID string
		var action PetAction
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &action); err != nil {
			return nil, err
		}
		return b.requirePet().PerformAction(petID, action)
	case "codeswitch/services.PetService.EndWorkEarlyForPet":
		var petID string
		var now int64
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &now); err != nil {
			return nil, err
		}
		return b.requirePet().EndWorkEarlyForPet(petID, now)
	case "codeswitch/services.PetService.Petted":
		var petID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		return nil, b.requirePet().Petted(petID)
	case "codeswitch/services.PetService.PettedForPet":
		var petID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		return b.requirePet().PettedForPet(petID)
	case "codeswitch/services.PetService.RecordProactive":
		var petID string
		var now int64
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &now); err != nil {
			return nil, err
		}
		return b.requirePet().RecordProactive(petID, now)
	case "codeswitch/services.PetService.RecordProactiveState":
		var petID string
		var now int64
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &now); err != nil {
			return nil, err
		}
		return b.requirePet().RecordProactiveState(petID, now)
	case "codeswitch/services.PetService.SaveSettings":
		var petID string
		var settings PetSettingsInput
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &settings); err != nil {
			return nil, err
		}
		return b.requirePet().SaveSettings(petID, settings)
	case "codeswitch/services.PetService.UpdateName":
		var petID, name string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &name); err != nil {
			return nil, err
		}
		return b.requirePet().UpdateName(petID, name)
	case "codeswitch/services.PetService.ClaimDailyBonusForPet":
		var petID string
		var now int64
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &now); err != nil {
			return nil, err
		}
		return b.requirePet().ClaimDailyBonusForPet(petID, now)
	case "codeswitch/services.PetService.MarkMilestoneForPet":
		var petID string
		var days int64
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &days); err != nil {
			return nil, err
		}
		return b.requirePet().MarkMilestoneForPet(petID, days)
	case "codeswitch/services.PetService.ListExperienceLog", "codeswitch/services.PetService.ListExpLog":
		var page, pageSize int
		if err := decodePetBrowserArg(args, 0, &page); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &pageSize); err != nil {
			return nil, err
		}
		return b.requirePet().ListExperienceLog(page, pageSize)

	case "codeswitch/services.PetMemoryService.List":
		return b.requireMemory().List()
	case "codeswitch/services.PetMemoryService.Append":
		var texts []string
		if err := decodePetBrowserArg(args, 0, &texts); err != nil {
			return nil, err
		}
		return b.requireMemory().Append(texts)
	case "codeswitch/services.PetMemoryService.Remove":
		var id string
		if err := decodePetBrowserArg(args, 0, &id); err != nil {
			return nil, err
		}
		return nil, b.requireMemory().Remove(id)
	case "codeswitch/services.PetMemoryService.Clear":
		return nil, b.requireMemory().Clear()

	case "codeswitch/services.PetDreamAPIService.ListHistory":
		var petID string
		var page, pageSize int
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &page); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 2, &pageSize); err != nil {
			return nil, err
		}
		return b.requireDream().ListHistory(petID, page, pageSize)
	case "codeswitch/services.PetDreamAPIService.ReadImage":
		var petID, name string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &name); err != nil {
			return nil, err
		}
		return b.requireDream().ReadImage(petID, name)
	case "codeswitch/services.PetDreamAPIService.SaveHistory":
		var petID string
		var record PetDreamHistoryRecord
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &record); err != nil {
			return nil, err
		}
		return nil, b.requireDream().SaveHistory(petID, record)
	case "codeswitch/services.PetDreamAPIService.DeleteHistory":
		var petID, id string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &id); err != nil {
			return nil, err
		}
		return nil, b.requireDream().DeleteHistory(petID, id)
	case "codeswitch/services.PetDreamAPIService.ApplyEmotion":
		var petID string
		var emotion PetDreamEmotion
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &emotion); err != nil {
			return nil, err
		}
		return nil, b.requireDream().ApplyEmotion(petID, emotion)
	case "codeswitch/services.PetDreamAPIService.StoreImage":
		var petID, mediaType string
		var data []byte
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &mediaType); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 2, &data); err != nil {
			return nil, err
		}
		return b.requireDream().StoreImage(petID, mediaType, data)

	case "codeswitch/services.PetStudioAPIService.ReadSkin":
		var petID, skinID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &skinID); err != nil {
			return nil, err
		}
		return b.requireStudio().ReadSkin(petID, skinID)
	case "codeswitch/services.PetStudioAPIService.SaveSkin":
		var petID string
		var input PetStudioSaveSkinRequest
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &input); err != nil {
			return nil, err
		}
		return b.requireStudio().SaveSkin(petID, input)
	case "codeswitch/services.PetStudioAPIService.ListSkins":
		var petID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		return b.requireStudio().ListSkins(petID)
	case "codeswitch/services.PetStudioAPIService.DeleteSkin":
		var petID, skinID string
		if err := decodePetBrowserArg(args, 0, &petID); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &skinID); err != nil {
			return nil, err
		}
		return nil, b.requireStudio().DeleteSkin(petID, skinID)
	case "codeswitch/services.PetStudioAPIService.GetRoot":
		return b.requireStudio().GetRoot()
	case "codeswitch/services.PetStudioAPIService.OpenRoot":
		return nil, b.requireStudio().OpenRoot()

	case "codeswitch/services.PetMediaAPIService.ApplyChromaKey":
		var input PetChromaKeyRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireMedia().ApplyChromaKey(input)
	case "codeswitch/services.PetMediaAPIService.NormalizeSprite":
		var input PetSpriteNormalizationAPIRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireMedia().NormalizeSprite(input)
	case "codeswitch/services.PetMediaAPIService.PackAtlas":
		var input PetAtlasPackAPIRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireMedia().PackAtlas(input)
	case "codeswitch/services.PetMediaAPIService.SplitActionSheet":
		var input PetActionSheetSplitRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireMedia().SplitActionSheet(input)
	case "codeswitch/services.PetImageAPIService.GenerateImage":
		var input PetImageRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireImage().GenerateImage(input)

	case "codeswitch/services.PetWindowAPI.Open":
		return nil, b.requireWindow().Open()
	case "codeswitch/services.PetWindowAPI.Close":
		return nil, b.requireWindow().Close()
	case "codeswitch/services.PetWindowAPI.State":
		return b.requireWindow().State(), nil
	case "codeswitch/services.PetWindowAPI.SetMode":
		var mode string
		if err := decodePetBrowserArg(args, 0, &mode); err != nil {
			return nil, err
		}
		return nil, b.requireWindow().SetMode(mode)
	case "codeswitch/services.PetWindowAPI.Move":
		var x, y int
		if err := decodePetBrowserArg(args, 0, &x); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &y); err != nil {
			return nil, err
		}
		return nil, b.requireWindow().Move(x, y)
	case "codeswitch/services.PetWindowAPI.Resize":
		var width, height int
		if err := decodePetBrowserArg(args, 0, &width); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &height); err != nil {
			return nil, err
		}
		return nil, b.requireWindow().Resize(width, height)
	case "codeswitch/services.PetWindowAPI.Focus":
		return nil, b.requireWindow().Focus()
	case "codeswitch/services.PetWindowAPI.SetAlwaysOnTop":
		var alwaysOnTop bool
		if err := decodePetBrowserArg(args, 0, &alwaysOnTop); err != nil {
			return nil, err
		}
		return nil, b.requireWindow().SetAlwaysOnTop(alwaysOnTop)
	case "codeswitch/services.PetWindowAPI.SetPlatformLayer":
		var platformID string
		if err := decodePetBrowserArg(args, 0, &platformID); err != nil {
			return nil, err
		}
		return nil, b.requireWindow().SetPlatformLayer(platformID)
	case "codeswitch/services.PetWindowAPI.GetPlatforms":
		return b.requireWindow().GetPlatforms()

	case "codeswitch/services.ProviderService.LoadProviders":
		var kind string
		if err := decodePetBrowserArg(args, 0, &kind); err != nil {
			return nil, err
		}
		providers, err := b.requireProvider().LoadProviders(kind)
		if err != nil {
			return nil, err
		}
		return sanitizePetBrowserProviders(providers), nil
	case "codeswitch/services.ProviderService.FetchModels":
		var kind string
		var providerID int64
		if err := decodePetBrowserArg(args, 0, &kind); err != nil {
			return nil, err
		}
		if err := decodePetBrowserArg(args, 1, &providerID); err != nil {
			return nil, err
		}
		// 只返回 ProviderModel 的安全模型元数据，API key 留在后端；浏览器预览也必须走同一条安全边界。
		return b.requireProvider().FetchModels(kind, providerID)
	case "codeswitch/services.GeminiService.GetProviders":
		return sanitizePetBrowserGeminiProviders(b.requireGemini().GetProviders()), nil
	case "codeswitch/services.ProjectManagerService.GetSnapshot":
		return b.requireProject().GetSnapshot()
	case "codeswitch/services.ProjectManagerService.RefreshProjectIndex":
		return b.requireProject().RefreshProjectIndex()
	case "codeswitch/services.AppSettingsService.GetAppSettings":
		return b.requireAppSettings().GetAppSettings()

	case "codeswitch/services.PetAIAPIService.GenerateDreamText":
		var input PetDreamTextRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireAI().GenerateDreamText(input)
	case "codeswitch/services.PetAIAPIService.SynthesizeSpeech":
		var input PetSpeechRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireAI().SynthesizeSpeech(input)
	case "codeswitch/services.PetAIAPIService.StartSpeechStream":
		var input PetSpeechRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireAI().StartSpeechStream(input)
	case "codeswitch/services.PetAIAPIService.CancelSpeech":
		var requestID string
		if err := decodePetBrowserArg(args, 0, &requestID); err != nil {
			return nil, err
		}
		return nil, b.requireAI().CancelSpeech(requestID)
	case "codeswitch/services.PetAIAPIService.CancelChat":
		var requestID string
		if err := decodePetBrowserArg(args, 0, &requestID); err != nil {
			return nil, err
		}
		return nil, b.requireAI().CancelChat(requestID)
	case "codeswitch/services.PetAIAPIService.StartChat":
		var input PetChatRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireAI().StartChat(input)
	case "codeswitch/services.PetAIAPIService.TranscribeAudio":
		var input PetTranscriptionRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireAI().transcribeAudio(ctx, input)
	case "codeswitch/services.PetSchedulerAPI.SchedulePlan":
		var input PetSchedulerSchedulePlanInput
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireScheduler().SchedulePlan(ctx, input)
	case "codeswitch/services.PetSchedulerAPI.Cancel":
		var input PetSchedulerCancelRequest
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireScheduler().Cancel(ctx, input)
	case "codeswitch/services.PetSchedulerAPI.ValidatePlan":
		var input PetSchedulerValidatePlanInput
		if err := decodePetBrowserArg(args, 0, &input); err != nil {
			return nil, err
		}
		return b.requireScheduler().ValidatePlan(ctx, input)
	default:
		return nil, fmt.Errorf("浏览器 bridge 不允许调用 %q", method)
	}
}

func decodePetBrowserArg(args []json.RawMessage, index int, target interface{}) error {
	if index < 0 || index >= len(args) {
		return fmt.Errorf("bridge 参数 %d 缺失", index)
	}
	if err := json.Unmarshal(args[index], target); err != nil {
		return fmt.Errorf("bridge 参数 %d 无效: %w", index, err)
	}
	return nil
}

func (b *PetBrowserBridge) allowOrigin(origin string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" || origin == "null" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	host := parsed.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return false
	}
	// Vite 在目标端口被占用时会自动递增端口；如果这里只放行 5173，
	// 浏览器页面会在 5174/5175 上被 CORS 拒绝，表现成“本地数据读不了”。
	// 仍然只允许 loopback，且把开发端口限制在窄范围内，避免把 bridge 变成
	// 任意公网来源可调用的通用 RPC。
	port := parsed.Port()
	if port == "" || port == "4173" || port == "9245" {
		return true
	}
	portNumber, err := strconv.Atoi(port)
	return err == nil && portNumber >= 5173 && portNumber <= 5199
}

func (b *PetBrowserBridge) writeCORS(writer http.ResponseWriter, request *http.Request) {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
	} else if b.allowOrigin(origin) {
		writer.Header().Set("Access-Control-Allow-Origin", origin)
	}
	writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CodeSwitch-Pet-Token")
	writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	writer.Header().Set("Vary", "Origin")
}

func (b *PetBrowserBridge) writeJSON(writer http.ResponseWriter, status int, value interface{}) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (b *PetBrowserBridge) writeError(writer http.ResponseWriter, status int, message string) {
	b.writeJSON(writer, status, petBrowserCallResponse{OK: false, Error: message})
}

type petBrowserProvider struct {
	ID                         int64               `json:"id"`
	Name                       string              `json:"name"`
	Enabled                    bool                `json:"enabled"`
	SupportedModels            map[string]bool     `json:"supportedModels,omitempty"`
	ModelCategories            map[string]string   `json:"modelCategories,omitempty"`
	ModelReasoningEffortLevels map[string][]string `json:"modelReasoningEffortLevels,omitempty"`
}

func sanitizePetBrowserProviders(providers []Provider) []petBrowserProvider {
	result := make([]petBrowserProvider, 0, len(providers))
	for _, provider := range providers {
		result = append(result, petBrowserProvider{
			ID:                         provider.ID,
			Name:                       provider.Name,
			Enabled:                    provider.Enabled,
			SupportedModels:            provider.SupportedModels,
			ModelCategories:            provider.ModelCategories,
			ModelReasoningEffortLevels: provider.ModelReasoningEffortLevels,
		})
	}
	return result
}

type petBrowserGeminiProvider struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Model                 string   `json:"model,omitempty"`
	ModelCategory         string   `json:"modelCategory,omitempty"`
	ReasoningEffortLevels []string `json:"reasoningEffortLevels,omitempty"`
	Enabled               bool     `json:"enabled"`
}

func sanitizePetBrowserGeminiProviders(providers []GeminiProvider) []petBrowserGeminiProvider {
	result := make([]petBrowserGeminiProvider, 0, len(providers))
	for _, provider := range providers {
		result = append(result, petBrowserGeminiProvider{
			ID:                    provider.ID,
			Name:                  provider.Name,
			Model:                 provider.Model,
			ModelCategory:         provider.ModelCategory,
			ReasoningEffortLevels: append([]string(nil), provider.ReasoningEffortLevels...),
			Enabled:               provider.Enabled,
		})
	}
	return result
}

func bridgeUnavailable(name string) error {
	return fmt.Errorf("宠物浏览器 bridge 依赖 %s 未配置", name)
}

func (b *PetBrowserBridge) requirePet() *PetService {
	if b == nil || b.deps.Pet == nil {
		return &PetService{}
	}
	return b.deps.Pet
}

func (b *PetBrowserBridge) requireMemory() *PetMemoryService {
	if b == nil || b.deps.Memory == nil {
		return &PetMemoryService{}
	}
	return b.deps.Memory
}

func (b *PetBrowserBridge) requireDream() *PetDreamAPIService {
	if b == nil || b.deps.Dream == nil {
		return &PetDreamAPIService{}
	}
	return b.deps.Dream
}

func (b *PetBrowserBridge) requireAI() *PetAIAPIService {
	if b == nil || b.deps.AI == nil {
		return &PetAIAPIService{}
	}
	return b.deps.AI
}

func (b *PetBrowserBridge) requireImage() *PetImageAPIService {
	if b == nil || b.deps.Image == nil {
		return &PetImageAPIService{}
	}
	return b.deps.Image
}

func (b *PetBrowserBridge) requireMedia() *PetMediaAPIService {
	if b == nil || b.deps.Media == nil {
		return &PetMediaAPIService{}
	}
	return b.deps.Media
}

func (b *PetBrowserBridge) requireStudio() *PetStudioAPIService {
	if b == nil || b.deps.Studio == nil {
		return &PetStudioAPIService{}
	}
	return b.deps.Studio
}

func (b *PetBrowserBridge) requireWindow() *PetWindowAPI {
	if b == nil || b.deps.Window == nil {
		return NewPetWindowAPI(nil)
	}
	return b.deps.Window
}

func (b *PetBrowserBridge) requireProvider() *ProviderService {
	if b == nil || b.deps.Provider == nil {
		return &ProviderService{}
	}
	return b.deps.Provider
}

func (b *PetBrowserBridge) requireGemini() *GeminiService {
	if b == nil || b.deps.Gemini == nil {
		return &GeminiService{}
	}
	return b.deps.Gemini
}

func (b *PetBrowserBridge) requireProject() *ProjectManagerService {
	if b == nil || b.deps.Project == nil {
		return &ProjectManagerService{}
	}
	return b.deps.Project
}

func (b *PetBrowserBridge) requireAppSettings() *AppSettingsService {
	if b == nil || b.deps.AppSettings == nil {
		return &AppSettingsService{}
	}
	return b.deps.AppSettings
}

func (b *PetBrowserBridge) requireScheduler() *PetSchedulerAPI {
	if b == nil || b.deps.Scheduler == nil {
		return &PetSchedulerAPI{}
	}
	return b.deps.Scheduler
}

func (b *PetBrowserBridge) requireEvents() *PetBrowserEventHub {
	if b == nil || b.deps.Events == nil {
		return NewPetBrowserEventHub()
	}
	return b.deps.Events
}
