package modelpricing

import (
	"math"
	"testing"
)

func TestCalculateCostIncludesReturnedImageCount(t *testing.T) {
	service, err := NewService()
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// 图片模型的返回张数单独计价；这里同时放入 token，验证两种计价不会互相覆盖。
	got := service.CalculateCost("aiml/dall-e-3", UsageSnapshot{
		InputTokens:  100,
		OutputTokens: 20,
		ImageCount:   2,
	})
	const wantImageCost = 0.084
	if got.ImageCost != wantImageCost {
		t.Fatalf("ImageCost = %v, want %v", got.ImageCost, wantImageCost)
	}
	if got.TotalCost < got.ImageCost || !got.HasPricing {
		t.Fatalf("image cost should be included in total pricing: %+v", got)
	}
}

func TestCalculateCostDoesNotTreatImageCountAsTokens(t *testing.T) {
	service, err := NewService()
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	withImages := service.CalculateCost("aiml/dall-e-3", UsageSnapshot{ImageCount: 1})
	withoutImages := service.CalculateCost("aiml/dall-e-3", UsageSnapshot{})
	if withImages.InputCost != 0 || withImages.OutputCost != 0 || withImages.ReasoningCost != 0 {
		t.Fatalf("image count must not become token usage: %+v", withImages)
	}
	if withImages.TotalCost-withoutImages.TotalCost != withImages.ImageCost {
		t.Fatalf("image delta = %v, image cost = %v", withImages.TotalCost-withoutImages.TotalCost, withImages.ImageCost)
	}
}

func TestCalculateCostForGPTImageUsesPixelPriceAndDefaultSize(t *testing.T) {
	service, err := NewService()
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// gpt-image-1 的价格表没有 output_cost_per_image，而是用 input_cost_per_pixel
	// 表示生成图价格；无尺寸代表旧日志，必须按服务默认 1024x1024 兼容计价。
	got := service.CalculateCost("gpt-image-1", UsageSnapshot{ImageCount: 1})
	const want = 0.042
	if math.Abs(got.ImageCost-want) > 1e-9 {
		t.Fatalf("gpt-image-1 ImageCost = %v, want %v", got.ImageCost, want)
	}
	if !got.HasPricing || math.Abs(got.TotalCost-want) > 1e-9 {
		t.Fatalf("gpt-image-1 pricing = %+v", got)
	}
}

func TestCalculateCostForGPTImageUsesRequestedPixels(t *testing.T) {
	service, err := NewService()
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	got := service.CalculateCost("gpt-image-1", UsageSnapshot{
		ImageCount:  2,
		ImageWidth:  1024,
		ImageHeight: 1536,
	})
	const want = 0.126
	if math.Abs(got.ImageCost-want) > 1e-9 {
		t.Fatalf("gpt-image-1 landscape ImageCost = %v, want %v", got.ImageCost, want)
	}
}

func TestCalculateCostForImagePricingVariantReadsSizeFromModel(t *testing.T) {
	service, err := NewService()
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// 变体模型名自带尺寸时，即使日志没有新尺寸字段，也必须使用变体对应像素数。
	got := service.CalculateCost("medium/1024-x-1536/gpt-image-1", UsageSnapshot{ImageCount: 1})
	const want = 0.063
	if math.Abs(got.ImageCost-want) > 1e-9 {
		t.Fatalf("image pricing variant ImageCost = %v, want %v", got.ImageCost, want)
	}
}
