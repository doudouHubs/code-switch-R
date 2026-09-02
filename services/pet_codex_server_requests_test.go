package services

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPetCodexServerRequestHandlerReturnsProtocolTerminalResults(t *testing.T) {
	runtime := &PetCodexRuntime{}
	cases := []struct {
		name   string
		method string
		params any
		check  func(t *testing.T, result map[string]any)
	}{
		{
			name:   "command approval",
			method: "item/commandExecution/requestApproval",
			check: func(t *testing.T, result map[string]any) {
				if result["decision"] != "accept" {
					t.Fatalf("decision = %#v", result["decision"])
				}
			},
		},
		{
			name:   "file approval",
			method: "item/fileChange/requestApproval",
			check: func(t *testing.T, result map[string]any) {
				if result["decision"] != "accept" {
					t.Fatalf("decision = %#v", result["decision"])
				}
			},
		},
		{
			name:   "permissions",
			method: "item/permissions/requestApproval",
			check: func(t *testing.T, result map[string]any) {
				if result["scope"] != "session" {
					t.Fatalf("scope = %#v", result["scope"])
				}
				permissions, ok := result["permissions"].(map[string]any)
				if !ok || len(permissions) != 0 {
					t.Fatalf("permissions = %#v", result["permissions"])
				}
			},
		},
		{
			name:   "mcp elicitation",
			method: "mcpServer/elicitation/request",
			check: func(t *testing.T, result map[string]any) {
				if result["action"] != "decline" {
					t.Fatalf("action = %#v", result["action"])
				}
			},
		},
		{
			name:   "user input",
			method: "item/tool/requestUserInput",
			params: map[string]any{"questions": []any{map[string]any{"id": "question-1"}}},
			check: func(t *testing.T, result map[string]any) {
				answers, ok := result["answers"].(map[string]any)
				if !ok {
					t.Fatalf("answers = %#v", result["answers"])
				}
				question, ok := answers["question-1"].(map[string]any)
				if !ok {
					t.Fatalf("question answer = %#v", answers["question-1"])
				}
				if values, ok := question["answers"].([]any); !ok || len(values) != 0 {
					t.Fatalf("empty answer values = %#v", question["answers"])
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			params, err := json.Marshal(testCase.params)
			if err != nil {
				t.Fatal(err)
			}
			response := runtime.handleCodexServerRequest(context.Background(), CodexAppServerMessage{
				ID:     json.RawMessage("1"),
				Method: testCase.method,
				Params: params,
			})
			if response.Error != nil {
				t.Fatalf("response error = %#v", response.Error)
			}
			encoded, err := json.Marshal(response.Result)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			testCase.check(t, result)
		})
	}
}

func TestPetCodexServerRequestHandlerRejectsUnsupportedDynamicTool(t *testing.T) {
	runtime := &PetCodexRuntime{}
	response := runtime.handleCodexServerRequest(context.Background(), CodexAppServerMessage{
		ID:     json.RawMessage("2"),
		Method: "item/tool/call",
	})
	if response.Error == nil || response.Error.Code != petCodexRPCMethodNotFound {
		t.Fatalf("dynamic tool response = %#v", response)
	}
}
