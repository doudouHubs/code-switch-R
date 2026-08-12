package services

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
)

const (
	petMediaAPIMaxInputBytes    int64 = 16 << 20
	petMediaAPIMaxOutputBytes   int64 = 32 << 20
	petMediaAPIMaxActions             = 32
	petMediaAPIMaxFrames              = 64
	petActionSheetMaxFrames           = 8
	petActionSheetMaxInputBytes int64 = 32 << 20
)

// PetMediaAPIService 将已经存在的媒体纯函数暴露给 Wails。
// 请求只接受 base64 图片数据，不接受任意 filePath；这样生成链路可以复用同一套
// ChromaKey/Normalize/Atlas 规则，同时不把 Electron 文件 IPC 的越权边界带进目标应用。
type PetMediaAPIService struct{}

func NewPetMediaAPIService() *PetMediaAPIService {
	return &PetMediaAPIService{}
}

type PetChromaKeyRequest struct {
	Data      string `json:"data"`
	KeyColor  string `json:"keyColor,omitempty"`
	Tolerance int    `json:"tolerance,omitempty"`
	Softness  int    `json:"softness,omitempty"`
}

type PetChromaKeyResult struct {
	Data      string `json:"data"`
	MediaType string `json:"mediaType"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type PetSpriteNormalizationAPIRequest struct {
	Data           string `json:"data"`
	TargetHeight   int    `json:"targetHeight,omitempty"`
	AlphaThreshold int    `json:"alphaThreshold,omitempty"`
	PaddingX       int    `json:"paddingX,omitempty"`
	PaddingY       int    `json:"paddingY,omitempty"`
}

type PetSpriteNormalizationAPIResult struct {
	Data           string          `json:"data"`
	MediaType      string          `json:"mediaType"`
	OriginalBounds PetSpriteBounds `json:"originalBounds"`
	SourceBounds   PetSpriteBounds `json:"sourceBounds"`
	SubjectBounds  PetSpriteBounds `json:"subjectBounds"`
	OutputWidth    int             `json:"outputWidth"`
	OutputHeight   int             `json:"outputHeight"`
}

type PetAtlasPackAPIAction struct {
	ID          string   `json:"id"`
	Frames      []string `json:"frames"`
	DurationsMS []int    `json:"durationsMs,omitempty"`
	Loop        bool     `json:"loop"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
}

type PetAtlasPackAPIRequest struct {
	Name      string                  `json:"name,omitempty"`
	Actions   []PetAtlasPackAPIAction `json:"actions"`
	MaxWidth  int                     `json:"maxWidth,omitempty"`
	MaxHeight int                     `json:"maxHeight,omitempty"`
	Padding   int                     `json:"padding,omitempty"`
}

type PetAtlasPackAPIResult struct {
	Data      string           `json:"data"`
	MediaType string           `json:"mediaType"`
	Manifest  PetAtlasManifest `json:"manifest"`
	Atlas     PetAtlasMetadata `json:"atlas"`
}

// PetActionSheetLayout 与源项目共享层保持相同的布局规则；Studio 生成多帧动作时，
// 模型先返回一张序列图，再由这里按固定网格拆成可归一化的独立帧。
type PetActionSheetLayout struct {
	Columns    int `json:"columns"`
	Rows       int `json:"rows"`
	FrameCount int `json:"frameCount"`
}

type PetActionSheetSplitRequest struct {
	Data       string `json:"data"`
	FrameCount int    `json:"frameCount"`
}

type PetActionSheetSplitFrame struct {
	Data   string `json:"data"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type PetActionSheetSplitResult struct {
	Layout PetActionSheetLayout       `json:"layout"`
	Frames []PetActionSheetSplitFrame `json:"frames"`
}

func resolvePetActionSheetLayout(frameCount int) (PetActionSheetLayout, error) {
	if frameCount < 1 || frameCount > petActionSheetMaxFrames {
		return PetActionSheetLayout{}, fmt.Errorf("pet action sheet requires 1-%d frames", petActionSheetMaxFrames)
	}
	columns := 4
	switch {
	case frameCount <= 2:
		columns = frameCount
	case frameCount <= 4:
		columns = 2
	case frameCount <= 6:
		columns = 3
	}
	return PetActionSheetLayout{
		Columns:    columns,
		Rows:       (frameCount + columns - 1) / columns,
		FrameCount: frameCount,
	}, nil
}

// SplitActionSheet 只接受 PNG base64，且只在内存中处理。源项目的实现写临时文件是
// Electron IPC 约束，在 Wails 目标栈里没有必要；返回裸 base64 还能让前端继续复用
// NormalizeSprite 的单帧校验和透明背景处理。
func (api *PetMediaAPIService) SplitActionSheet(request PetActionSheetSplitRequest) (PetActionSheetSplitResult, error) {
	layout, err := resolvePetActionSheetLayout(request.FrameCount)
	if err != nil {
		return PetActionSheetSplitResult{}, err
	}
	encoded := strings.TrimSpace(request.Data)
	if encoded == "" || strings.Contains(encoded, ",") || strings.ContainsAny(encoded, " \t\r\n") {
		return PetActionSheetSplitResult{}, errors.New("pet action sheet data must be bare base64 PNG")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return PetActionSheetSplitResult{}, errors.New("pet action sheet data is invalid")
	}
	if int64(len(data)) > petActionSheetMaxInputBytes {
		return PetActionSheetSplitResult{}, errors.New("pet action sheet input is too large")
	}
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return PetActionSheetSplitResult{}, fmt.Errorf("decode pet action sheet PNG: %w", err)
	}
	bounds := source.Bounds()
	if bounds.Empty() || bounds.Dx() < layout.Columns || bounds.Dy() < layout.Rows ||
		bounds.Dx() > PetAtlasMaxTextureSize || bounds.Dy() > PetAtlasMaxTextureSize {
		return PetActionSheetSplitResult{}, errors.New("pet action sheet dimensions are outside the supported grid range")
	}

	frames := make([]PetActionSheetSplitFrame, 0, layout.FrameCount)
	var outputBytes int64
	for index := 0; index < layout.FrameCount; index++ {
		row := index / layout.Columns
		column := index % layout.Columns
		rowStart := row * layout.Columns
		rowFrameCount := layout.FrameCount - rowStart
		if rowFrameCount > layout.Columns {
			rowFrameCount = layout.Columns
		}
		// 最后一行不足整列时右对齐，保持序列图最后一帧贴住右边界，
		// 这样源图尺寸不能整除列数时，余数仍归属于最后一帧而不是空洞。
		if row == layout.Rows-1 && rowFrameCount < layout.Columns {
			column += layout.Columns - rowFrameCount
		}
		// 按比例计算边界，余数只落到相邻格子的边界上，避免非整除尺寸丢掉最右/最下像素。
		x := bounds.Min.X + column*bounds.Dx()/layout.Columns
		right := bounds.Min.X + (column+1)*bounds.Dx()/layout.Columns
		y := bounds.Min.Y + row*bounds.Dy()/layout.Rows
		bottom := bounds.Min.Y + (row+1)*bounds.Dy()/layout.Rows
		cropBounds := image.Rect(x, y, right, bottom)
		if cropBounds.Dx() < 1 || cropBounds.Dy() < 1 {
			return PetActionSheetSplitResult{}, errors.New("pet action sheet is too small for the requested grid layout")
		}
		crop := image.NewNRGBA(image.Rect(0, 0, cropBounds.Dx(), cropBounds.Dy()))
		draw.Draw(crop, crop.Bounds(), source, cropBounds.Min, draw.Src)
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, crop); err != nil {
			return PetActionSheetSplitResult{}, fmt.Errorf("encode pet action sheet frame %d: %w", index+1, err)
		}
		outputBytes += int64(buffer.Len())
		if outputBytes > petMediaAPIMaxOutputBytes {
			return PetActionSheetSplitResult{}, errors.New("pet action sheet output is too large")
		}
		frames = append(frames, PetActionSheetSplitFrame{
			Data:   base64.StdEncoding.EncodeToString(buffer.Bytes()),
			Width:  cropBounds.Dx(),
			Height: cropBounds.Dy(),
		})
	}
	return PetActionSheetSplitResult{Layout: layout, Frames: frames}, nil
}

// ApplyChromaKey 将生成图片的指定色键背景转成透明像素。
func (api *PetMediaAPIService) ApplyChromaKey(request PetChromaKeyRequest) (PetChromaKeyResult, error) {
	source, _, err := decodePetMediaImage(request.Data)
	if err != nil {
		return PetChromaKeyResult{}, err
	}
	options := DefaultPetChromaKeyOptions()
	if strings.TrimSpace(request.KeyColor) != "" {
		options.KeyColor, err = parsePetMediaColor(request.KeyColor)
		if err != nil {
			return PetChromaKeyResult{}, err
		}
	}
	if request.Tolerance != 0 {
		if request.Tolerance < 0 || request.Tolerance > 255 {
			return PetChromaKeyResult{}, errors.New("pet chroma-key tolerance must be between 0 and 255")
		}
		options.Tolerance = uint8(request.Tolerance)
	}
	if request.Softness != 0 {
		if request.Softness < 0 || request.Softness > 255 {
			return PetChromaKeyResult{}, errors.New("pet chroma-key softness must be between 0 and 255")
		}
		options.Softness = uint8(request.Softness)
	}
	output, err := ApplyPetChromaKey(source, options)
	if err != nil {
		return PetChromaKeyResult{}, err
	}
	pngBytes, err := encodePNG(output)
	if err != nil {
		return PetChromaKeyResult{}, fmt.Errorf("encode chroma-key result: %w", err)
	}
	if int64(len(pngBytes)) > petMediaAPIMaxOutputBytes {
		return PetChromaKeyResult{}, errors.New("pet chroma-key output is too large")
	}
	return PetChromaKeyResult{
		Data:      base64.StdEncoding.EncodeToString(pngBytes),
		MediaType: "image/png",
		Width:     output.Bounds().Dx(),
		Height:    output.Bounds().Dy(),
	}, nil
}

// NormalizeSprite 裁剪透明边缘并按统一主体高度生成 PNG，供 atlas 生成前复用。
func (api *PetMediaAPIService) NormalizeSprite(request PetSpriteNormalizationAPIRequest) (PetSpriteNormalizationAPIResult, error) {
	source, _, err := decodePetMediaImage(request.Data)
	if err != nil {
		return PetSpriteNormalizationAPIResult{}, err
	}
	if request.TargetHeight < 0 || request.AlphaThreshold < 0 || request.AlphaThreshold > 255 || request.PaddingX < 0 || request.PaddingY < 0 {
		return PetSpriteNormalizationAPIResult{}, errors.New("pet sprite normalization options are invalid")
	}
	result, err := NormalizePetSprite(source, PetSpriteNormalizationOptions{
		TargetHeight:   request.TargetHeight,
		AlphaThreshold: uint8(request.AlphaThreshold),
		PaddingX:       request.PaddingX,
		PaddingY:       request.PaddingY,
	})
	if err != nil {
		return PetSpriteNormalizationAPIResult{}, err
	}
	if int64(len(result.PNG)) > petMediaAPIMaxOutputBytes {
		return PetSpriteNormalizationAPIResult{}, errors.New("normalized pet sprite output is too large")
	}
	return PetSpriteNormalizationAPIResult{
		Data:           base64.StdEncoding.EncodeToString(result.PNG),
		MediaType:      "image/png",
		OriginalBounds: result.OriginalBounds,
		SourceBounds:   result.SourceBounds,
		SubjectBounds:  result.SubjectBounds,
		OutputWidth:    result.OutputWidth,
		OutputHeight:   result.OutputHeight,
	}, nil
}

// PackAtlas 将每个 action 的 base64 帧按固定 action-row 布局打包。
func (api *PetMediaAPIService) PackAtlas(request PetAtlasPackAPIRequest) (PetAtlasPackAPIResult, error) {
	if len(request.Actions) == 0 || len(request.Actions) > petMediaAPIMaxActions {
		return PetAtlasPackAPIResult{}, fmt.Errorf("pet atlas requires 1-%d actions", petMediaAPIMaxActions)
	}
	totalInputBytes := int64(0)
	actions := make([]PetAtlasAction, 0, len(request.Actions))
	for _, action := range request.Actions {
		if len(action.Frames) == 0 || len(action.Frames) > petMediaAPIMaxFrames {
			return PetAtlasPackAPIResult{}, fmt.Errorf("pet atlas action %q requires 1-%d frames", action.ID, petMediaAPIMaxFrames)
		}
		frames := make([]image.Image, 0, len(action.Frames))
		for _, encoded := range action.Frames {
			frame, size, err := decodePetMediaImage(encoded)
			if err != nil {
				return PetAtlasPackAPIResult{}, fmt.Errorf("decode pet atlas frame %q: %w", action.ID, err)
			}
			totalInputBytes += int64(size)
			if totalInputBytes > petMediaAPIMaxInputBytes {
				return PetAtlasPackAPIResult{}, errors.New("pet atlas input is too large")
			}
			frames = append(frames, frame)
		}
		actions = append(actions, PetAtlasAction{
			ID:          action.ID,
			Frames:      frames,
			DurationsMS: action.DurationsMS,
			Loop:        action.Loop,
			Label:       action.Label,
			Description: action.Description,
		})
	}
	packed, err := PackPetAtlas(PetAtlasPackRequest{
		Name:      request.Name,
		Actions:   actions,
		MaxWidth:  request.MaxWidth,
		MaxHeight: request.MaxHeight,
		Padding:   request.Padding,
	})
	if err != nil {
		return PetAtlasPackAPIResult{}, err
	}
	if int64(len(packed.PNG)) > petMediaAPIMaxOutputBytes {
		return PetAtlasPackAPIResult{}, errors.New("pet atlas output is too large")
	}
	return PetAtlasPackAPIResult{
		Data:      base64.StdEncoding.EncodeToString(packed.PNG),
		MediaType: "image/png",
		Manifest:  packed.Manifest,
		Atlas:     packed.Atlas,
	}, nil
}

func decodePetMediaImage(encoded string) (image.Image, int, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || strings.Contains(encoded, ",") || strings.ContainsAny(encoded, " \t\r\n") {
		return nil, 0, errors.New("pet media image data must be bare base64")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return nil, 0, errors.New("pet media image data is invalid")
	}
	if int64(len(data)) > petMediaAPIMaxInputBytes {
		return nil, 0, errors.New("pet media image input is too large")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("decode pet media image: %w", err)
	}
	bounds := source.Bounds()
	if bounds.Empty() || bounds.Dx() > PetAtlasMaxTextureSize || bounds.Dy() > PetAtlasMaxTextureSize {
		return nil, 0, errors.New("pet media image dimensions are invalid")
	}
	return source, len(data), nil
}

func parsePetMediaColor(value string) (color.RGBA, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "#"))
	if len(value) != 6 {
		return color.RGBA{}, errors.New("pet chroma-key color must be a six-digit hex value")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return color.RGBA{}, errors.New("pet chroma-key color is invalid")
	}
	return color.RGBA{R: decoded[0], G: decoded[1], B: decoded[2], A: 255}, nil
}
