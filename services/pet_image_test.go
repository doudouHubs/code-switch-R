package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type petImageTestProviderReader struct {
	config PetAIProviderConfig
	err    error
}

func (r *petImageTestProviderReader) Read(ctx context.Context, _ PetProviderReference) (PetAIProviderConfig, error) {
	if ctx != nil && ctx.Err() != nil {
		return PetAIProviderConfig{}, ctx.Err()
	}
	if r.err != nil {
		return PetAIProviderConfig{}, r.err
	}
	return r.config, nil
}

type petImageTestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f petImageTestRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func petImageTestResponse(status int, contentType, body string) *http.Response {
	response := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
	if contentType != "" {
		response.Header.Set("Content-Type", contentType)
	}
	return response
}

func petImageTestConfig() PetAIProviderConfig {
	return PetAIProviderConfig{
		Platform:   "openai",
		ProviderID: "pet-provider",
		Model:      "image-model",
		BaseURL:    "https://provider.test/v1",
		APIKey:     "pet-image-secret",
		Protocol:   "openai",
	}
}

func petImageTestRequest() PetImageRequest {
	return PetImageRequest{
		PetID:     "pet-1",
		RequestID: "image-1",
		Provider: PetProviderReference{
			Platform:   "openai",
			ProviderID: "pet-provider",
			Model:      "image-model",
			Capability: PetCapabilityImage,
		},
		Prompt: "一只在月光下睡觉的桌宠",
		Size:   "512x512",
		Count:  1,
	}
}

func petImageTestPNG(t *testing.T) []byte {
	return petImageTestPNGWithSize(t, 2, 2)
}

func petImageTestPNGWithSize(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("生成测试 PNG 失败: %v", err)
	}
	return buffer.Bytes()
}

func petImageTestDataURL(imageBytes []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
}

func petImageTestJSONResponse(imageBytes []byte) string {
	return `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageBytes) + `"}]}`
}

func petImageTestReference(t *testing.T, root string, imageBytes []byte) PetImageReference {
	t.Helper()
	skinPath := filepath.Join(root, "skin-current")
	if err := os.MkdirAll(skinPath, 0o700); err != nil {
		t.Fatalf("创建测试皮肤目录失败: %v", err)
	}
	path := filepath.Join(skinPath, "idle.png")
	if err := os.WriteFile(path, imageBytes, 0o600); err != nil {
		t.Fatalf("写入测试 idle frame 失败: %v", err)
	}
	return PetImageReference{
		Path:       path,
		SkinPath:   skinPath,
		MediaType:  "image/png",
		Pose:       "idle",
		FrameIndex: 0,
	}
}

func petImageTestCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，但返回 nil")
	}
	code := PetImageErrorCodeOf(err)
	if code == "" {
		t.Fatalf("错误缺少稳定 code: %T %v", err, err)
	}
	return code
}

func TestPetImageServiceGeneratesB64JSONAndUsesSharedAuth(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	reader := &petImageTestProviderReader{config: petImageTestConfig()}
	transport := petImageTestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/images/generations" {
			t.Fatalf("图片 endpoint = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer pet-image-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		var payload struct {
			Model          string `json:"model"`
			Prompt         string `json:"prompt"`
			Size           string `json:"size"`
			Count          int    `json:"n"`
			ResponseFormat string `json:"response_format"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取请求体失败: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if payload.Model != "image-model" || payload.Prompt != "一只在月光下睡觉的桌宠" || payload.Size != "512x512" || payload.Count != 1 || payload.ResponseFormat != "b64_json" {
			t.Fatalf("图片请求体 = %#v", payload)
		}
		return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
	})

	service := NewPetImageService(reader, transport)
	result, err := service.GenerateImage(context.Background(), petImageTestRequest())
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(result.Images) != 1 || !bytes.Equal(result.Images[0], imageBytes) {
		t.Fatalf("图片结果 = %#v", result)
	}
}

func TestPetImageServiceUsesIdleReferenceWithImagesEditsMultipart(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	reference := petImageTestReference(t, t.TempDir(), imageBytes)
	options := normalizePetImageOptions(PetImageOptions{ReferenceRoot: filepath.Dir(reference.SkinPath)})
	root := filepath.Clean(options.ReferenceRoot)
	skinPath := filepath.Clean(reference.SkinPath)
	path := filepath.Clean(reference.Path)
	rootInfo, rootErr := os.Lstat(root)
	skinInfo, skinErr := os.Lstat(skinPath)
	pathInfo, pathErr := os.Lstat(path)
	if rootErr != nil || skinErr != nil || pathErr != nil || !rootInfo.IsDir() || !skinInfo.IsDir() || !pathInfo.Mode().IsRegular() {
		t.Fatalf("测试 reference 文件状态异常: root=%v skin=%v path=%v", rootErr, skinErr, pathErr)
	}
	if err := validatePetImageReferenceMetadata(reference, options); err != nil {
		t.Fatalf("测试 idle reference 元数据预校验失败: root=%q skin=%q path=%q err=%v", options.ReferenceRoot, reference.SkinPath, reference.Path, err)
	}
	transport := petImageTestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/images/edits" {
			t.Fatalf("图片 edit endpoint = %q", request.URL.Path)
		}
		if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		reader, err := request.MultipartReader()
		if err != nil {
			t.Fatalf("创建 multipart reader 失败: %v", err)
		}
		fields := make(map[string]string)
		var uploaded []byte
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("读取 multipart part 失败: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("读取 multipart part 内容失败: %v", err)
			}
			if part.FormName() == "image" {
				if part.FileName() != "idle.png" || part.Header.Get("Content-Type") != "image/png" {
					t.Fatalf("idle frame part = name:%q filename:%q content-type:%q", part.FormName(), part.FileName(), part.Header.Get("Content-Type"))
				}
				uploaded = data
			} else {
				fields[part.FormName()] = string(data)
			}
		}
		if !bytes.Equal(uploaded, imageBytes) || fields["model"] != "image-model" || fields["prompt"] != "一只在月光下睡觉的桌宠" || fields["size"] != "512x512" || fields["n"] != "1" || fields["response_format"] != "b64_json" {
			t.Fatalf("multipart 请求 = fields:%#v uploaded:%d", fields, len(uploaded))
		}
		return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
	})

	request := petImageTestRequest()
	request.ReferenceImage = &reference
	service := NewPetImageServiceWithOptions(
		&petImageTestProviderReader{config: petImageTestConfig()},
		transport,
		options,
	)
	result, err := service.GenerateImage(context.Background(), request)
	if err != nil || len(result.Images) != 1 || !bytes.Equal(result.Images[0], imageBytes) {
		t.Fatalf("idle reference 结果 = %#v, error=%v", result, err)
	}
}

func TestPetImageServiceUsesCroppedDataReferenceWithImagesEditsMultipart(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	cases := []struct {
		name string
		data string
	}{
		{name: "data URL", data: petImageTestDataURL(imageBytes)},
		{name: "bare base64", data: base64.StdEncoding.EncodeToString(imageBytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := petImageTestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/v1/images/edits" {
					t.Fatalf("图片 data reference endpoint = %q", request.URL.Path)
				}
				reader, err := request.MultipartReader()
				if err != nil {
					t.Fatalf("创建 data reference multipart reader 失败: %v", err)
				}
				var uploaded []byte
				for {
					part, err := reader.NextPart()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatalf("读取 data reference multipart part 失败: %v", err)
					}
					data, err := io.ReadAll(part)
					if err != nil {
						t.Fatalf("读取 data reference multipart 内容失败: %v", err)
					}
					if part.FormName() == "image" {
						if part.FileName() != "idle.png" || part.Header.Get("Content-Type") != "image/png" {
							t.Fatalf("data reference part = name:%q filename:%q content-type:%q", part.FormName(), part.FileName(), part.Header.Get("Content-Type"))
						}
						uploaded = data
					}
				}
				if !bytes.Equal(uploaded, imageBytes) {
					t.Fatalf("data reference 上传内容不等于已裁单帧: %d bytes", len(uploaded))
				}
				return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
			})
			reference := PetImageReference{
				Data:       tc.data,
				MediaType:  "image/png",
				Pose:       "idle",
				FrameIndex: 0,
			}
			request := petImageTestRequest()
			request.ReferenceImage = &reference
			result, err := NewPetImageService(
				&petImageTestProviderReader{config: petImageTestConfig()},
				transport,
			).GenerateImage(context.Background(), request)
			if err != nil || len(result.Images) != 1 || !bytes.Equal(result.Images[0], imageBytes) {
				t.Fatalf("data reference 结果 = %#v, error=%v", result, err)
			}
		})
	}
}

func TestPetImageServiceRejectsUnsafeCroppedDataReference(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	tallAtlasLike := petImageTestPNGWithSize(t, 2, PetImageMaxReferenceDimension+1)
	cases := []struct {
		name      string
		reference PetImageReference
		code      string
	}{
		{
			name: "remote URL",
			reference: PetImageReference{
				Data:      "https://attacker.test/atlas.png",
				MediaType: "image/png",
			},
			code: string(PET_IMAGE_REMOTE_URL_UNSUPPORTED),
		},
		{
			name: "atlas-like dimensions",
			reference: PetImageReference{
				Data:      petImageTestDataURL(tallAtlasLike),
				MediaType: "image/png",
				Pose:      "idle",
			},
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "data and path mixed",
			reference: PetImageReference{
				Data:      petImageTestDataURL(imageBytes),
				Path:      filepath.Join(t.TempDir(), "idle.png"),
				MediaType: "image/png",
			},
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "media type mismatch",
			reference: PetImageReference{
				Data:      petImageTestDataURL(imageBytes),
				MediaType: "image/jpeg",
			},
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transportCalled := false
			service := NewPetImageService(
				&petImageTestProviderReader{config: petImageTestConfig()},
				petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
					transportCalled = true
					return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
				}),
			)
			request := petImageTestRequest()
			request.ReferenceImage = &tc.reference
			_, err := service.GenerateImage(context.Background(), request)
			if got := petImageTestCode(t, err); got != tc.code {
				t.Fatalf("错误码 = %q, want %q", got, tc.code)
			}
			if transportCalled {
				t.Fatal("非法 data reference 不应进入 transport")
			}
		})
	}
}

func TestPetImageServiceRejectsNonIdleOrUnsafeReference(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	root := t.TempDir()
	valid := petImageTestReference(t, root, imageBytes)
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, imageBytes, 0o600); err != nil {
		t.Fatalf("写入越界测试图片失败: %v", err)
	}
	cases := []struct {
		name      string
		reference PetImageReference
		options   PetImageOptions
		code      string
	}{
		{
			name: "atlas is not a frame",
			reference: func() PetImageReference {
				atlas := valid
				atlas.Path = filepath.Join(atlas.SkinPath, "atlas.png")
				if err := os.WriteFile(atlas.Path, imageBytes, 0o600); err != nil {
					t.Fatalf("写入 atlas 测试图片失败: %v", err)
				}
				return atlas
			}(),
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "non idle pose",
			reference: func() PetImageReference {
				nonIdle := valid
				nonIdle.Pose = "walk"
				return nonIdle
			}(),
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "path escapes skin",
			reference: func() PetImageReference {
				escaped := valid
				escaped.Path = outside
				return escaped
			}(),
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "declared media type does not match bytes",
			reference: func() PetImageReference {
				mismatch := valid
				mismatch.MediaType = "image/jpeg"
				return mismatch
			}(),
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name:      "reference exceeds image limit",
			reference: valid,
			options:   PetImageOptions{ReferenceRoot: root, MaxImageBytes: 1},
			code:      string(PET_IMAGE_REQUEST_TOO_LARGE),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := tc.options
			if options.ReferenceRoot == "" {
				options.ReferenceRoot = root
			}
			transportCalled := false
			service := NewPetImageServiceWithOptions(
				&petImageTestProviderReader{config: petImageTestConfig()},
				petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
					transportCalled = true
					return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
				}),
				options,
			)
			request := petImageTestRequest()
			request.ReferenceImage = &tc.reference
			_, err := service.GenerateImage(context.Background(), request)
			if got := petImageTestCode(t, err); got != tc.code {
				t.Fatalf("错误码 = %q, want %q", got, tc.code)
			}
			if transportCalled {
				t.Fatal("非法参考图不应进入 transport")
			}
		})
	}
}

func TestPetImageServicePrefersB64AndRejectsURLWithoutFollowingIt(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	cases := []struct {
		name string
		body string
		code string
	}{
		{
			name: "b64 wins over url",
			body: `{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString(imageBytes) + `","url":"https://attacker.test/image.png"}]}`,
		},
		{
			name: "url only is rejected",
			body: `{"data":[{"url":"https://attacker.test/image.png"}]}`,
			code: string(PET_IMAGE_REMOTE_URL_UNSUPPORTED),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &petImageTestProviderReader{config: petImageTestConfig()}
			transport := petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return petImageTestResponse(http.StatusOK, "application/json", tc.body), nil
			})
			service := NewPetImageService(reader, transport)
			result, err := service.GenerateImage(context.Background(), petImageTestRequest())
			if tc.code == "" {
				if err != nil || len(result.Images) != 1 || !bytes.Equal(result.Images[0], imageBytes) {
					t.Fatalf("b64 优先结果 = %#v, error=%v", result, err)
				}
				return
			}
			if got := petImageTestCode(t, err); got != tc.code {
				t.Fatalf("错误码 = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestPetImageServiceRejectsInvalidImageBytesAndRequestBounds(t *testing.T) {
	cases := []struct {
		name    string
		request PetImageRequest
		options PetImageOptions
		body    string
		code    string
	}{
		{
			name: "invalid image bytes",
			body: petImageTestJSONResponse([]byte("not-an-image")),
			code: string(PET_IMAGE_RESPONSE_INVALID),
		},
		{
			name: "prompt too long",
			request: func() PetImageRequest {
				request := petImageTestRequest()
				request.Prompt = strings.Repeat("x", PetImageMaxPromptLength+1)
				return request
			}(),
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "count too large",
			request: func() PetImageRequest {
				request := petImageTestRequest()
				request.Count = PetImageMaxCount + 1
				return request
			}(),
			code: string(PET_IMAGE_INVALID_REQUEST),
		},
		{
			name: "response too large",
			options: PetImageOptions{
				MaxResponseBytes: 8,
			},
			body: petImageTestJSONResponse([]byte("0123456789")),
			code: string(PET_IMAGE_RESPONSE_TOO_LARGE),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			if request.PetID == "" {
				request = petImageTestRequest()
			}
			transport := petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
				body := tc.body
				if body == "" {
					body = petImageTestJSONResponse(petImageTestPNG(t))
				}
				return petImageTestResponse(http.StatusOK, "application/json", body), nil
			})
			service := NewPetImageServiceWithOptions(
				&petImageTestProviderReader{config: petImageTestConfig()},
				transport,
				tc.options,
			)
			_, err := service.GenerateImage(context.Background(), request)
			if got := petImageTestCode(t, err); got != tc.code {
				t.Fatalf("错误码 = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestPetImageServiceMapsCancellationAndNon2xxWithoutSensitiveText(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		service := NewPetImageService(
			&petImageTestProviderReader{config: petImageTestConfig()},
			petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("取消的请求不应进入 transport")
				return nil, nil
			}),
		)
		_, err := service.GenerateImage(ctx, petImageTestRequest())
		if got := petImageTestCode(t, err); got != string(PET_IMAGE_REQUEST_CANCELLED) {
			t.Fatalf("取消错误码 = %q", got)
		}
	})

	t.Run("non-2xx", func(t *testing.T) {
		service := NewPetImageService(
			&petImageTestProviderReader{config: petImageTestConfig()},
			petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return petImageTestResponse(http.StatusBadGateway, "application/json", `{"error":"apiKey=pet-image-secret"}`), nil
			}),
		)
		_, err := service.GenerateImage(context.Background(), petImageTestRequest())
		if got := petImageTestCode(t, err); got != string(PET_IMAGE_UPSTREAM_ERROR) {
			t.Fatalf("上游错误码 = %q", got)
		}
		if !errors.Is(err, &PetAIError{Code: PET_IMAGE_UPSTREAM_ERROR}) || strings.Contains(err.Error(), "pet-image-secret") {
			t.Fatalf("上游错误投影不安全: %v", err)
		}
		var imageErr *PetAIError
		if !errors.As(err, &imageErr) || imageErr.Status != http.StatusBadGateway {
			t.Fatalf("上游状态 = %#v", imageErr)
		}
	})
}
