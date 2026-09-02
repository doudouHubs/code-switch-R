package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCodexAppServerFixture 是跨平台的 JSONL 子进程 fixture。它只在子进程环境
// 明确标记时运行，避免把测试逻辑混入正常测试进程；父测试仍然通过真实 exec.Cmd
// 验证 app-server client 的管道、退出和通知收口。
func TestCodexAppServerFixture(t *testing.T) {
	if os.Getenv("CODE_SWITCH_CODEX_FIXTURE") != "1" {
		return
	}
	if os.Getenv("CODE_SWITCH_CODEX_FIXTURE_CHILD") == "1" {
		// 子进程故意继承父进程 stdout 并保持句柄；这复现 Windows 下 cmd.exe
		// 被杀后 codex.exe 仍持有 JSONL 管道、导致 reader 永远收不到 EOF 的场景。
		if readyFile := os.Getenv("CODE_SWITCH_CODEX_READY_FILE"); readyFile != "" {
			_ = os.WriteFile(readyFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		time.Sleep(10 * time.Second)
		return
	}
	scenario := os.Getenv("CODE_SWITCH_CODEX_SCENARIO")
	if scenario == "" {
		scenario = "rpc"
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var pendingOriginalID json.RawMessage
	var pendingTurnStartID json.RawMessage
	turnStartCount := 0
	staleInterrupted := false
	threadWorkspace := ""
	fixtureThreadID := ""
	if scenario == "hold-stdout-child" {
		child := exec.Command(os.Args[0], "-test.run=TestCodexAppServerFixture")
		child.Env = append(os.Environ(),
			"CODE_SWITCH_CODEX_FIXTURE=1",
			"CODE_SWITCH_CODEX_SCENARIO=hold-stdout-child",
			"CODE_SWITCH_CODEX_FIXTURE_CHILD=1",
		)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		_ = child.Start()
	}
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return
		}

		if scenario == "rpc" || scenario == "rpc-handler" {
			if request.Method == "initialize" {
				writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(request.ID), "result": map[string]any{}})
				continue
			}
			if request.Method == "run" {
				pendingOriginalID = append(json.RawMessage(nil), request.ID...)
				writeCodexFixtureMessage(map[string]any{
					"method": "fixture/notice",
					"params": map[string]any{"value": "before-response"},
				})
				writeCodexFixtureMessage(map[string]any{
					"id":     900,
					"method": "approval/request",
					"params": map[string]any{"reason": "fixture"},
				})
				continue
			}
			if len(pendingOriginalID) > 0 && string(request.ID) == "900" && (len(request.Error) > 0 || len(request.Result) > 0) {
				if scenario == "rpc-handler" && len(request.Result) > 0 {
					var serverResult struct {
						Decision string `json:"decision"`
					}
					if json.Unmarshal(request.Result, &serverResult) == nil {
						writeCodexFixtureMessage(map[string]any{
							"method": "fixture/server-request-accepted",
							"params": map[string]any{"decision": serverResult.Decision},
						})
					}
					writeCodexFixtureMessage(map[string]any{
						"id":     json.RawMessage(pendingOriginalID),
						"result": map[string]any{"ok": true},
					})
					pendingOriginalID = nil
					continue
				}
				var serverError struct {
					Code int `json:"code"`
				}
				if json.Unmarshal(request.Error, &serverError) == nil {
					writeCodexFixtureMessage(map[string]any{
						"method": "fixture/server-request-rejected",
						"params": map[string]any{"code": serverError.Code},
					})
				}
				writeCodexFixtureMessage(map[string]any{
					"id":     json.RawMessage(pendingOriginalID),
					"result": map[string]any{"ok": true},
				})
				pendingOriginalID = nil
			}
			continue
		}

		if scenario == "exit" {
			if request.Method == "initialize" {
				writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(request.ID), "result": map[string]any{}})
				continue
			}
			return
		}

		handlePetCodexFixtureRequest(scenario, request.ID, request.Method, request.Params, request.Result, request.Error, &staleInterrupted, &pendingTurnStartID, &turnStartCount, &threadWorkspace, &fixtureThreadID)
	}
	if scenario == "hold-stdout-child" {
		// stdin 被父测试关闭后仍保持父进程存活，确保 Close 观察到的是“父子
		// 进程树同时存在”的真实窗口，而不是父进程先退出后的竞态。
		time.Sleep(10 * time.Second)
	}
}

func writeCodexFixtureMessage(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = os.Stdout.Write(append(data, '\n'))
}

func handlePetCodexFixtureRequest(scenario string, id json.RawMessage, method string, rawParams, rawResult, rawError json.RawMessage, staleInterrupted *bool, pendingTurnStartID *json.RawMessage, turnStartCount *int, threadWorkspace, fixtureThreadID *string) {
	if method == "initialize" {
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{}})
		if scenario == "pet-unknown-notification" {
			// 真实 app-server 会在握手期间发送不参与宠物业务的通知，
			// 用这个事件验证 runtime 忽略未知类型时不会遗留状态锁。
			writeCodexFixtureMessage(map[string]any{
				"method": "mcpServer/startupStatus/updated",
				"params": map[string]any{"status": "ready"},
			})
		}
		return
	}
	if scenario == "pet-dynamic-tool" && string(id) == "901" && (len(rawResult) > 0 || len(rawError) > 0) {
		// 工具响应必须先回到同一条 turn/start RPC，再继续发终态通知；这样
		// fixture 同时覆盖 server request 异步处理和 runtime 的 turn 归属。
		success := false
		if len(rawResult) > 0 {
			var result struct {
				Success bool `json:"success"`
			}
			success = json.Unmarshal(rawResult, &result) == nil && result.Success
		}
		if pendingTurnStartID != nil && len(*pendingTurnStartID) > 0 {
			pendingID := append(json.RawMessage(nil), (*pendingTurnStartID)...)
			*pendingTurnStartID = nil
			writeCodexFixtureMessage(map[string]any{
				"id":     pendingID,
				"result": map[string]any{"turn": map[string]any{"id": "active-turn"}},
			})
		}
		if success {
			writeCodexFixtureMessage(map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{"threadId": os.Getenv("CODE_SWITCH_CODEX_THREAD_ID"), "turnId": "active-turn", "delta": "动态工具已执行"},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": os.Getenv("CODE_SWITCH_CODEX_THREAD_ID"), "turn": map[string]any{"id": "active-turn", "status": "completed"}},
			})
		} else {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": os.Getenv("CODE_SWITCH_CODEX_THREAD_ID"), "turn": map[string]any{"id": "active-turn", "status": "failed"}},
			})
		}
		return
	}
	if strings.HasPrefix(scenario, "pet-interaction-") && string(id) == "902" && (len(rawResult) > 0 || len(rawError) > 0) {
		// 交互响应必须先完成挂起的 turn/start，再发终态通知；否则测试只能
		// 证明 UI 收到了卡片，不能证明用户决定真的回到了同一条 Codex turn。
		accepted := len(rawError) == 0 && petCodexFixtureInteractionAccepted(scenario, rawResult)
		if pendingTurnStartID != nil && len(*pendingTurnStartID) > 0 {
			pendingID := append(json.RawMessage(nil), (*pendingTurnStartID)...)
			*pendingTurnStartID = nil
			writeCodexFixtureMessage(map[string]any{
				"id":     pendingID,
				"result": map[string]any{"turn": map[string]any{"id": "active-turn"}},
			})
		}
		if accepted {
			writeCodexFixtureMessage(map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{"threadId": os.Getenv("CODE_SWITCH_CODEX_THREAD_ID"), "turnId": "active-turn", "delta": "交互已确认"},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": os.Getenv("CODE_SWITCH_CODEX_THREAD_ID"), "turn": map[string]any{"id": "active-turn", "status": "completed"}},
			})
		} else {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": os.Getenv("CODE_SWITCH_CODEX_THREAD_ID"), "turn": map[string]any{"id": "active-turn", "status": "failed"}},
			})
		}
		return
	}
	var params map[string]any
	_ = json.Unmarshal(rawParams, &params)
	workspace, _ := params["cwd"].(string)
	if strings.TrimSpace(workspace) != "" && threadWorkspace != nil {
		*threadWorkspace = workspace
	} else if threadWorkspace != nil {
		workspace = *threadWorkspace
	}
	threadID := ""
	if fixtureThreadID != nil {
		threadID = strings.TrimSpace(*fixtureThreadID)
	}
	if threadID == "" {
		threadID = os.Getenv("CODE_SWITCH_CODEX_THREAD_ID")
	}
	if threadID == "" {
		threadID = "fixture-thread"
	}

	switch method {
	case "thread/start", "thread/resume":
		if method == "thread/resume" && scenario == "pet-model-switch" {
			// 第二个 app-server 进程的环境变量故意使用新值；只有按 resume
			// 请求里的旧 threadId 返回，才能证明模型切换没有创建新会话。
			if requestedThreadID, ok := params["threadId"].(string); ok && strings.TrimSpace(requestedThreadID) != "" {
				threadID = strings.TrimSpace(requestedThreadID)
				if fixtureThreadID != nil {
					*fixtureThreadID = threadID
				}
			}
		} else if fixtureThreadID != nil {
			*fixtureThreadID = threadID
		}
		if scenario == "pet-delay-session" {
			// 该延迟模拟真实 app-server 首次握手或 thread/resume 卡住；
			// StartChat 必须已经返回，不能把这段等待暴露到 Wails 调用栈。
			time.Sleep(1200 * time.Millisecond)
		}
		if scenario == "pet-dynamic-tool" && method == "thread/start" {
			dynamicTools, ok := params["dynamicTools"].([]any)
			if !ok || len(dynamicTools) != 1 {
				writeCodexFixtureMessage(map[string]any{
					"id":    json.RawMessage(id),
					"error": map[string]any{"code": -32001, "message": "fixture expected one dynamic tool"},
				})
				return
			}
		}
		result := petCodexFixtureThreadResult(workspace, threadID)
		if model, ok := params["model"].(string); ok && strings.TrimSpace(model) != "" {
			result["model"] = strings.TrimSpace(model)
		}
		if method == "thread/resume" && scenario == "pet-resume" {
			result["initialTurnsPage"] = map[string]any{
				"data": []any{map[string]any{"id": "stale-turn", "status": "inProgress"}},
			}
		}
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": result})
	case "skills/list":
		if scenario != "pet-capabilities" {
			writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{"data": []any{}}})
			break
		}
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{
			"data": []any{
				map[string]any{
					"cwd": workspace,
					"skills": []any{
						map[string]any{
							"name":             "fixture-skill",
							"path":             filepath.Join(workspace, ".codex", "skills", "fixture-skill", "SKILL.md"),
							"description":      "fixture skill",
							"shortDescription": "fixture",
							"scope":            "workspace",
							"enabled":          true,
						},
						map[string]any{
							"name":    "disabled-skill",
							"path":    filepath.Join(workspace, ".codex", "skills", "disabled-skill", "SKILL.md"),
							"enabled": false,
						},
					},
				},
				map[string]any{
					"cwd": filepath.Join(workspace, "other-project"),
					"skills": []any{map[string]any{
						"name":    "foreign-skill",
						"path":    filepath.Join(workspace, "other-project", "SKILL.md"),
						"enabled": true,
					}},
				},
			},
		}})
	case "model/list":
		if scenario != "pet-capabilities" {
			writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{"data": []any{}}})
			break
		}
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{
			"data": []any{
				map[string]any{"id": "fixture-default", "model": "gpt-fixture", "displayName": "Fixture Default", "isDefault": true, "inputModalities": []string{"text", "image"}, "defaultReasoningEffort": "medium"},
				map[string]any{"id": "fixture-hidden", "model": "hidden-fixture", "displayName": "Hidden Fixture", "hidden": true},
			},
			"nextCursor": "fixture-next",
		}})
	case "thread/compact/start":
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{"threadId": threadID, "status": "completed"}})
	case "thread/read":
		result := petCodexFixtureThreadResult(workspace, threadID)
		result["thread"].(map[string]any)["turns"] = []any{
			map[string]any{
				"id":     "history-turn-1",
				"status": "completed",
				"items": []any{
					map[string]any{
						"type": "userMessage",
						"id":   "history-user-1",
						"content": []any{
							map[string]any{"type": "text", "text": "历史问题"},
						},
					},
					map[string]any{
						"type": "agentMessage",
						"id":   "history-assistant-1",
						"text": "历史回答",
					},
					map[string]any{
						"type":    "commandExecution",
						"id":      "history-tool-1",
						"command": "浏览器工具内部记录",
					},
				},
			},
		}
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": result})
	case "turn/interrupt":
		turnID, _ := params["turnId"].(string)
		writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{}})
		if scenario == "pet-cancel-before-start-response" && turnID == "active-turn" {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": "active-turn", "status": "interrupted"},
				},
			})
			if pendingTurnStartID != nil && len(*pendingTurnStartID) > 0 {
				pendingID := append(json.RawMessage(nil), (*pendingTurnStartID)...)
				*pendingTurnStartID = nil
				time.Sleep(150 * time.Millisecond)
				writeCodexFixtureMessage(map[string]any{
					"id":     pendingID,
					"result": map[string]any{"turn": map[string]any{"id": "active-turn"}},
				})
			}
			return
		}
		if scenario == "pet-resume" && turnID == "stale-turn" {
			*staleInterrupted = true
			// interrupt 的响应先返回，随后才补发旧 turn 的终态，模拟真实
			// app-server 在 runtime 已挂载新 active 后才排空通知队列的时序。
			time.Sleep(50 * time.Millisecond)
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": "stale-turn", "status": "interrupted"},
				},
			})
			return
		}
		if scenario == "pet-hold" && turnID == "active-turn" {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": "active-turn", "status": "interrupted"},
				},
			})
		}
		if scenario == "pet-controls" {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "interrupted"},
				},
			})
		}
		if scenario == "pet-shared-concurrent" {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "interrupted"},
				},
			})
		}
	case "turn/steer":
		if scenario == "pet-controls" {
			writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{"turnId": "steered-turn"}})
		}
	case "review/start":
		if scenario == "pet-review" {
			writeCodexFixtureMessage(map[string]any{"id": json.RawMessage(id), "result": map[string]any{
				"reviewThreadId": threadID,
				"turn":           map[string]any{"id": "review-turn"},
			}})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/started",
				"params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": "review-turn", "status": "inProgress"}},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{"threadId": threadID, "turnId": "review-turn", "delta": "Review 已完成"},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": "review-turn", "status": "completed"}},
			})
		}
	case "turn/start":
		if turnStartCount != nil {
			*turnStartCount = *turnStartCount + 1
		}
		if scenario == "pet-start-exit" {
			// 这个 fixture 专门模拟 turn/start 尚未返回时 app-server 进程退出，
			// 用来验证启动调用与消费 goroutine 争夺 active 所有权时不会重复收口。
			os.Exit(0)
		}
		turnID := "active-turn"
		if scenario == "pet-complete" || scenario == "pet-resume" {
			turnID = "complete-turn"
		}
		if scenario == "pet-resume" && !*staleInterrupted {
			return
		}
		if scenario == "pet-timeout" {
			// 故意不回 turn/start response；runtime 必须把超时 client 收口，
			// 而不是把迟到通知或旧 pending request 带进下一轮。
			return
		}
		if scenario == "pet-dynamic-tool" {
			if pendingTurnStartID != nil {
				*pendingTurnStartID = append((*pendingTurnStartID)[:0], id...)
			}
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/started",
				"params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "inProgress"}},
			})
			writeCodexFixtureMessage(map[string]any{
				"id":     json.RawMessage("901"),
				"method": "item/tool/call",
				"params": map[string]any{"threadId": threadID, "turnId": turnID, "callId": "fixture-call", "name": "FixtureTool", "arguments": map[string]any{"value": "hello"}},
			})
			return
		}
		if scenario == "pet-delay-start" {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/started",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "inProgress"},
				},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{"threadId": threadID, "turnId": turnID, "delta": "提前到达的回复"},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "completed"},
				},
			})
			time.Sleep(150 * time.Millisecond)
		}
		if scenario == "pet-cancel-before-start-response" {
			if pendingTurnStartID != nil {
				*pendingTurnStartID = append(json.RawMessage(nil), id...)
			}
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/started",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "inProgress"},
				},
			})
			return
		}
		if strings.HasPrefix(scenario, "pet-interaction-") {
			if pendingTurnStartID != nil {
				*pendingTurnStartID = append((*pendingTurnStartID)[:0], id...)
			}
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/started",
				"params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "inProgress"}},
			})
			writeCodexFixtureMessage(petCodexFixtureInteractionRequest(scenario, threadID, turnID))
			return
		}
		writeCodexFixtureMessage(map[string]any{
			"id":     json.RawMessage(id),
			"result": map[string]any{"turn": map[string]any{"id": turnID}},
		})
		if scenario == "pet-hold" {
			return
		}
		writeCodexFixtureMessage(map[string]any{
			"method": "turn/started",
			"params": map[string]any{
				"threadId": threadID,
				"turn":     map[string]any{"id": turnID, "status": "inProgress"},
			},
		})
		if scenario == "pet-shared-concurrent" && turnStartCount != nil && *turnStartCount == 1 {
			// 第一轮保持运行，测试通过 interrupt 推进 Hub 队列；第二轮则
			// 复用同一 app-server/thread 并正常完成，形成确定性的并发入口验证。
			return
		}
		if scenario == "pet-shared-concurrent" {
			writeCodexFixtureMessage(map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{"threadId": threadID, "turnId": turnID, "delta": "第二条已完成"},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "completed"},
				},
			})
			return
		}
		if scenario == "pet-item-completed" {
			// 新版 Codex 可能只发 item/completed，不发 delta；runtime 必须
			// 从完成 item 读取全文，而不是一直等前端 watchdog 超时。
			writeCodexFixtureMessage(map[string]any{
				"method": "item/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turnId":   turnID,
					"item": map[string]any{
						"type": "agentMessage",
						"agentMessage": map[string]any{
							"text": "item 完成的完整回复",
						},
					},
				},
			})
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "completed"},
				},
			})
			return
		}
		if scenario == "pet-turn-items" {
			// 兼容只在 turn/completed 汇总 items 的 app-server 版本。
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn": map[string]any{
						"id":     turnID,
						"status": "completed",
						"items": []any{
							map[string]any{"type": "commandExecution", "command": "ignored"},
							map[string]any{"type": "agentMessage", "text": "turn items 的完整回复"},
						},
					},
				},
			})
			return
		}
		if scenario == "pet-failed" {
			writeCodexFixtureMessage(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     map[string]any{"id": turnID, "status": "failed"},
				},
			})
			return
		}
		writeCodexFixtureMessage(map[string]any{
			"method": "item/agentMessage/delta",
			"params": map[string]any{"threadId": threadID, "turnId": turnID, "delta": "宠物回复"},
		})
		for _, usage := range []map[string]any{
			{"inputTokens": 5, "cachedInputTokens": 1, "cacheWriteInputTokens": 2, "outputTokens": 2, "reasoningOutputTokens": 1},
			{"inputTokens": 7, "cachedInputTokens": 3, "cacheWriteInputTokens": 4, "outputTokens": 3, "reasoningOutputTokens": 2},
		} {
			writeCodexFixtureMessage(map[string]any{
				"method": "thread/tokenUsage/updated",
				"params": map[string]any{
					"threadId": threadID,
					"turnId":   turnID,
					"tokenUsage": map[string]any{
						"last": usage,
					},
				},
			})
		}
		writeCodexFixtureMessage(map[string]any{
			"method": "turn/completed",
			"params": map[string]any{
				"threadId": threadID,
				"turn":     map[string]any{"id": turnID, "status": "completed"},
			},
		})
	}
}

func petCodexFixtureInteractionRequest(scenario, threadID, turnID string) map[string]any {
	kind := strings.TrimPrefix(scenario, "pet-interaction-")
	base := map[string]any{"threadId": threadID, "turnId": turnID}
	switch kind {
	case "approval":
		base["itemId"] = "fixture-command-item"
		base["approvalId"] = "fixture-approval"
		base["command"] = "git status"
		base["cwd"] = "C:\\fixture"
		base["reason"] = "fixture approval"
		base["availableDecisions"] = []string{"accept", "acceptForSession", "decline"}
		return map[string]any{"id": 902, "method": "item/commandExecution/requestApproval", "params": base}
	case "permission":
		base["itemId"] = "fixture-permission-item"
		base["cwd"] = "C:\\fixture"
		base["reason"] = "fixture permission"
		base["permissions"] = map[string]any{"fileSystem": map[string]any{"roots": []string{"C:\\fixture"}}}
		return map[string]any{"id": 902, "method": "item/permissions/requestApproval", "params": base}
	case "user-input":
		base["itemId"] = "fixture-question-item"
		base["questions"] = []any{map[string]any{
			"id":       "question-1",
			"header":   "选择项",
			"question": "请选择一个测试答案",
			"options":  []any{map[string]any{"label": "answer", "description": "fixture answer"}},
		}}
		return map[string]any{"id": 902, "method": "item/tool/requestUserInput", "params": base}
	case "mcp":
		base["serverName"] = "fixture-mcp"
		base["message"] = "请输入 MCP 测试值"
		base["mode"] = "form"
		base["requestedSchema"] = map[string]any{
			"type":       "object",
			"properties": map[string]any{"token": map[string]any{"type": "string", "title": "Token"}},
			"required":   []string{"token"},
		}
		return map[string]any{"id": 902, "method": "mcpServer/elicitation/request", "params": base}
	default:
		return map[string]any{"id": 902, "method": "fixture/unsupported-interaction", "params": base}
	}
}

func petCodexFixtureInteractionAccepted(scenario string, rawResult json.RawMessage) bool {
	if len(rawResult) == 0 {
		return false
	}
	kind := strings.TrimPrefix(scenario, "pet-interaction-")
	switch kind {
	case "approval":
		var response struct {
			Decision string `json:"decision"`
		}
		return json.Unmarshal(rawResult, &response) == nil && response.Decision == "acceptForSession"
	case "permission":
		var response struct {
			Scope string `json:"scope"`
		}
		return json.Unmarshal(rawResult, &response) == nil && response.Scope == "session"
	case "user-input":
		var response struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		}
		if json.Unmarshal(rawResult, &response) != nil {
			return false
		}
		answer, ok := response.Answers["question-1"]
		return ok && len(answer.Answers) == 1 && answer.Answers[0] == "answer"
	case "mcp":
		var response struct {
			Action  string         `json:"action"`
			Content map[string]any `json:"content"`
		}
		if json.Unmarshal(rawResult, &response) != nil {
			return false
		}
		return response.Action == "accept" && response.Content["token"] == "secret"
	default:
		return false
	}
}

func petCodexFixtureThreadResult(workspace, threadID string) map[string]any {
	return map[string]any{
		"thread": map[string]any{
			"id":    threadID,
			"cwd":   workspace,
			"turns": []any{},
		},
		"model":                 "fixture-model",
		"modelProvider":         "fixture-provider",
		"cwd":                   workspace,
		"runtimeWorkspaceRoots": []string{workspace},
		"approvalPolicy":        "never",
		"sandbox": map[string]any{
			"type":          "workspaceWrite",
			"writableRoots": []string{workspace},
			"networkAccess": true,
		},
	}
}

func newCodexFixtureFactory(scenario string, nextThreadID func() string) CodexAppServerCommandFactory {
	return func(_ string, _ ...string) *exec.Cmd {
		threadID := "fixture-thread"
		if nextThreadID != nil {
			threadID = nextThreadID()
		}
		return newCodexFixtureCommand(scenario, threadID)
	}
}

func newCodexFixtureCommand(scenario, threadID string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=TestCodexAppServerFixture")
	command.Env = append(os.Environ(),
		"CODE_SWITCH_CODEX_FIXTURE=1",
		"CODE_SWITCH_CODEX_SCENARIO="+scenario,
		"CODE_SWITCH_CODEX_THREAD_ID="+threadID,
	)
	return command
}

func TestCodexExecutableUsableRejectsBatchShimWithMissingAbsoluteTarget(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows batch shim resolution is under test")
	}
	directory := t.TempDir()
	shim := filepath.Join(directory, "codex.cmd")
	target := filepath.Join(directory, "missing", "codex.exe")
	if err := os.WriteFile(shim, []byte("@\""+target+"\" %*\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if codexExecutableUsable(shim) {
		t.Fatal("stale batch shim should not be considered usable")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !codexExecutableUsable(shim) {
		t.Fatal("batch shim with an existing absolute target should be usable")
	}
}

func TestCodexAppServerClientRoutesRPCNotificationsAndRejectsServerRequest(t *testing.T) {
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		CommandFactory:  newCodexFixtureFactory("rpc", nil),
		ResponseTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Call(context.Background(), "initialize", map[string]any{}); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	result, err := client.Call(context.Background(), "run", map[string]any{"value": "request"})
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil || decoded["ok"] != true {
		t.Fatalf("run result = %s, err = %v", result, err)
	}

	seenNotice := false
	seenRejected := false
	deadline := time.After(2 * time.Second)
	for !(seenNotice && seenRejected) {
		select {
		case message := <-client.Notifications():
			if message.Method == "fixture/notice" {
				seenNotice = true
			}
			if message.Method == "fixture/server-request-rejected" {
				var params struct {
					Code int `json:"code"`
				}
				if json.Unmarshal(message.Params, &params) == nil && params.Code == -32601 {
					seenRejected = true
				}
			}
		case <-deadline:
			t.Fatalf("notifications incomplete: notice=%v rejected=%v", seenNotice, seenRejected)
		}
	}
}

func TestCodexAppServerClientRoutesServerRequestToHandler(t *testing.T) {
	accepted := make(chan string, 1)
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		CommandFactory:  newCodexFixtureFactory("rpc-handler", nil),
		ResponseTimeout: 2 * time.Second,
		ServerRequestHandler: func(_ context.Context, message CodexAppServerMessage) CodexAppServerServerRequestResponse {
			if message.Method != "approval/request" {
				t.Fatalf("server request method = %q", message.Method)
			}
			accepted <- message.Method
			return CodexAppServerServerRequestResponse{Result: map[string]any{"decision": "accept"}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Call(context.Background(), "initialize", map[string]any{}); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	if _, err := client.Call(context.Background(), "run", map[string]any{"value": "request"}); err != nil {
		t.Fatalf("run error = %v", err)
	}
	select {
	case method := <-accepted:
		if method != "approval/request" {
			t.Fatalf("accepted request method = %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server request handler was not called")
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case message := <-client.Notifications():
			if message.Method != "fixture/server-request-accepted" {
				continue
			}
			var params struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil || params.Decision != "accept" {
				t.Fatalf("accepted server request payload = %s, err=%v", message.Params, err)
			}
			return
		case <-deadline:
			t.Fatal("fixture did not observe server request result")
		}
	}
}

func TestCodexAppServerClientResolvesObservedServerRequestAsynchronously(t *testing.T) {
	observed := make(chan CodexAppServerMessage, 1)
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		CommandFactory:  newCodexFixtureFactory("rpc-handler", nil),
		ResponseTimeout: 2 * time.Second,
		ServerRequestObserver: func(_ context.Context, message CodexAppServerMessage) bool {
			observed <- message
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Call(context.Background(), "initialize", map[string]any{}); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	runResult := make(chan error, 1)
	go func() {
		_, callErr := client.Call(context.Background(), "run", map[string]any{"value": "request"})
		runResult <- callErr
	}()

	var request CodexAppServerMessage
	select {
	case request = <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("server request observer was not called")
	}
	if request.Method != "approval/request" || string(request.ID) != "900" {
		t.Fatalf("observed server request = %#v", request)
	}
	if err := client.ResolveServerRequest(request.ID, CodexAppServerServerRequestResponse{
		Result: map[string]any{"decision": "acceptForSession"},
	}); err != nil {
		t.Fatalf("ResolveServerRequest() error = %v", err)
	}
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not resume after asynchronous server request resolution")
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case message := <-client.Notifications():
			if message.Method != "fixture/server-request-accepted" {
				continue
			}
			var params struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil || params.Decision != "acceptForSession" {
				t.Fatalf("accepted asynchronous server request payload = %s, err=%v", message.Params, err)
			}
			return
		case <-deadline:
			t.Fatal("fixture did not observe asynchronous server request result")
		}
	}
}

func TestCodexPetInitializeDeclaresHandledCapabilities(t *testing.T) {
	params := codexPetAppServerInitializeParams("pet", "Pet", "test")
	capabilities, ok := params["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %#v", params["capabilities"])
	}
	if capabilities["mcpServerOpenaiFormElicitation"] != true {
		t.Fatalf("MCP elicitation capability = %#v", capabilities["mcpServerOpenaiFormElicitation"])
	}
	if _, present := capabilities["optOutNotificationMethods"]; present {
		t.Fatalf("pet initialize should not opt out notifications: %#v", capabilities)
	}
}

func TestCodexAppServerClientProcessExitWakesPendingCall(t *testing.T) {
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		CommandFactory:  newCodexFixtureFactory("exit", nil),
		ResponseTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Call(context.Background(), "initialize", map[string]any{}); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	_, err = client.Call(context.Background(), "block", nil)
	if err == nil {
		t.Fatal("pending call should fail after fixture process exits")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending call waited for timeout instead of process exit: %v", err)
	}
}

func TestCodexAppServerClientCloseDoesNotWaitForDescendantHoldingStdout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows process tree and inherited pipe semantics are under test")
	}
	readyFile := filepath.Join(t.TempDir(), "codex-child-ready")
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		CommandFactory: func(_ string, _ ...string) *exec.Cmd {
			command := newCodexFixtureCommand("hold-stdout-child", "hold-child-thread")
			command.Env = append(command.Env, "CODE_SWITCH_CODEX_READY_FILE="+readyFile)
			return command
		},
		ResponseTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(readyFile); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture child did not inherit stdout before Close")
		}
		time.Sleep(10 * time.Millisecond)
	}

	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close() blocked while descendant held stdout")
	}
}

func TestPetCodexRuntimeRealLocalConfigSinglePoint(t *testing.T) {
	if os.Getenv("CODE_SWITCH_REAL_CODEX") != "1" {
		t.Skip("set CODE_SWITCH_REAL_CODEX=1 to run the local Codex/Relay single-point check")
	}

	workspace := t.TempDir()
	stream := &petCodexEventStream{events: make(chan PetAIEvent, 32)}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
		Emitter:         stream,
		Executable:      os.Getenv("CODESWITCH_CODEX_EXECUTABLE"),
		ResponseTimeout: 60 * time.Second,
	})
	defer runtime.Close()

	request := PetChatRequest{
		PetID:     "real-local-single-point",
		RequestID: "real-local-single-point-request",
		Persona:   "你是单点测试助手，只需要简短回复 OK。",
		UserText:  "请回复 OK。",
	}
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("real StartChat() error = %v", err)
	}

	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-stream.events:
			if event.RequestID != request.RequestID {
				continue
			}
			switch event.Type {
			case PetAIEventFailed:
				t.Fatalf("real Codex failed: %#v", event.Error)
			case PetAIEventCompleted:
				if strings.TrimSpace(event.Text) == "" {
					t.Fatal("real Codex completed without text")
				}
				state := runtime.stateForPet(request.PetID)
				state.mu.Lock()
				modelProvider, model := state.modelProvider, state.model
				state.mu.Unlock()
				t.Logf("real Codex completed: modelProvider=%q model=%q textBytes=%d", modelProvider, model, len(event.Text))
				return
			}
		case <-deadline.C:
			t.Fatal("real Codex single-point check timed out")
		}
	}
}

func TestCodexAppServerRealBrowserSinglePoint(t *testing.T) {
	if os.Getenv("CODE_SWITCH_REAL_BROWSER") != "1" {
		t.Skip("set CODE_SWITCH_REAL_BROWSER=1 to run the local Codex browser capability check")
	}

	workspace, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve browser probe workspace: %v", err)
	}
	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		Executable:       os.Getenv("CODESWITCH_CODEX_EXECUTABLE"),
		WorkingDirectory: workspace,
		ResponseTimeout:  120 * time.Second,
		ServerRequestHandler: func(ctx context.Context, message CodexAppServerMessage) CodexAppServerServerRequestResponse {
			// 浏览器探针不提供第二套交互 UI，但仍按协议接受 Codex 的审批请求；
			// 否则工具可能还没执行就被宿主自己的“未实现”分支截断。
			return (&PetCodexRuntime{}).handleCodexServerRequest(ctx, message)
		},
	})
	if err != nil {
		t.Fatalf("start real browser probe: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "initialize", codexPetAppServerInitializeParams(
		"codeswitch-browser-probe",
		"CodeSwitch Browser Probe",
		"test",
	)); err != nil {
		t.Fatalf("browser probe initialize: %v", err)
	}
	if err := client.Notify("initialized", map[string]any{}); err != nil {
		t.Fatalf("browser probe initialized: %v", err)
	}
	threadRaw, err := client.Call(ctx, "thread/start", map[string]any{
		"cwd":                   workspace,
		"developerInstructions": "你是浏览器能力单点测试助手。只有实际调用浏览器工具并完成后，才回复浏览器工具已调用；没有工具时必须明确说明缺少工具。",
		"threadSource":          "user",
	})
	if err != nil {
		t.Fatalf("browser probe thread/start: %v", err)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(threadRaw, &thread); err != nil || strings.TrimSpace(thread.Thread.ID) == "" {
		t.Fatalf("browser probe thread/start returned invalid thread: %s", string(threadRaw))
	}
	turnRaw, err := client.Call(ctx, "turn/start", map[string]any{
		"threadId": thread.Thread.ID,
		"input": []map[string]any{{
			"type": "text",
			"text": "请使用 browser 工具打开 https://example.com，然后只回复浏览器工具已调用。不要用文字假装完成。",
		}},
		"cwd": workspace,
	})
	if err != nil {
		t.Fatalf("browser probe turn/start: %v", err)
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(turnRaw, &turn); err != nil || strings.TrimSpace(turn.Turn.ID) == "" {
		t.Fatalf("browser probe turn/start returned invalid turn: %s", string(turnRaw))
	}

	sawBrowserTool := false
	deadline := time.NewTimer(180 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case message, ok := <-client.Notifications():
			if !ok {
				t.Fatal("browser probe app-server closed before turn completion")
			}
			if strings.HasPrefix(message.Method, "item/") || strings.HasPrefix(message.Method, "mcp") {
				var item struct {
					Item struct {
						Type   string `json:"type"`
						Server string `json:"server"`
						Tool   string `json:"tool"`
						Status string `json:"status"`
					} `json:"item"`
				}
				_ = json.Unmarshal(message.Params, &item)
				if item.Item.Type == "mcpToolCall" && item.Item.Server == "node_repl" && item.Item.Tool == "js" {
					sawBrowserTool = true
				}
				t.Logf("browser probe event method=%q item_type=%q server=%q tool=%q status=%q", message.Method, item.Item.Type, item.Item.Server, item.Item.Tool, item.Item.Status)
			}
			if message.Method == "turn/completed" || message.Method == "turn/failed" {
				if !sawBrowserTool {
					t.Fatalf("browser probe completed without browser tool event; terminal=%s", string(message.Params))
				}
				return
			}
		case <-deadline.C:
			t.Fatal("browser probe timed out")
		}
	}
}

type petCodexSessionMemory struct {
	mu       sync.Mutex
	sessions map[string]PetCodexSession
}

func (m *petCodexSessionMemory) LoadCodexSession(_ context.Context, petID string) (*PetCodexSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[petID]
	if !ok {
		return nil, nil
	}
	copy := session
	return &copy, nil
}

func (m *petCodexSessionMemory) SaveCodexSession(_ context.Context, session PetCodexSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = make(map[string]PetCodexSession)
	}
	m.sessions[session.PetID] = session
	return nil
}

type petCodexEventRecorder struct {
	mu     sync.Mutex
	events []PetAIEvent
}

type petCodexEventStream struct {
	recorder petCodexEventRecorder
	events   chan PetAIEvent
}

func (r *petCodexEventStream) Emit(event PetAIEvent) error {
	r.recorder.Emit(event)
	r.events <- event
	return nil
}

func (r *petCodexEventRecorder) Emit(event PetAIEvent) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *petCodexEventRecorder) waitFor(requestID string, eventType PetAIEventType) PetAIEvent {
	// 异步 runtime 的终态预算包含 Windows fixture 启动握手和一次 RPC response
	// timeout；只等 2 秒会把“迟到但正确的 failed 事件”误判成事件丢失。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, event := range r.events {
			if event.RequestID == requestID && event.Type == eventType {
				r.mu.Unlock()
				return event
			}
		}
		r.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	panic(fmt.Sprintf("event %s for request %s not received", eventType, requestID))
}

func (r *petCodexEventRecorder) eventsFor(requestID string, eventType PetAIEventType) []PetAIEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]PetAIEvent, 0)
	for _, event := range r.events {
		if event.RequestID == requestID && event.Type == eventType {
			result = append(result, event)
		}
	}
	return result
}

func TestPetCodexRuntimeConsumesNotificationsBeforeTurnStartResponse(t *testing.T) {
	workspace := t.TempDir()
	stream := &petCodexEventStream{events: make(chan PetAIEvent, 16)}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           stream,
		CommandFactory:    newCodexFixtureFactory("pet-delay-start", func() string { return "delayed-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-delay", "delayed-request", "delayed persona")
	startedAt := time.Now()
	result, err := runtime.StartChat(context.Background(), request)
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if result.RequestID != request.RequestID {
		t.Fatalf("StartChat() request id = %q, want %q", result.RequestID, request.RequestID)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("StartChat() blocked for %s while Codex worker was starting", elapsed)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-stream.events:
			if event.RequestID != request.RequestID || event.Type != PetAIEventCompleted {
				continue
			}
			return
		case <-deadline:
			t.Fatal("completed event was not consumed by the asynchronous Codex worker")
		}
	}
}

func TestPetCodexRuntimeReturnsBeforeSessionHandshake(t *testing.T) {
	workspace := t.TempDir()
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-delay-session", func() string { return "delayed-session-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-delay-session", "delayed-session-request", "delayed session persona")
	startedAt := time.Now()
	result, err := runtime.StartChat(context.Background(), request)
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if result.RequestID != request.RequestID {
		t.Fatalf("StartChat() request id = %q, want %q", result.RequestID, request.RequestID)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("StartChat() blocked for %s on delayed session handshake", elapsed)
	}
	if completed := recorder.waitFor(request.RequestID, PetAIEventCompleted); completed.Text != "宠物回复" {
		t.Fatalf("completed event = %#v", completed)
	}
}

func TestPetCodexRuntimeIgnoresUnknownNotificationsWithoutLocking(t *testing.T) {
	workspace := t.TempDir()
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-unknown-notification", func() string { return "unknown-notification-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-unknown-notification", "unknown-notification-request", "unknown notification persona")
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := recorder.waitFor(request.RequestID, PetAIEventCompleted)
	if completed.Text != "宠物回复" {
		t.Fatalf("completed event = %#v", completed)
	}
}

func TestPetCodexRuntimeAllowsConsecutiveTurns(t *testing.T) {
	workspace := t.TempDir()
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-complete", func() string { return "consecutive-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	first := petCodexRuntimeRequest("pet-consecutive", "consecutive-first", "consecutive persona")
	if _, err := runtime.StartChat(context.Background(), first); err != nil {
		t.Fatalf("first StartChat() error = %v", err)
	}
	recorder.waitFor(first.RequestID, PetAIEventCompleted)

	second := petCodexRuntimeRequest("pet-consecutive", "consecutive-second", "consecutive persona")
	if _, err := runtime.StartChat(context.Background(), second); err != nil {
		t.Fatalf("second StartChat() error = %v", err)
	}
	recorder.waitFor(second.RequestID, PetAIEventCompleted)
}

func TestPetCodexRuntimeCompletesFromItemCompletedWithoutDelta(t *testing.T) {
	workspace := t.TempDir()
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-item-completed", func() string { return "item-completed-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-item-completed", "item-completed-request", "item completed persona")
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := recorder.waitFor(request.RequestID, PetAIEventCompleted)
	if completed.Text != "item 完成的完整回复" {
		t.Fatalf("completed text = %q, want item completion text", completed.Text)
	}
	if progress := recorder.eventsFor(request.RequestID, PetAIEventProgress); len(progress) == 0 {
		t.Fatal("item/completed did not emit a progress event")
	}
}

func TestPetCodexRuntimeFallsBackToTurnCompletedItems(t *testing.T) {
	workspace := t.TempDir()
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-turn-items", func() string { return "turn-items-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-turn-items", "turn-items-request", "turn items persona")
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := recorder.waitFor(request.RequestID, PetAIEventCompleted)
	if completed.Text != "turn items 的完整回复" {
		t.Fatalf("completed text = %q, want turn item text", completed.Text)
	}
}

func TestPetCodexRuntimeMapsFailedTurnToFailedEvent(t *testing.T) {
	workspace := t.TempDir()
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-failed", func() string { return "failed-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-failed", "failed-request", "failed persona")
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	failed := recorder.waitFor(request.RequestID, PetAIEventFailed)
	if failed.Error == nil || failed.Error.Code != string(PET_AI_UPSTREAM_ERROR) {
		t.Fatalf("failed event = %#v, want upstream failure", failed)
	}
	if cancelled := recorder.eventsFor(request.RequestID, PetAIEventCancelled); len(cancelled) != 0 {
		t.Fatalf("failed turn emitted cancelled events: %#v", cancelled)
	}
}

func TestPetCodexRuntimeCleansUpTurnStartTimeoutAndReinitializes(t *testing.T) {
	workspace := t.TempDir()
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	recorder := &petCodexEventRecorder{}
	var processMu sync.Mutex
	processNumber := 0
	factory := CodexAppServerCommandFactory(func(_ string, _ ...string) *exec.Cmd {
		processMu.Lock()
		processNumber++
		current := processNumber
		processMu.Unlock()
		scenario := "pet-timeout"
		if current > 1 {
			scenario = "pet-complete"
		}
		return newCodexFixtureCommand(scenario, "retry-thread")
	})
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    factory,
		// Windows 下 fixture 子进程的初始化和 thread/start 握手可能超过
		// 500ms；预算过短会在真正进入 turn/start 超时场景前误报依赖不可用。
		ResponseTimeout: 2 * time.Second,
	})
	defer runtime.Close()

	first := petCodexRuntimeRequest("pet-timeout", "timeout-first", "timeout persona")
	if _, err := runtime.StartChat(context.Background(), first); err != nil {
		t.Fatalf("first StartChat() error = %v", err)
	}
	failed := recorder.waitFor(first.RequestID, PetAIEventFailed)
	if failed.Error == nil || failed.Error.Code != string(PET_AI_TIMEOUT) {
		t.Fatalf("first terminal event = %#v, want %s", failed, PET_AI_TIMEOUT)
	}

	state := runtime.stateForPet(first.PetID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		idle := state.active == nil && state.client == nil
		state.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	state.mu.Lock()
	idle := state.active == nil && state.client == nil
	state.mu.Unlock()
	if !idle {
		t.Fatal("turn/start timeout left active turn or client behind")
	}

	second := petCodexRuntimeRequest("pet-timeout", "timeout-retry", "timeout persona")
	if _, err := runtime.StartChat(context.Background(), second); err != nil {
		t.Fatalf("retry StartChat() error = %v", err)
	}
	recorder.waitFor(second.RequestID, PetAIEventCompleted)
}

func TestPetCodexRuntimeCancelsBeforeTurnStartResponseWithoutDuplicateTerminal(t *testing.T) {
	workspace := t.TempDir()
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	stream := &petCodexEventStream{events: make(chan PetAIEvent, 16)}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           stream,
		CommandFactory:    newCodexFixtureFactory("pet-cancel-before-start-response", func() string { return "cancel-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-cancel-before-response", "cancel-before-response", "cancel persona")
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}

	state := runtime.stateForPet(request.PetID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		turnStarted := state.active != nil && state.active.turnID == "active-turn"
		state.mu.Unlock()
		if turnStarted {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	state.mu.Lock()
	turnStarted := state.active != nil && state.active.turnID == "active-turn"
	state.mu.Unlock()
	if !turnStarted {
		t.Fatal("turn/started notification was not consumed before response")
	}
	if err := runtime.CancelChat(request.RequestID); err != nil {
		t.Fatalf("CancelChat() error = %v", err)
	}

	seenCancelled := false
	cancelDeadline := time.After(2 * time.Second)
	for !seenCancelled {
		select {
		case event := <-stream.events:
			if event.RequestID != request.RequestID || event.Type != PetAIEventCancelled {
				continue
			}
			seenCancelled = true
		case <-cancelDeadline:
			t.Fatal("cancelled event was not emitted")
		}
	}
	if failed := stream.recorder.eventsFor(request.RequestID, PetAIEventFailed); len(failed) != 0 {
		t.Fatalf("cancelled request emitted failed events: %#v", failed)
	}
	if cancelled := stream.recorder.eventsFor(request.RequestID, PetAIEventCancelled); len(cancelled) != 1 {
		t.Fatalf("cancelled events = %#v, want exactly one", cancelled)
	}
}

func petCodexRuntimeRequest(petID, requestID, persona string) PetChatRequest {
	return PetChatRequest{
		PetID:          petID,
		RequestID:      requestID,
		Persona:        persona,
		RuntimeContext: "当前本地时间：2026-08-21T15:00:00Z；当前时区：Asia/Shanghai。",
		UserText:       "陪我聊聊",
	}
}

func TestPetCodexRuntimeUsesCodexDefaultsWithoutModelOverrides(t *testing.T) {
	workspace := t.TempDir()
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{})

	paramsByMethod := map[string]map[string]any{
		"thread/start":  runtime.threadStartParams(workspace, "persona"),
		"thread/resume": runtime.threadResumeParams("thread-id", workspace, "persona"),
	}
	for method, params := range paramsByMethod {
		for _, key := range []string{
			"model", "modelProvider", "model_provider", "effort",
			"approvalPolicy", "sandbox", "sandboxPolicy", "runtimeWorkspaceRoots", "networkAccess",
		} {
			if _, present := params[key]; present {
				t.Fatalf("%s params unexpectedly contain %q: %#v", method, key, params)
			}
		}
	}

	input, err := normalizePetCodexChatRequest(PetChatRequest{
		PetID:     "pet-default",
		RequestID: "request-default",
		Persona:   "persona",
		UserText:  "hello",
		// 共享请求仍保留该字段供主动消息和梦境文本兼容；主聊天 runtime 必须忽略它。
		Reasoning: "high",
	})
	if err != nil {
		t.Fatalf("normalizePetCodexChatRequest() error = %v", err)
	}
	turnParams := runtime.buildTurnStartParams(&petCodexActiveTurn{
		state:   &petCodexPetState{threadID: "thread-id", workspace: workspace},
		request: input,
	})
	for _, key := range []string{
		"model", "modelProvider", "model_provider", "effort",
		"approvalPolicy", "sandbox", "sandboxPolicy", "runtimeWorkspaceRoots", "networkAccess",
	} {
		if _, present := turnParams[key]; present {
			t.Fatalf("turn/start params unexpectedly contain %q: %#v", key, turnParams)
		}
	}
}

func TestPetCodexRuntimePersistsIsolatedThreadsAndMergesUsage(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "pet-a")
	rootB := filepath.Join(t.TempDir(), "pet-b")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	recorder := &petCodexEventRecorder{}
	workspace := map[string]string{"pet-a": rootA, "pet-b": rootB}
	var processNumber int
	var processMu sync.Mutex
	factory := newCodexFixtureFactory("pet-complete", func() string {
		processMu.Lock()
		defer processMu.Unlock()
		processNumber++
		return fmt.Sprintf("thread-%d", processNumber)
	})
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(_ context.Context, petID string) (string, error) {
			return workspace[petID], nil
		}),
		Emitter:         recorder,
		CommandFactory:  factory,
		ResponseTimeout: 2 * time.Second,
	})
	defer runtime.Close()

	for _, petID := range []string{"pet-a", "pet-b"} {
		requestID := petID + "-request"
		if _, err := runtime.StartChat(context.Background(), petCodexRuntimeRequest(petID, requestID, "稳定 persona")); err != nil {
			t.Fatalf("StartChat(%s) error = %v", petID, err)
		}
		completed := recorder.waitFor(requestID, PetAIEventCompleted)
		if completed.Text != "宠物回复" {
			t.Fatalf("completed text for %s = %q", petID, completed.Text)
		}
		usageEvents := recorder.eventsFor(requestID, PetAIEventUsage)
		if len(usageEvents) != 1 || usageEvents[0].Usage == nil {
			t.Fatalf("usage events for %s = %#v", petID, usageEvents)
		}
		if usageEvents[0].Usage.InputTokens != 7 || usageEvents[0].Usage.OutputTokens != 3 {
			t.Fatalf("merged usage for %s = %#v", petID, usageEvents[0].Usage)
		}
	}

	sessions.mu.Lock()
	first := sessions.sessions["pet-a"]
	second := sessions.sessions["pet-b"]
	sessions.mu.Unlock()
	if first.ThreadID == "" || second.ThreadID == "" || first.ThreadID == second.ThreadID {
		t.Fatalf("pet threads are not isolated: first=%#v second=%#v", first, second)
	}
	if first.Workspace != rootA || second.Workspace != rootB {
		t.Fatalf("pet workspaces are not isolated: first=%#v second=%#v", first, second)
	}
}

func TestPetCodexRuntimeDeduplicatesTurnStartFailureAfterClientExit(t *testing.T) {
	workspace := t.TempDir()
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
		Emitter:         recorder,
		CommandFactory:  newCodexFixtureFactory("pet-start-exit", func() string { return "start-exit-thread" }),
		ResponseTimeout: 2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("pet-start-exit", "start-exit-request", "start failure persona")
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	failed := recorder.waitFor(request.RequestID, PetAIEventFailed)
	if failed.Error == nil || failed.Error.Code != string(PET_AI_DEPENDENCY_UNAVAILABLE) {
		t.Fatalf("terminal event = %#v, want %s", failed, PET_AI_DEPENDENCY_UNAVAILABLE)
	}

	state := runtime.stateForPet(request.PetID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		idle := state.active == nil && state.client == nil
		state.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	state.mu.Lock()
	idle := state.active == nil && state.client == nil
	state.mu.Unlock()
	if !idle {
		t.Fatal("client exit left the pet turn active")
	}
	if failed := recorder.eventsFor(request.RequestID, PetAIEventFailed); len(failed) != 1 {
		t.Fatalf("failed events = %#v, want exactly one terminal event", failed)
	}
}

func TestPetCodexRuntimeResumesAndInterruptsStaleTurn(t *testing.T) {
	workspace := t.TempDir()
	persona := "恢复测试 persona"
	sessions := &petCodexSessionMemory{sessions: map[string]PetCodexSession{
		"pet-resume": {
			PetID:              "pet-resume",
			ThreadID:           "persisted-thread",
			Workspace:          workspace,
			PersonaFingerprint: petCodexPersonaFingerprint(persona),
			ProtocolVersion:    PetCodexPlanProtocolVersion,
			UpdatedAt:          time.Now().UnixMilli(),
		},
	}}
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-resume", func() string { return "persisted-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()
	request := petCodexRuntimeRequest("pet-resume", "resume-request", persona)
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if completed := recorder.waitFor(request.RequestID, PetAIEventCompleted); completed.Text != "宠物回复" {
		t.Fatalf("completed event = %#v", completed)
	}
	if normalizePetCodexTurnStatus("inProgress") != "in_progress" {
		t.Fatalf("inProgress status was not normalized")
	}
}

func TestPetCodexRuntimeRejectsConcurrentTurnAndCancels(t *testing.T) {
	workspace := t.TempDir()
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:          sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) { return workspace, nil }),
		Emitter:           recorder,
		CommandFactory:    newCodexFixtureFactory("pet-hold", func() string { return "hold-thread" }),
		ResponseTimeout:   2 * time.Second,
	})
	defer runtime.Close()
	first := petCodexRuntimeRequest("pet-hold", "hold-request", "hold persona")
	if _, err := runtime.StartChat(context.Background(), first); err != nil {
		t.Fatalf("first StartChat() error = %v", err)
	}
	second := petCodexRuntimeRequest("pet-hold", "second-request", "hold persona")
	if _, err := runtime.StartChat(context.Background(), second); PetAIErrorCodeOf(err) != string(PET_AI_REQUEST_IN_FLIGHT) {
		t.Fatalf("concurrent StartChat() error = %v", err)
	}
	if err := runtime.CancelChat(first.RequestID); err != nil {
		t.Fatalf("CancelChat() error = %v", err)
	}
	if cancelled := recorder.waitFor(first.RequestID, PetAIEventCancelled); cancelled.Type != PetAIEventCancelled {
		t.Fatalf("cancelled event = %#v", cancelled)
	}
}
