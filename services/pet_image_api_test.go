package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestPetImageAPIUnavailableReturnsStructuredError(t *testing.T) {
	api := NewPetImageAPIService(nil)
	if _, err := api.GenerateImage(PetImageRequest{}); PetImageErrorCodeOf(err) != string(PET_IMAGE_DEPENDENCY_UNAVAILABLE) {
		t.Fatalf("nil service 错误码 = %q, want %q", PetImageErrorCodeOf(err), PET_IMAGE_DEPENDENCY_UNAVAILABLE)
	}
	var imageErr *PetAIError
	if _, err := api.GenerateImage(PetImageRequest{}); !errors.As(err, &imageErr) || imageErr == nil {
		t.Fatalf("nil service 错误类型 = %T", err)
	}

	var nilAPI *PetImageAPIService
	if _, err := nilAPI.GenerateImage(PetImageRequest{}); PetImageErrorCodeOf(err) != string(PET_IMAGE_DEPENDENCY_UNAVAILABLE) {
		t.Fatalf("nil API receiver 错误码 = %q", PetImageErrorCodeOf(err))
	}
}

func TestPetImageAPIForwardsAndSerializesBytesAsBase64(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	service := NewPetImageService(
		&petImageTestProviderReader{config: petImageTestConfig()},
		petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
		}),
	)
	api := NewPetImageAPIService(service)
	result, err := api.GenerateImage(petImageTestRequest())
	if err != nil {
		t.Fatalf("bridge GenerateImage() error = %v", err)
	}
	if len(result.Images) != 1 || string(result.Images[0]) != string(imageBytes) {
		t.Fatalf("bridge result = %#v", result)
	}

	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化 PetImageResult 失败: %v", err)
	}
	var encoded struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(payload, &encoded); err != nil {
		t.Fatalf("解析 PetImageResult JSON 失败: %v", err)
	}
	if len(encoded.Images) != 1 || encoded.Images[0] != base64.StdEncoding.EncodeToString(imageBytes) {
		t.Fatalf("图片 base64 = %#v", encoded)
	}
}

func TestPetImageAPIUsesBackgroundContextAndKeepsProviderOutOfResult(t *testing.T) {
	service := NewPetImageService(
		&petImageTestProviderReader{config: petImageTestConfig()},
		petImageTestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Context() == context.Background() {
				t.Fatal("HTTP request 不应直接使用不可超时的 Background context")
			}
			return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(petImageTestPNG(t))), nil
		}),
	)
	result, err := NewPetImageAPIService(service).GenerateImage(petImageTestRequest())
	if err != nil || len(result.Images) != 1 {
		t.Fatalf("bridge 调用结果 = %#v, error=%v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("序列化结果失败: %v", err)
	}
	if string(encoded) == "" || string(encoded) == "pet-image-secret" {
		t.Fatalf("结果异常: %s", encoded)
	}
}

func TestPetImageAPIForwardsIdleReferenceToEditPath(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	reference := petImageTestReference(t, t.TempDir(), imageBytes)
	service := NewPetImageServiceWithOptions(
		&petImageTestProviderReader{config: petImageTestConfig()},
		petImageTestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/v1/images/edits" {
				t.Fatalf("API bridge edit endpoint = %q", request.URL.Path)
			}
			if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
				t.Fatalf("API bridge Content-Type 异常: %q", request.Header.Get("Content-Type"))
			}
			return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
		}),
		PetImageOptions{ReferenceRoot: filepath.Dir(reference.SkinPath)},
	)
	request := petImageTestRequest()
	request.ReferenceImage = &reference
	result, err := NewPetImageAPIService(service).GenerateImage(request)
	if err != nil || len(result.Images) != 1 {
		t.Fatalf("API bridge idle reference 结果 = %#v, error=%v", result, err)
	}
}

func TestPetImageAPIForwardsCroppedDataURLReferenceToEditPath(t *testing.T) {
	imageBytes := petImageTestPNG(t)
	reference := PetImageReference{
		Data:       petImageTestDataURL(imageBytes),
		MediaType:  "image/png",
		Pose:       "idle",
		FrameIndex: 0,
	}
	service := NewPetImageService(
		&petImageTestProviderReader{config: petImageTestConfig()},
		petImageTestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/v1/images/edits" {
				t.Fatalf("API bridge data reference endpoint = %q", request.URL.Path)
			}
			reader, err := request.MultipartReader()
			if err != nil {
				t.Fatalf("API bridge data reference multipart reader 失败: %v", err)
			}
			for {
				part, err := reader.NextPart()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("读取 API bridge data reference part 失败: %v", err)
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("读取 API bridge data reference 内容失败: %v", err)
				}
				if part.FormName() == "image" && !bytes.Equal(data, imageBytes) {
					t.Fatalf("API bridge data reference 上传内容不一致: %d bytes", len(data))
				}
			}
			return petImageTestResponse(http.StatusOK, "application/json", petImageTestJSONResponse(imageBytes)), nil
		}),
	)
	request := petImageTestRequest()
	request.ReferenceImage = &reference
	result, err := NewPetImageAPIService(service).GenerateImage(request)
	if err != nil || len(result.Images) != 1 || !bytes.Equal(result.Images[0], imageBytes) {
		t.Fatalf("API bridge data reference 结果 = %#v, error=%v", result, err)
	}
}
