package main

import (
	"codeswitch/services"
	"context"
	"embed"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"codeswitch/services/channels"
	"github.com/daodao97/xgo/xdb"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/dock"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist all:resources/pets
var assets embed.FS

//go:embed assets/icon.png assets/icon-dark.png
var trayIcons embed.FS

type AppService struct {
	App        *application.App
	MainWindow application.Window
}

// channelBrowserBridgeAdapter 把 channels.ChannelService 适配到 services 包的
// 浏览器窄接口。跨包只传 JSON 字节，避免 services 反向 import channels 形成循环依赖。
type channelBrowserBridgeAdapter struct {
	service *channels.ChannelService
}

func (a channelBrowserBridgeAdapter) ListDescriptors() (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.ListDescriptors(), nil
}

func (a channelBrowserBridgeAdapter) ListInstances() (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.ListInstances()
}

func (a channelBrowserBridgeAdapter) ListProjects() (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.ListProjects()
}

func (a channelBrowserBridgeAdapter) SaveInstance(payload []byte) error {
	if a.service == nil {
		return fmt.Errorf("channel service is unavailable")
	}
	var instance channels.ChannelInstance
	if err := json.Unmarshal(payload, &instance); err != nil {
		return fmt.Errorf("channel instance payload is invalid: %w", err)
	}
	return a.service.SaveInstance(instance)
}

func (a channelBrowserBridgeAdapter) RemoveInstance(id string) error {
	if a.service == nil {
		return fmt.Errorf("channel service is unavailable")
	}
	return a.service.RemoveInstance(id)
}

func (a channelBrowserBridgeAdapter) SetEnabled(id string, enabled bool) error {
	if a.service == nil {
		return fmt.Errorf("channel service is unavailable")
	}
	return a.service.SetEnabled(id, enabled)
}

func (a channelBrowserBridgeAdapter) Start(id string) error {
	if a.service == nil {
		return fmt.Errorf("channel service is unavailable")
	}
	return a.service.Start(id)
}

func (a channelBrowserBridgeAdapter) Stop(id string) error {
	if a.service == nil {
		return fmt.Errorf("channel service is unavailable")
	}
	return a.service.Stop(id)
}

func (a channelBrowserBridgeAdapter) GetStatus(id string) (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.GetStatus(id), nil
}

func (a channelBrowserBridgeAdapter) ListSessions(instanceID string) (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.ListSessions(instanceID)
}

func (a channelBrowserBridgeAdapter) ListMessages(sessionID string, limit int) (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.ListMessages(sessionID, limit)
}

func (a channelBrowserBridgeAdapter) SendMessage(instanceID, chatID, content string) (string, error) {
	if a.service == nil {
		return "", fmt.Errorf("channel service is unavailable")
	}
	return a.service.SendMessage(instanceID, chatID, content)
}

func (a channelBrowserBridgeAdapter) StartWeixinLogin(instanceID string) (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.StartWeixinLogin(instanceID)
}

func (a channelBrowserBridgeAdapter) WaitWeixinLogin(instanceID, sessionKey string) (interface{}, error) {
	if a.service == nil {
		return nil, fmt.Errorf("channel service is unavailable")
	}
	return a.service.WaitWeixinLogin(instanceID, sessionKey)
}

func (a channelBrowserBridgeAdapter) CancelWeixinLogin(instanceID, sessionKey string) error {
	if a.service == nil {
		return fmt.Errorf("channel service is unavailable")
	}
	return a.service.CancelWeixinLogin(instanceID, sessionKey)
}

func (a *AppService) SetApp(app *application.App) {
	a.App = app
}

// ShowMainWindow 供独立桌宠窗唤起主窗口；设置和 Studio 属于主应用页面，
// 不能在透明桌宠窗里再挂一套路由，否则会重新引入主布局和焦点冲突。
func (a *AppService) ShowMainWindow() {
	if a == nil || a.MainWindow == nil {
		return
	}
	window := a.MainWindow
	if window.IsMinimised() {
		window.UnMinimise()
	}
	window.Show()
	if runtime.GOOS == "windows" {
		window.SetAlwaysOnTop(true)
		window.Focus()
		go func() {
			time.Sleep(150 * time.Millisecond)
			window.SetAlwaysOnTop(false)
		}()
		return
	}
	window.Focus()
}

func (a *AppService) OpenSecondWindow() {
	if a.App == nil {
		fmt.Println("[ERROR] app not initialized")
		return
	}
	name := fmt.Sprintf("logs-%d", time.Now().UnixNano())
	win := a.App.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Logs",
		Name:      name,
		Width:     1024,
		Height:    800,
		MinWidth:  600,
		MinHeight: 300,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			TitleBar:                application.MacTitleBarHidden,
			Backdrop:                application.MacBackdropTransparent,
		},
		BackgroundColour: application.NewRGB(27, 38, 54),
		URL:              "/#/logs",
	})
	win.Center()
}

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts a goroutine that emits a time-based event every second. It subsequently runs the application and
// logs any error that might occur.
func main() {
	services.WriteRuntimeDiagnostic("main-start", fmt.Sprintf("args=%q", os.Args[1:]))
	defer services.WriteRuntimeDiagnostic("main-exit")

	// Codex Hook 会同步启动当前 EXE；必须在数据库和 Wails 初始化前走轻量分支，
	// 否则一次状态事件就会拉起一个完整 CodeSwitch 实例并阻塞 Codex。
	if services.IsProjectManagerCodexHookInvocation(os.Args[1:]) {
		services.WriteRuntimeDiagnostic("hook-dispatch")
		if err := services.RunProjectManagerCodexHookReceiver(os.Stdin); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "CodeSwitch Codex hook failed: %v\n", err)
			// Hook 的失败必须回传给 Codex；继续返回 0 会让状态灯静默丢事件，排障时只剩假成功。
			os.Exit(1)
		}
		return
	}

	appservice := &AppService{}
	// 资源服务必须在首个宠物快照读取前注入 embed.FS；否则开发环境可能正常，
	// 打包后却会退回工作区磁盘路径，导致 atlas 在用户机器上凭空消失。
	services.SetPetAssetSource(assets)

	// 【修复】第一步：初始化数据库（必须最先执行）
	// 解决问题：InitGlobalDBQueue 依赖 xdb.DB("default")，但 xdb.Inits() 在 NewProviderRelayService 中
	if err := services.InitDatabase(); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	log.Println("✅ 数据库已初始化")

	// 【修复】第二步：初始化写入队列（依赖数据库连接）
	if err := services.InitGlobalDBQueue(); err != nil {
		log.Fatalf("初始化数据库队列失败: %v", err)
	}
	log.Println("✅ 数据库写入队列已启动")

	// 宠物数据必须在 PetService 注册前完成迁移，确保首个 Wails 快照就能看到旧数据；
	// 迁移器只读 ~/.open-cowork，使用 fingerprint 和 marker 保证重复启动不会重写目标状态。
	petDB, err := xdb.DB("default")
	if err != nil {
		log.Fatalf("获取宠物数据库连接失败: %v", err)
	}
	petDAO := services.NewPetDAO(petDB)
	migrationReport, migrationErr := services.MigrateOpenCoworkPet(context.Background(), petDAO, services.PetMigrationOptions{})
	if migrationErr != nil {
		log.Printf("⚠️ OpenCowork 宠物迁移失败，保留目标默认配置: %v", migrationErr)
	} else if !migrationReport.AlreadyApplied {
		log.Printf("✅ OpenCowork 宠物迁移完成：导入=%d 跳过=%d 缺失=%d 失败=%d 引用缺失=%d",
			migrationReport.Imported,
			migrationReport.Skipped,
			migrationReport.Missing,
			migrationReport.Failed,
			migrationReport.MissingReferences,
		)
	}
	petService := services.NewPetService(petDAO)
	// usage 事件可能来自并发聊天；每只宠物必须复用同一个 PetService，才能让
	// AddExperience 的读改写锁覆盖跨请求并发，而不是每次事件临时创建一把失效的锁。
	petUsageServicesMu := sync.Mutex{}
	petUsageServices := map[string]*services.PetService{services.DefaultPetID: petService}
	petMemoryService := services.NewPetMemoryService(petDAO)
	petDreamAPIService := services.NewPetDreamAPIService(petDAO, petDAO)
	petJobStore, petJobStoreErr := services.NewPetSQLiteJobStore(petDB)
	if petJobStoreErr != nil {
		log.Fatalf("宠物调度数据库初始化失败: %v", petJobStoreErr)
	}
	if sourceRoot := strings.TrimSpace(migrationReport.SourceRoot); sourceRoot != "" {
		// 旧宠物状态和旧 cron 任务必须使用同一个只读源目录；放在目标
		// scheduler 构造前迁移，避免首个心跳已经开始消费时仍在补写任务。
		cronReport, cronErr := services.MigrateOpenCoworkPetCronJobs(
			context.Background(),
			sourceRoot,
			petJobStore,
		)
		if cronErr != nil {
			log.Printf("⚠️ OpenCowork 宠物计划迁移失败，保留目标计划: %v", cronErr)
		} else if !cronReport.AlreadyApplied && (cronReport.Scanned > 0 || cronReport.Imported > 0 || len(cronReport.Diagnostics) > 0) {
			log.Printf("✅ OpenCowork 宠物计划迁移完成：扫描=%d 导入=%d 跳过=%d 诊断=%d",
				cronReport.Scanned,
				cronReport.Imported,
				cronReport.Skipped,
				len(cronReport.Diagnostics),
			)
		}
	}

	// 运行时只消费调用方提供的心跳，不自行启动 ticker；这样状态推进、自动照料
	// 和应用关闭共用同一生命周期，避免后台 goroutine 在 Wails 已退出后继续写库。
	var petApp *application.App
	var petBrowserBridge *services.PetBrowserBridge
	petBrowserEvents := services.NewPetBrowserEventHub()
	petAICompletionBroker := services.NewPetAICompletionBroker()
	petAIWailsEvents := services.NewPetAIEventCoalescer(50*time.Millisecond, func(event services.PetAIEvent) {
		if petApp != nil {
			petApp.Event.Emit("pet.ai", event)
		}
	})
	petActivityEmitter := services.PetActivityEmitterFunc(func(event services.PetActivityEvent) error {
		// 活动态同时投递给 Wails 和 loopback bridge；它是 UI 旁路，不得因为
		// 任一广播通道暂时不可用而阻断真实模型请求或 provider 降级。
		petBrowserEvents.Publish("pet.activity", event)
		if petApp != nil {
			petApp.Event.Emit("pet.activity", event)
		}
		return nil
	})
	petRuntime := services.NewPetRuntime(petService, services.PetRuntimeOptions{
		Emitter: services.PetRuntimeEmitterFunc(func(result services.PetRuntimeResult) {
			petBrowserEvents.Publish("pet.runtime", result)
			if petApp != nil {
				petApp.Event.Emit("pet.runtime", result)
			}
		}),
	})
	petStopChan := make(chan struct{})
	petScheduler := services.NewPetScheduler(
		petJobStore,
		services.PetSchedulerEmitterFunc(func(_ context.Context, event services.PetSchedulerEvent) error {
			petBrowserEvents.Publish("pet.reminder", event)
			if petApp != nil {
				petApp.Event.Emit("pet.reminder", event)
			}
			return nil
		}),
		nil,
	)
	petSchedulerRuntime := services.NewPetSchedulerRuntime(petScheduler, petService)
	petSchedulerAPI := services.NewPetSchedulerAPIForPet(petScheduler, services.DefaultPetID, petDAO, petJobStore)

	// 【修复】第三步：创建服务（现在可以安全使用数据库了）
	suiService, errt := services.NewSuiStore()
	if errt != nil {
		log.Fatalf("SuiStore 初始化失败: %v", errt)
	}

	providerService := services.NewProviderService()
	mcpService := services.NewMCPService()
	settingsService := services.NewSettingsService()
	autoStartService := services.NewAutoStartService()
	projectManagerService := services.NewProjectManagerServiceWithPetAgentModelReader(petDAO)
	// 宠物绑定保存的是 projectId；解析必须回到 ProjectManager 的项目列表取当前路径，
	// 这样项目重命名或旧迁移数据不会让 Codex runtime 启动到过期目录。
	petProjectWorkspaceResolver := services.NewPetProjectWorkspaceResolver(petDAO, projectManagerService)
	appSettings := services.NewAppSettingsService(autoStartService, projectManagerService)
	notificationService := services.NewNotificationService(appSettings) // 通知服务
	blacklistService := services.NewBlacklistService(settingsService, notificationService)
	// 正常启动继续使用固定默认端口；验证副本可以通过环境变量切换到独立端口，
	// 避免为了测试最新 bundle 去抢占仍在运行的旧实例 relay。
	providerRelayAddr := strings.TrimSpace(os.Getenv("CODESWITCH_PROVIDER_RELAY_ADDR"))
	if providerRelayAddr == "" {
		providerRelayAddr = "127.0.0.1:18100"
	}
	geminiService := services.NewGeminiService(providerRelayAddr)
	// 频道和桌面客户端都复用同一个 Relay；频道只读取 Codex 默认模型，
	// Provider 选择、项目路由、降级和轮询继续由 Relay 统一处理。
	providerRelay := services.NewProviderRelayService(
		providerService,
		geminiService,
		blacklistService,
		notificationService,
		appSettings,
		providerRelayAddr,
	)
	providerRelay.SetActivityEmitter(petActivityEmitter)
	petAIProviderReader := services.NewPetAIProviderReader(providerService, geminiService)
	petAIEventEmitter := services.PetAIEventEmitterFunc(func(event services.PetAIEvent) error {
		// 心跳只等待 AI 终态，不依赖 Wails coalescer；终态必须在所有 UI
		// 旁路之前进入 broker，避免快速完成的 Codex turn 丢失唤醒信号。
		petAICompletionBroker.Publish(event)
		petBrowserEvents.Publish("pet.ai", event)
		if event.Type != services.PetAIEventDelta || event.Sequence <= 2 {
			services.WriteRuntimeDiagnosticAsync(
				"pet-ai-event",
				fmt.Sprintf("type=%q", event.Type),
				fmt.Sprintf("pet_id=%q", event.PetID),
				fmt.Sprintf("request_id=%q", event.RequestID),
				fmt.Sprintf("sequence=%d", event.Sequence),
				fmt.Sprintf("delta_bytes=%d", len(event.Delta)),
				fmt.Sprintf("text_bytes=%d", len(event.Text)),
				fmt.Sprintf("app_ready=%t", petApp != nil),
			)
		}
		if event.Type == services.PetAIEventUsage {
			// usage 账本使用 requestId 作为稳定幂等键；入账失败只记录日志，
			// 不能因为经验或 canonical pricing 异常把已经完成的聊天变成失败。
			usageEvent, err := event.Usage.ToPetUsageEvent()
			if err != nil {
				log.Printf("⚠️ 宠物 AI usage 载荷无效: %v", err)
			} else {
				petID := strings.TrimSpace(event.PetID)
				if petID == "" {
					log.Printf("⚠️ 宠物 AI usage 缺少 petId")
				} else {
					petUsageServicesMu.Lock()
					usageService := petUsageServices[petID]
					if usageService == nil {
						usageService = services.NewPetServiceForPet(petDAO, petID)
						petUsageServices[petID] = usageService
					}
					petUsageServicesMu.Unlock()
					if _, err := usageService.AddExperienceFromUsage(usageEvent); err != nil {
						log.Printf("⚠️ 宠物 AI usage 经验入账失败: %v", err)
					}
				}
			}
		}
		// 记忆指令只允许从完成事件落盘；流式 delta 可能仍处于未闭合状态，
		// 提前写入会把半截内部协议当成长期事实。
		if event.Type == services.PetAIEventCompleted {
			facts := services.ExtractPetMemoryDirectives(event.Text)
			if len(facts) > 0 {
				memoryService := services.NewPetMemoryServiceForPet(petDAO, event.PetID)
				if _, err := memoryService.Append(facts); err != nil {
					log.Printf("⚠️ 保存宠物 AI 记忆失败: %v", err)
				}
			}
		}
		petAIWailsEvents.Submit(event)
		return nil
	})
	petAIService := services.NewPetAIServiceWithDependencies(services.PetAIDependencies{
		ProviderReader:        petAIProviderReader,
		SpeechSelectionReader: appSettings,
		WorkspaceResolver:     petProjectWorkspaceResolver,
		Transport:             http.DefaultTransport,
		ActivityEmitter:       petActivityEmitter,
		Emitter:               petAIEventEmitter,
		// 音频 chunk 与文本事件共用同一生命周期，但单独使用 pet.audio 事件，
		// 前端可以在取消时丢弃旧 request 的 PCM，不会把二进制塞进文本事件。
		AudioEmitter: services.PetAudioEventEmitterFunc(func(event services.PetAudioEvent) error {
			petBrowserEvents.Publish("pet.audio", event)
			if petApp != nil {
				petApp.Event.Emit("pet.audio", event)
			}
			return nil
		}),
	})
	// 频道使用独立数据库；旧 OpenCowork 配置只导入一次，项目绑定则每次从当前
	// ProjectManager 事实源解析，避免两个应用的项目 ID 漂移。
	channelStore, channelStoreErr := channels.OpenDefaultStore()
	if channelStoreErr != nil {
		log.Fatalf("频道数据库初始化失败: %v", channelStoreErr)
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		importReport, importErr := channelStore.ImportOpenCoworkOnce(filepath.Join(home, ".open-cowork", "plugins.json"))
		if importErr != nil {
			log.Printf("⚠️ OpenCowork 频道配置导入失败: %v", importErr)
		} else if importReport.Imported > 0 || importReport.Templates > 0 {
			log.Printf("✅ OpenCowork 频道配置导入完成：插件=%d 模板=%d 跳过=%d", importReport.Imported, importReport.Templates, importReport.Skipped)
		}
	}
	channelProjects := func() ([]channels.ProjectBinding, error) {
		projects, err := projectManagerService.ListProjects()
		if err != nil {
			return nil, err
		}
		bindings := make([]channels.ProjectBinding, 0, len(projects))
		for _, project := range projects {
			name := strings.TrimSpace(project.DisplayName)
			if name == "" {
				name = strings.TrimSpace(project.SourceName)
			}
			bindings = append(bindings, channels.ProjectBinding{ID: project.ID, Path: project.Path, Name: name})
		}
		return bindings, nil
	}
	if _, ensureErr := channelStore.EnsureBuiltinInstances(); ensureErr != nil {
		log.Printf("⚠️ 频道内置实例补齐失败: %v", ensureErr)
	}
	channelProjectResolve := func(ctx context.Context, projectID string) (string, error) {
		bindings, err := channelProjects()
		if err != nil {
			return "", err
		}
		for _, project := range bindings {
			if project.ID == projectID || strings.EqualFold(strings.TrimSpace(project.Path), strings.TrimSpace(projectID)) {
				return project.Path, nil
			}
		}
		return "", fmt.Errorf("bound project %q was not found", projectID)
	}
	// 两个入口只共享项目级 thread，不共享各自提交上来的 persona。这里把
	// 宠物设置作为唯一人格事实源，避免频道旧配置或前端兼容字段改变 thread
	// fingerprint；读取失败通过请求终态报告，不能悄悄退回另一份人格。
	agentPersonaResolver := services.AgentConversationPersonaResolverFunc(func(ctx context.Context, _ string, petID string) (string, error) {
		petID = strings.TrimSpace(petID)
		if petID == "" {
			petID = services.DefaultPetID
		}
		persona, err := petDAO.LoadAgentPersona(ctx, petID)
		if err != nil {
			return "", err
		}
		return services.BuildPetAgentPersona(persona.SystemPrompt, persona.PetName), nil
	})
	channelEventSink := func(event channels.ChannelEvent) {
		petBrowserEvents.Publish("channels.event", event)
		if petApp != nil {
			petApp.Event.Emit("channels.event", event)
		}
	}
	var agentHub *services.AgentConversationHub
	var channelRuntime *channels.AgentRuntime
	channelManager := channels.NewManager(channelStore, func(event channels.ChannelEvent) {
		if channelRuntime != nil {
			channelRuntime.HandleEvent(event)
			return
		}
		channelEventSink(event)
	})
	petProjectWorkspaceByID := services.ProjectWorkspaceResolverFunc(func(ctx context.Context, projectID string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return channelProjectResolve(ctx, projectID)
	})
	petCodexRuntime := services.NewPetCodexRuntime(services.PetCodexRuntimeDependencies{
		Sessions:                 petDAO,
		AgentSessions:            petDAO,
		AgentModelReader:         petDAO,
		WorkspaceResolver:        petProjectWorkspaceResolver,
		ProjectWorkspaceResolver: petProjectWorkspaceByID,
		Emitter: services.PetAIEventEmitterFunc(func(event services.PetAIEvent) error {
			if agentHub != nil {
				return agentHub.Emit(event)
			}
			return petAIEventEmitter.Emit(event)
		}),
		ActivityEmitter: petActivityEmitter,
		DynamicToolProvider: channels.NewChannelCodexDynamicToolProvider(
			channelStore,
			channelManager,
			channelEventSink,
		),
		// 共享 Codex runtime 的 thread/turn 来源必须登记到项目管理器状态服务，
		// Hook 才能区分 Agent 管家和具体频道，避免按“最近项目”误投递。
		CodexHookSourceRegistrar: projectManagerService,
		// 微信入站图片落在频道数据库旁的受控目录；Codex 只允许读取这一路径，
		// 不能因为请求携带了本地路径就获得任意文件读取能力。
		LocalImageRoots: []string{channelStore.MediaRoot()},
		Executable:      strings.TrimSpace(os.Getenv("CODESWITCH_CODEX_EXECUTABLE")),
	})
	agentHub = services.NewAgentConversationHub(petCodexRuntime, services.AgentConversationHubOptions{
		PersonaResolver: agentPersonaResolver,
		Emitter: services.PetAIEventEmitterFunc(func(event services.PetAIEvent) error {
			// Hub 是事件的唯一公共出口；先送入完成 broker 和 UI，再让频道入口
			// 处理原频道的流式投递，二者都不能反向启动第二个 Codex runtime。
			err := petAIEventEmitter.Emit(event)
			if channelRuntime != nil {
				if channelErr := channelRuntime.Emit(event); err == nil {
					err = channelErr
				}
			}
			return err
		}),
		Broadcaster: services.AgentChannelBroadcasterFunc(func(ctx context.Context, projectID, text, requestID string) []services.AgentDeliveryResult {
			if channelRuntime == nil {
				return []services.AgentDeliveryResult{{ProjectID: projectID, Error: "channel runtime is unavailable"}}
			}
			return channelRuntime.BroadcastProject(ctx, projectID, text, requestID)
		}),
	})
	channelRuntime = channels.NewAgentRuntime(
		channelStore,
		channelManager,
		channelProjectResolve,
		channelEventSink,
		channels.AgentRuntimeOptions{
			ChatRuntime:       agentHub,
			SharedChatRuntime: true,
		},
	)
	projectManagerService.SetCodexHookNotificationSink(func(ctx context.Context, notification services.CodexHookNotification) error {
		if channelRuntime == nil {
			return fmt.Errorf("channel runtime is unavailable")
		}
		return channelRuntime.DeliverCodexHookNotification(ctx, notification)
	})
	channelService := channels.NewChannelService(channelStore, channelManager, channelProjects, channelEventSink)
	petAIAPIService := services.NewPetAIAPIServiceWithChatRuntime(petAIService, agentHub)
	petHeartbeatService := services.NewPetHeartbeatService(services.PetHeartbeatDependencies{
		PetID:             services.DefaultPetID,
		Pet:               petService,
		Repository:        petDAO,
		WorkspaceResolver: petProjectWorkspaceResolver,
		ChatRunner:        petAIAPIService,
		Completions:       petAICompletionBroker,
		Emitter: services.PetHeartbeatEmitterFunc(func(event services.PetHeartbeatEvent) error {
			petBrowserEvents.Publish("pet.heartbeat", event)
			if petApp != nil {
				petApp.Event.Emit("pet.heartbeat", event)
			}
			return nil
		}),
	})
	petHeartbeatAPI := services.NewPetHeartbeatAPI(petHeartbeatService)
	petImageService := services.NewPetImageService(petAIProviderReader, http.DefaultTransport)
	petImageAPIService := services.NewPetImageAPIService(petImageService)
	petMediaAPIService := services.NewPetMediaAPIService()
	// Studio 的资源目录和皮肤记录必须共享同一个 PetDAO；否则前端保存后桌宠快照无法看到新皮肤。
	petStudioAPIService := services.NewPetStudioAPIService(petDAO)
	claudeSettings := services.NewClaudeSettingsService(providerRelay.Addr())
	codexSettings := services.NewCodexSettingsService(providerRelay.Addr())
	cliConfigService := services.NewCliConfigService(providerRelay.Addr())
	logService := services.NewLogService()
	skillService := services.NewSkillService()
	promptService := services.NewPromptService()
	importService := services.NewImportService(providerService, mcpService)
	deeplinkService := services.NewDeepLinkService(providerService)
	dockService := dock.New()
	versionService := NewVersionService()
	updateService := services.NewUpdateService(AppVersion)
	consoleService := services.NewConsoleService()
	customCliService := services.NewCustomCliService(providerRelay.Addr())
	networkService := services.NewNetworkService(providerRelay.Addr(), claudeSettings, codexSettings, geminiService)
	radarService := services.NewRadarService()
	codexHookEnabled := true
	if settings, settingsErr := appSettings.GetAppSettings(); settingsErr != nil {
		// 设置文件不可读时保持旧行为，避免启动后静默关闭项目管理状态监控。
		log.Printf("读取 Codex Hook 设置失败，按默认开启处理: %v", settingsErr)
	} else {
		codexHookEnabled = settings.EnableCodexHook
	}

	go func() {
		if err := providerRelay.Start(); err != nil {
			log.Printf("provider relay start error: %v", err)
		}
	}()

	// 启动黑名单自动恢复定时器（每分钟检查一次）
	blacklistStopChan := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := blacklistService.AutoRecoverExpired(); err != nil {
					log.Printf("自动恢复黑名单失败: %v", err)
				}
			case <-blacklistStopChan:
				log.Println("✅ 黑名单定时器已停止")
				return
			}
		}
	}()

	//fmt.Println(clipboardService)
	// Create a new Wails application by providing the necessary options.
	// Variables 'Name' and 'Description' are for application metadata.
	// 'Assets' configures the asset server with the 'FS' variable pointing to the frontend files.
	// 'Bind' is a list of Go struct instances. The frontend has access to the methods of these instances.
	// 'Mac' options tailor the application when running an macOS.
	app := application.New(application.Options{
		Name:        "AI Code Studio",
		Description: "Claude Code and Codex provier manager",
		Windows: application.WindowsOptions{
			// Wails alpha.38 默认会让带 WS_EX_NOACTIVATE 的窗口按普通窗口
			// 处理 WM_MOUSEACTIVATE；宠物 driver 在这里收敛原生防激活规则，
			// 不影响主窗口的正常 Focus() 和终端交互。
			WndProcInterceptor: services.PetWindowWndProcInterceptor,
		},
		Services: []application.Service{
			application.NewService(appservice),
			application.NewService(suiService),
			application.NewService(providerService),
			application.NewService(mcpService),
			application.NewService(settingsService),
			application.NewService(blacklistService),
			application.NewService(claudeSettings),
			application.NewService(codexSettings),
			application.NewService(cliConfigService),
			application.NewService(logService),
			application.NewService(appSettings),
			application.NewService(skillService),
			application.NewService(promptService),
			application.NewService(importService),
			application.NewService(deeplinkService),
			application.NewService(dockService),
			application.NewService(versionService),
			application.NewService(updateService),
			application.NewService(geminiService),
			application.NewService(consoleService),
			application.NewService(customCliService),
			application.NewService(networkService),
			application.NewService(radarService),
			application.NewService(projectManagerService),
			application.NewService(channelService),
			application.NewService(petService),
			application.NewService(petMemoryService),
			application.NewService(petDreamAPIService),
			application.NewService(petAIAPIService),
			application.NewService(petHeartbeatAPI),
			application.NewService(petImageAPIService),
			application.NewService(petMediaAPIService),
			application.NewService(petStudioAPIService),
			application.NewService(petSchedulerAPI),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})
	petApp = app
	if err := petHeartbeatService.Start(context.Background()); err != nil {
		log.Printf("⚠️ 宠物心跳服务启动失败: %v", err)
	} else {
		log.Println("✅ 宠物心跳服务已启动")
	}
	go func() {
		started, failed, err := channelService.StartAuto()
		if err != nil {
			log.Printf("⚠️ 频道自动启动失败: %v", err)
			return
		}
		if len(started) > 0 || len(failed) > 0 {
			log.Printf("✅ 频道自动启动完成：成功=%d 失败=%d", len(started), len(failed))
		}
	}()
	// 宠物自动照料由运行时统一编排：先结算离线衰减和 away 奖励，再按规则尝试
	// 一次动作，并把结果广播给桌宠窗口。心跳间隔与源项目保持 30 秒。
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if _, err := petRuntime.Tick(); err != nil {
					log.Printf("宠物运行时心跳失败: %v", err)
				}
				// 调度器与自动照料共用这一条心跳，避免两个 ticker 同时抢占
				// SQLite lease；runtime 只负责编排，提醒仍由 scheduler emitter 投递。
				schedulerResult, err := petSchedulerRuntime.RunOnce(context.Background())
				if err != nil {
					log.Printf("宠物计划调度心跳失败: %v", err)
				}
				petBrowserEvents.Publish("pet.scheduler", schedulerResult)
				for _, action := range schedulerResult.Actions {
					petBrowserEvents.Publish("pet.action", action)
				}
				if petApp != nil {
					petApp.Event.Emit("pet.scheduler", schedulerResult)
					for _, action := range schedulerResult.Actions {
						petApp.Event.Emit("pet.action", action)
					}
				}
			case <-petStopChan:
				log.Println("✅ 宠物运行时心跳已停止")
				return
			}
		}
	}()

	var petWindow *services.PetWindow
	petWindow, petWindowErr := services.NewPetWindow(app, services.PetWindowOptions{
		Name:  services.DefaultPetWindowName,
		Title: services.DefaultPetWindowTitle,
		// 与 OpenCowork 的 appView=pet 入口保持一致；独立窗口必须绕过主应用
		// App.vue，否则会把 Sidebar 和普通路由布局一起渲染进透明桌宠窗。
		URL:    "/?appView=pet",
		Width:  services.DefaultPetWindowWidth,
		Height: services.DefaultPetWindowHeight,
	})
	if petWindowErr != nil {
		services.WriteRuntimeDiagnostic("pet-window-create-failed", fmt.Sprintf("err=%q", petWindowErr.Error()))
		log.Printf("⚠️ 宠物窗口初始化失败: %v", petWindowErr)
	} else {
		// 桌宠不再永久置顶；打开后由平台运行时根据当前站立窗口同步 Z 序。

		windowConfig, configErr := petService.GetWindowConfig()
		if configErr != nil {
			services.WriteRuntimeDiagnostic("pet-window-config-read-failed", fmt.Sprintf("err=%q", configErr.Error()))
			log.Printf("⚠️ 读取宠物窗口配置失败: %v", configErr)
		} else if windowConfig.Enabled {
			services.WriteRuntimeDiagnostic("pet-window-enabled", "enabled=true")
			// 与稳定主线保持一致：等 Wails 应用进入 Started 状态后直接打开桌宠。
			// 不把桌宠绑定到主窗口导航完成事件，避免两个 WebView 的生命周期互相等待。
			app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
				if err := petWindow.Open(); err != nil {
					log.Printf("⚠️ 打开宠物窗口失败: %v", err)
				}
			})
		} else {
			services.WriteRuntimeDiagnostic("pet-window-enabled", "enabled=false")
		}
	}
	// PetWindow 依赖已经创建好的原生窗口，故在 app 创建后再注册；服务仍在
	// Wails 启动前完成绑定，前端不会看到“设置已保存但窗口方法不存在”的假状态。
	petWindowAPI := services.NewPetWindowAPI(petWindow)
	app.RegisterService(application.NewService(petWindowAPI))

	// 浏览器预览没有 Wails 原生消息桥；loopback bridge 让设置页能够复用同一套
	// PetService/SQLite，而不是退回到只存在于当前 tab 内存里的 fallback 快照。
	// 只监听 127.0.0.1，且 handler 内部仍按宠物页面白名单分发，不暴露通用 Wails RPC。
	petBrowserBridgeAddr := strings.TrimSpace(os.Getenv("CODESWITCH_PET_BRIDGE_ADDR"))
	if petBrowserBridgeAddr == "" {
		petBrowserBridgeAddr = services.DefaultPetBrowserBridgeAddr
	}
	petBrowserBridge = services.NewPetBrowserBridge(services.PetBrowserBridgeDependencies{
		Pet:         petService,
		Memory:      petMemoryService,
		Dream:       petDreamAPIService,
		AI:          petAIAPIService,
		Image:       petImageAPIService,
		Media:       petMediaAPIService,
		Studio:      petStudioAPIService,
		Window:      petWindowAPI,
		Provider:    providerService,
		Gemini:      geminiService,
		Project:     projectManagerService,
		AppSettings: appSettings,
		Scheduler:   petSchedulerAPI,
		Heartbeat:   petHeartbeatAPI,
		Channels:    channelBrowserBridgeAdapter{service: channelService},
		Events:      petBrowserEvents,
	}, services.PetBrowserBridgeOptions{Addr: petBrowserBridgeAddr})
	if err := petBrowserBridge.Start(); err != nil {
		log.Printf("⚠️ 宠物浏览器 bridge 启动失败: %v", err)
	} else {
		log.Printf("✅ 宠物浏览器 bridge 已启动: http://%s", petBrowserBridge.Addr())
	}

	// 设置 NotificationService 的 App 引用，用于发送事件到前端
	notificationService.SetApp(app)
	// 设置 UpdateService 的 App 引用，用于发送更新事件
	updateService.SetApp(app)
	// 状态监控只在 GUI 主进程中运行；Hook 子进程只负责落盘事件。
	projectManagerService.SetApp(app)
	projectManagerService.StartCodexStatusMonitor(codexHookEnabled)

	var shutdownOnce sync.Once
	app.OnShutdown(func() {
		shutdownOnce.Do(func() {
			log.Println("🛑 应用正在关闭，停止后台服务...")
			// Wails 的 OnShutdown 是同步回调；所有阶段共用一个总预算，避免
			// bridge、provider、Codex 和数据库各自拿一份 timeout 后叠加成假死。
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer shutdownCancel()
			runShutdownStage := func(name string, action func(context.Context) error) error {
				if err := shutdownCtx.Err(); err != nil {
					services.WriteRuntimeDiagnostic("shutdown-stage-skipped", fmt.Sprintf("stage=%q error=%q", name, err.Error()))
					return err
				}
				startedAt := time.Now()
				services.WriteRuntimeDiagnostic("shutdown-stage-start", fmt.Sprintf("stage=%q", name))
				done := make(chan error, 1)
				go func() { done <- action(shutdownCtx) }()
				select {
				case err := <-done:
					if err != nil {
						log.Printf("⚠️ 关闭阶段 %s 失败: %v", name, err)
						services.WriteRuntimeDiagnostic("shutdown-stage-error", fmt.Sprintf("stage=%q error=%q", name, err.Error()))
					} else {
						services.WriteRuntimeDiagnostic("shutdown-stage-complete", fmt.Sprintf("stage=%q elapsed_ms=%d", name, time.Since(startedAt).Milliseconds()))
					}
					return err
				case <-shutdownCtx.Done():
					log.Printf("⚠️ 关闭阶段 %s 超时，继续收口: %v", name, shutdownCtx.Err())
					services.WriteRuntimeDiagnostic("shutdown-stage-timeout", fmt.Sprintf("stage=%q elapsed_ms=%d", name, time.Since(startedAt).Milliseconds()))
					return shutdownCtx.Err()
				}
			}
			runMainThreadShutdownStage := func(name string, action func(context.Context) error) error {
				if err := shutdownCtx.Err(); err != nil {
					services.WriteRuntimeDiagnostic("shutdown-stage-skipped", fmt.Sprintf("stage=%q error=%q", name, err.Error()))
					return err
				}
				startedAt := time.Now()
				services.WriteRuntimeDiagnostic("shutdown-stage-start", fmt.Sprintf("stage=%q", name))
				// OnShutdown 本身由 Wails 在主线程同步调用；窗口 API 内部还会
				// InvokeSync 回主线程。这里必须直接执行，否则主线程等待 goroutine、
				// goroutine 等待主线程，退出就会稳定卡满总预算。
				err := action(shutdownCtx)
				if err != nil {
					log.Printf("⚠️ 关闭阶段 %s 失败: %v", name, err)
					services.WriteRuntimeDiagnostic("shutdown-stage-error", fmt.Sprintf("stage=%q error=%q", name, err.Error()))
					return err
				}
				if deadlineErr := shutdownCtx.Err(); deadlineErr != nil {
					log.Printf("⚠️ 关闭阶段 %s 超时，继续收口: %v", name, deadlineErr)
					services.WriteRuntimeDiagnostic("shutdown-stage-timeout", fmt.Sprintf("stage=%q elapsed_ms=%d", name, time.Since(startedAt).Milliseconds()))
					return deadlineErr
				}
				services.WriteRuntimeDiagnostic("shutdown-stage-complete", fmt.Sprintf("stage=%q elapsed_ms=%d", name, time.Since(startedAt).Milliseconds()))
				return nil
			}

			if petBrowserBridge != nil {
				_ = runShutdownStage("pet-browser-bridge", func(ctx context.Context) error {
					return petBrowserBridge.Stop(ctx)
				})
			}
			if petWindow != nil {
				_ = runMainThreadShutdownStage("pet-window", func(context.Context) error {
					return petWindow.Close()
				})
			}

			// Hub 同时拥有 Agent 管家和频道入口的共享 Codex runtime；只在这里
			// 关闭一次，频道适配器本身不再拥有任何 app-server 进程。
			_ = runShutdownStage("agent-conversation-hub", func(context.Context) error {
				return agentHub.Close()
			})

			// provider 可能持有平台长轮询、WebSocket 和 heartbeat；Manager 内部
			// 已按实例并行停止，这里只负责给它一个统一的退出阶段和预算。
			channelStopErr := runShutdownStage("channel-providers", func(ctx context.Context) error {
				return channelManager.StopAll(ctx)
			})
			if channelStopErr == nil || shutdownCtx.Err() == nil {
				_ = runShutdownStage("channel-store", func(context.Context) error {
					return channelStore.Close()
				})
			}

			_ = runShutdownStage("codex-status-monitor", func(context.Context) error {
				projectManagerService.StopCodexStatusMonitor()
				return nil
			})
			_ = runShutdownStage("pet-heartbeat", func(ctx context.Context) error {
				return petHeartbeatService.Close(ctx)
			})
			_ = runShutdownStage("pet-ai-event-dispatcher", func(context.Context) error {
				petAIWailsEvents.Close()
				return nil
			})
			_ = runShutdownStage("background-timers", func(context.Context) error {
				close(blacklistStopChan)
				close(petStopChan)
				return nil
			})
			_ = runShutdownStage("provider-relay", func(context.Context) error {
				return providerRelay.Stop()
			})

			queueTimeout := 10 * time.Second
			if deadline, ok := shutdownCtx.Deadline(); ok {
				remaining := time.Until(deadline)
				if remaining < queueTimeout*2 {
					queueTimeout = remaining / 2
				}
				if queueTimeout < 0 {
					queueTimeout = 0
				}
			}
			queueErr := runShutdownStage("db-queues", func(context.Context) error {
				return services.ShutdownGlobalDBQueue(queueTimeout)
			})
			if queueErr == nil {
				stats1 := services.GetGlobalDBQueueStats()
				log.Printf("✅ 单次队列已关闭，统计：成功=%d 失败=%d 平均延迟=%.2fms", stats1.SuccessWrites, stats1.FailedWrites, stats1.AvgLatencyMs)
				stats2 := services.GetGlobalDBQueueLogsStats()
				log.Printf("✅ 批量队列已关闭，统计：成功=%d 失败=%d 平均延迟=%.2fms（批均分） 批次=%d", stats2.SuccessWrites, stats2.FailedWrites, stats2.AvgLatencyMs, stats2.BatchCommits)
			}

			if shutdownCtx.Err() != nil {
				log.Printf("⚠️ 应用关闭达到总预算: %v", shutdownCtx.Err())
				services.WriteRuntimeDiagnostic("shutdown-timeout", fmt.Sprintf("error=%q", shutdownCtx.Err().Error()))
			} else {
				log.Println("✅ 所有后台服务已停止")
				services.WriteRuntimeDiagnostic("shutdown-complete")
			}
		})
	})

	// Create a new window with the necessary options.
	// 'Title' is the title of the window.
	// 'Mac' options tailor the window when running on macOS.
	// 'BackgroundColour' is the background colour of the window.
	// 'URL' is the URL that will be loaded into the webview.
	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Code Switch CLI",
		Width:     1400,
		Height:    1040,
		MinWidth:  600,
		MinHeight: 300,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(27, 38, 54),
		URL:              "/",
	})
	appservice.MainWindow = mainWindow
	var mainWindowCentered bool
	focusMainWindow := func() {
		if runtime.GOOS == "windows" {
			mainWindow.SetAlwaysOnTop(true)
			mainWindow.Focus()
			go func() {
				time.Sleep(150 * time.Millisecond)
				mainWindow.SetAlwaysOnTop(false)
			}()
			return
		}
		mainWindow.Focus()
	}
	showMainWindow := func(withFocus bool) {
		if !mainWindowCentered {
			mainWindow.Center()
			mainWindowCentered = true
		}
		if mainWindow.IsMinimised() {
			mainWindow.UnMinimise()
		}
		mainWindow.Show()
		if withFocus {
			focusMainWindow()
		}
		handleDockVisibility(dockService, true)
	}

	// 首屏保持主线启动时序：创建窗口后立即交给 Wails 显示，不插入隐藏或导航接管。
	showMainWindow(false)

	mainWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		mainWindow.Hide()
		handleDockVisibility(dockService, false)
		e.Cancel()
	})

	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(event *application.ApplicationEvent) {
		showMainWindow(true)
	})

	app.Event.OnApplicationEvent(events.Mac.ApplicationDidBecomeActive, func(event *application.ApplicationEvent) {
		// 托盘菜单不再依赖独立 WebView；应用被激活时只聚焦已经可见的主窗口，
		// 避免用户从托盘操作时意外把被隐藏的主窗口重新弹出。
		if mainWindow.IsVisible() {
			mainWindow.Focus()
		}
	})

	systray := app.SystemTray.New()
	// systray.SetLabel("AI Code Studio")
	systray.SetTooltip("AI Code Studio")
	if lightIcon := loadTrayIcon("assets/icon.png"); len(lightIcon) > 0 {
		systray.SetIcon(lightIcon)
	}
	if darkIcon := loadTrayIcon("assets/icon-dark.png"); len(darkIcon) > 0 {
		systray.SetDarkModeIcon(darkIcon)
	}

	// 菜单只在启动时创建一次。右键回调不得读取日志、配置或重建菜单，
	// 否则系统原生菜单会被磁盘/数据库 I/O 阻塞，直接表现为“右键很慢”。
	trayMenu := application.NewMenu()
	trayMenu.Add("显示主窗口").OnClick(func(ctx *application.Context) {
		showMainWindow(true)
	})
	trayMenu.Add("退出").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	systray.SetMenu(trayMenu)
	systray.OnRightClick(func() {
		systray.OpenMenu()
	})
	systray.OnClick(func() {
		if !mainWindow.IsVisible() {
			showMainWindow(true)
			return
		}
		if !mainWindow.IsFocused() {
			focusMainWindow()
		}
	})

	appservice.SetApp(app)

	// Create a goroutine that emits an event containing the current time every second.
	// The frontend can listen to this event and update the UI accordingly.
	go func() {
		// for {
		// 	now := time.Now().Format(time.RFC1123)
		// 	app.EmitEvent("time", now)
		// 	time.Sleep(time.Second)
		// }
	}()

	// Run the application. This blocks until the application has been exited.
	services.WriteRuntimeDiagnostic("app-run-enter")
	err = app.Run()
	services.WriteRuntimeDiagnostic("app-run-return", fmt.Sprintf("err=%q", errString(err)))

	// If an error occurred while running the application, log it and exit.
	if err != nil {
		log.Fatal(err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func loadTrayIcon(path string) []byte {
	data, err := trayIcons.ReadFile(path)
	if err != nil {
		log.Printf("failed to load tray icon %s: %v", path, err)
		return nil
	}
	return data
}

func handleDockVisibility(service *dock.DockService, show bool) {
	if runtime.GOOS != "darwin" || service == nil {
		return
	}
	if show {
		service.ShowAppIcon()
	} else {
		service.HideAppIcon()
	}
}
