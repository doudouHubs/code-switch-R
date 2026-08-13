package main

import (
	"codeswitch/services"
	"context"
	"embed"
	_ "embed"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

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
	TrayWindow application.Window
}

func (a *AppService) SetApp(app *application.App) {
	a.App = app
}

func (a *AppService) SetTrayWindowHeight(height int) {
	if runtime.GOOS != "darwin" || a.TrayWindow == nil {
		return
	}
	if height < trayWindowMinHeight {
		height = trayWindowMinHeight
	}
	if height > trayWindowMaxHeight {
		height = trayWindowMaxHeight
	}
	a.TrayWindow.SetSize(trayWindowWidth, height)
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
	// Codex Hook 会同步启动当前 EXE；必须在数据库和 Wails 初始化前走轻量分支，
	// 否则一次状态事件就会拉起一个完整 CodeSwitch 实例并阻塞 Codex。
	if services.IsProjectManagerCodexHookInvocation(os.Args[1:]) {
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
	appSettings := services.NewAppSettingsService(autoStartService)
	notificationService := services.NewNotificationService(appSettings) // 通知服务
	blacklistService := services.NewBlacklistService(settingsService, notificationService)
	geminiService := services.NewGeminiService("127.0.0.1:18100")
	petAIProviderReader := services.NewPetAIProviderReader(providerService, geminiService)
	petAIService := services.NewPetAIServiceWithDependencies(services.PetAIDependencies{
		ProviderReader:        petAIProviderReader,
		SpeechSelectionReader: appSettings,
		WorkspaceResolver:     petDAO,
		Transport:             http.DefaultTransport,
		Emitter: services.PetAIEventEmitterFunc(func(event services.PetAIEvent) error {
			petBrowserEvents.Publish("pet.ai", event)
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
			if petApp != nil {
				petApp.Event.Emit("pet.ai", event)
			}
			return nil
		}),
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
	petAIAPIService := services.NewPetAIAPIService(petAIService)
	petImageService := services.NewPetImageService(petAIProviderReader, http.DefaultTransport)
	petImageAPIService := services.NewPetImageAPIService(petImageService)
	petMediaAPIService := services.NewPetMediaAPIService()
	// Studio 的资源目录和皮肤记录必须共享同一个 PetDAO；否则前端保存后桌宠快照无法看到新皮肤。
	petStudioAPIService := services.NewPetStudioAPIService(petDAO)
	// relay 复用主应用的 Gemini、黑名单、通知和轮询配置；这些依赖共同维持
	// provider 路由与请求日志的单一运行时状态，不能退回到宠物分支的简化代理。
	providerRelay := services.NewProviderRelayService(
		providerService,
		geminiService,
		blacklistService,
		notificationService,
		appSettings,
		":18100",
	)
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
	projectManagerService := services.NewProjectManagerService()

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
			application.NewService(petService),
			application.NewService(petMemoryService),
			application.NewService(petDreamAPIService),
			application.NewService(petAIAPIService),
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
		log.Printf("⚠️ 宠物窗口初始化失败: %v", petWindowErr)
	} else {
		// 置顶是运行时策略，不属于宠物配置；先明确关闭，避免透明窗口压住用户当前操作的应用。
		// 这里必须与配置读取、打开流程分开，设置失败不能短路后续初始化。
		if topmostErr := petWindow.SetAlwaysOnTop(false); topmostErr != nil {
			log.Printf("⚠️ 设置宠物窗口非置顶失败: %v", topmostErr)
		}

		windowConfig, configErr := petService.GetWindowConfig()
		if configErr != nil {
			log.Printf("⚠️ 读取宠物窗口配置失败: %v", configErr)
		} else if windowConfig.Enabled {
			// Wails 的 Screen 缓存要等 app.Run() 初始化平台后才可靠；在 ApplicationStarted
			// 后再打开窗口。具体 WorkArea 几何由 PetWindow driver 的唯一 Open owner 计算，
			// 这样启动和设置页重新开启不会各自维护一套贴底规则。
			app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
				if err := petWindow.Open(); err != nil {
					log.Printf("⚠️ 打开宠物窗口失败: %v", err)
				}
			})
		}
	}
	// PetWindow 依赖已经创建好的原生窗口，故在 app 创建后再注册；服务仍在
	// Wails 启动前完成绑定，前端不会看到“设置已保存但窗口方法不存在”的假状态。
	petWindowAPI := services.NewPetWindowAPI(petWindow)
	app.RegisterService(application.NewService(petWindowAPI))

	// 浏览器预览没有 Wails 原生消息桥；loopback bridge 让设置页能够复用同一套
	// PetService/SQLite，而不是退回到只存在于当前 tab 内存里的 fallback 快照。
	// 只监听 127.0.0.1，且 handler 内部仍按宠物页面白名单分发，不暴露通用 Wails RPC。
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
		Events:      petBrowserEvents,
	})
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
	projectManagerService.StartCodexStatusMonitor()

	app.OnShutdown(func() {
		log.Println("🛑 应用正在关闭，停止后台服务...")

		if petBrowserBridge != nil {
			bridgeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := petBrowserBridge.Stop(bridgeCtx); err != nil {
				log.Printf("⚠️ 宠物浏览器 bridge 关闭失败: %v", err)
			}
			cancel()
		}

		if petWindow != nil {
			if err := petWindow.Close(); err != nil {
				log.Printf("⚠️ 关闭宠物窗口失败: %v", err)
			}
		}

		// 1. 停止 Codex 状态监控，避免退出期间继续推送前端事件。
		projectManagerService.StopCodexStatusMonitor()

		// 2. 停止黑名单定时器
		close(blacklistStopChan)
		close(petStopChan)

		// 3. 停止代理服务器
		_ = providerRelay.Stop()

		// 4. 优雅关闭数据库写入队列（10秒超时，双队列架构）
		if err := services.ShutdownGlobalDBQueue(10 * time.Second); err != nil {
			log.Printf("⚠️ 队列关闭超时: %v", err)
		} else {
			// 单次队列统计
			stats1 := services.GetGlobalDBQueueStats()
			log.Printf("✅ 单次队列已关闭，统计：成功=%d 失败=%d 平均延迟=%.2fms",
				stats1.SuccessWrites, stats1.FailedWrites, stats1.AvgLatencyMs)

			// 批量队列统计
			stats2 := services.GetGlobalDBQueueLogsStats()
			log.Printf("✅ 批量队列已关闭，统计：成功=%d 失败=%d 平均延迟=%.2fms（批均分） 批次=%d",
				stats2.SuccessWrites, stats2.FailedWrites, stats2.AvgLatencyMs, stats2.BatchCommits)
		}

		log.Println("✅ 所有后台服务已停止")
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

	showMainWindow(false)

	mainWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		mainWindow.Hide()
		handleDockVisibility(dockService, false)
		e.Cancel()
	})

	var trayWindow application.Window

	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(event *application.ApplicationEvent) {
		showMainWindow(true)
	})

	app.Event.OnApplicationEvent(events.Mac.ApplicationDidBecomeActive, func(event *application.ApplicationEvent) {
		if trayWindow != nil {
			// Tray exists on macOS; avoid auto-opening the main window on activation.
			return
		}
		if mainWindow.IsVisible() {
			mainWindow.Focus()
			return
		}
		showMainWindow(true)
	})

	if runtime.GOOS == "darwin" {
		trayWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
			Title:            "Code Switch Tray",
			Name:             "tray",
			Width:            trayWindowWidth,
			Height:           trayWindowMinHeight,
			MinWidth:         trayWindowWidth,
			MaxWidth:         trayWindowWidth,
			MinHeight:        trayWindowMinHeight,
			MaxHeight:        trayWindowMaxHeight,
			AlwaysOnTop:      true,
			DisableResize:    true,
			Frameless:        true,
			Hidden:           true,
			BackgroundType:   application.BackgroundTypeTransparent,
			BackgroundColour: application.NewRGBA(0, 0, 0, 0),
			Mac: application.MacWindow{
				Backdrop:      application.MacBackdropTransparent,
				TitleBar:      application.MacTitleBarHidden,
				DisableShadow: true,
				WindowLevel:   application.MacWindowLevelPopUpMenu,
			},
			URL: "/#/tray",
		})
		trayWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			trayWindow.Hide()
			e.Cancel()
		})
		appservice.TrayWindow = trayWindow
	}

	systray := app.SystemTray.New()
	// systray.SetLabel("AI Code Studio")
	systray.SetTooltip("AI Code Studio")
	if lightIcon := loadTrayIcon("assets/icon.png"); len(lightIcon) > 0 {
		systray.SetIcon(lightIcon)
	}
	if darkIcon := loadTrayIcon("assets/icon-dark.png"); len(darkIcon) > 0 {
		systray.SetDarkModeIcon(darkIcon)
	}

	if runtime.GOOS == "darwin" && trayWindow != nil {
		trayMenu := application.NewMenu()
		trayMenu.Add("显示主窗口").OnClick(func(ctx *application.Context) {
			showMainWindow(true)
		})
		trayMenu.Add("退出").OnClick(func(ctx *application.Context) {
			app.Quit()
		})
		systray.SetMenu(trayMenu)
		systray.AttachWindow(trayWindow).WindowOffset(8)
		systray.OnRightClick(func() {
			systray.OpenMenu()
		})
	} else {
		refreshTrayMenu := func() {
			used, total := getTrayUsage(logService, appSettings)
			trayMenu := buildUsageTrayMenu(used, total, func() {
				showMainWindow(true)
			}, func() {
				app.Quit()
			})
			systray.SetMenu(trayMenu)
		}
		refreshTrayMenu()
		systray.OnRightClick(func() {
			refreshTrayMenu()
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
	}

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
	err = app.Run()

	// If an error occurred while running the application, log it and exit.
	if err != nil {
		log.Fatal(err)
	}
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

const (
	trayWindowWidth      = 360
	trayWindowMinHeight  = 120
	trayWindowMaxHeight  = 420
	trayProgressBarWidth = 28
)

func getTrayUsage(logService *services.LogService, appSettings *services.AppSettingsService) (float64, float64) {
	used := 0.0
	total := 0.0
	adjustment := 0.0
	if logService != nil {
		stats, err := logService.StatsSince("")
		if err == nil {
			used = stats.CostTotal
		}
	}
	if appSettings != nil {
		settings, err := appSettings.GetAppSettings()
		if err == nil {
			total = settings.BudgetTotal
			adjustment = settings.BudgetUsedAdjustment
		}
	}
	used += adjustment
	if used < 0 {
		used = 0
	}
	if total < 0 {
		total = 0
	}
	return used, total
}

func buildUsageTrayMenu(used float64, total float64, onShow func(), onQuit func()) *application.Menu {
	menu := application.NewMenu()
	menu.Add(trayUsageLabel(used, total)).SetEnabled(false)
	menu.Add(trayProgressLabel(used, total)).SetEnabled(false)
	menu.AddSeparator()
	menu.Add("显示主窗口").OnClick(func(ctx *application.Context) {
		onShow()
	})
	menu.Add("退出").OnClick(func(ctx *application.Context) {
		onQuit()
	})
	return menu
}

func trayUsageLabel(used float64, total float64) string {
	usedLabel := formatCurrency(used)
	if total <= 0 {
		return fmt.Sprintf("今日已用 %s / 未设置", usedLabel)
	}
	return fmt.Sprintf("今日已用 %s / %s", usedLabel, formatCurrency(total))
}

func trayProgressLabel(used float64, total float64) string {
	bar := strings.Repeat("-", trayProgressBarWidth)
	if total <= 0 {
		return fmt.Sprintf("进度 [%s] --%%", bar)
	}
	ratio := used / total
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(math.Round(ratio * float64(trayProgressBarWidth)))
	if filled < 0 {
		filled = 0
	}
	if filled > trayProgressBarWidth {
		filled = trayProgressBarWidth
	}
	bar = strings.Repeat("#", filled) + strings.Repeat("-", trayProgressBarWidth-filled)
	percent := int(math.Round(ratio * 100))
	return fmt.Sprintf("进度 [%s] %d%%", bar, percent)
}

func formatCurrency(value float64) string {
	return fmt.Sprintf("$%.2f", value)
}
