package services

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	imagedraw "image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
)

const (
	PetSpriteTargetHeight   = 320
	PetAtlasVersion         = 1
	PetAtlasMaxTextureSize  = 8192
	PetAtlasFramePadding    = 2
	PetAtlasDefaultDuration = 240
	PetAtlasMaxBehaviors    = 64
	petAtlasMaxActionIDLen  = 64
	petAtlasMaxLabelLen     = 80
	petAtlasMaxDescription  = 500
	petAtlasMaxSubjectLen   = 512
	petAtlasMaxModelIDLen   = 256
)

var (
	ErrPetAssetRootRequired    = errors.New("pet asset root is required")
	ErrPetAssetRootAbsolute    = errors.New("pet asset root must be absolute")
	ErrPetAssetRootDirectory   = errors.New("pet asset root must be a directory")
	ErrPetAssetPathRequired    = errors.New("pet asset path is required")
	ErrPetAssetPathTraversal   = errors.New("pet asset path traversal is not allowed")
	ErrPetAssetPathOutsideRoot = errors.New("pet asset path is outside the pet asset root")
	ErrPetAssetExtension       = errors.New("unsupported pet asset extension")
)

var petAssetExtensions = map[string]struct{}{
	".bmp":  {},
	".gif":  {},
	".jpeg": {},
	".jpg":  {},
	".png":  {},
	".webp": {},
}

// ValidatePetAssetPath 返回资源根目录内的规范路径。
// 模型或用户提供的路径不能被当成可信输入：filepath.Clean 只处理当前系统的分隔符，
// 因此这里同时检查 '/' 和 '\\'，并额外拦截绝对路径越界以及已存在的 symlink 逃逸。
func ValidatePetAssetPath(root, requested string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", ErrPetAssetRootRequired
	}
	if strings.IndexByte(root, 0) >= 0 {
		return "", fmt.Errorf("%w: path contains NUL byte", ErrPetAssetRootAbsolute)
	}
	rootPath := filepath.Clean(root)
	if !filepath.IsAbs(rootPath) {
		return "", ErrPetAssetRootAbsolute
	}
	if info, err := os.Stat(rootPath); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%w: %s", ErrPetAssetRootDirectory, rootPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect pet asset root %q: %w", rootPath, err)
	}

	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", ErrPetAssetPathRequired
	}
	if strings.IndexByte(requested, 0) >= 0 {
		return "", fmt.Errorf("%w: path contains NUL byte", ErrPetAssetPathTraversal)
	}
	if hasParentPathSegment(requested) {
		return "", fmt.Errorf("%w: %q", ErrPetAssetPathTraversal, requested)
	}

	var candidate string
	if filepath.IsAbs(requested) {
		candidate = filepath.Clean(requested)
	} else {
		// Windows 风格绝对路径在 Unix 上不会被 filepath.IsAbs 识别，必须单独拒绝，
		// 否则同一份用户输入跨平台运行时会产生不同的安全结论。
		if isCrossPlatformAbsolute(requested) {
			return "", fmt.Errorf("%w: absolute path %q is not inside the root", ErrPetAssetPathOutsideRoot, requested)
		}
		relative := filepath.FromSlash(strings.ReplaceAll(requested, "\\", "/"))
		candidate = filepath.Clean(filepath.Join(rootPath, relative))
	}

	if !pathWithin(rootPath, candidate) {
		return "", fmt.Errorf("%w: %q", ErrPetAssetPathOutsideRoot, requested)
	}
	extension := strings.ToLower(filepath.Ext(filepath.ToSlash(candidate)))
	if _, ok := petAssetExtensions[extension]; !ok {
		return "", fmt.Errorf("%w %q; allowed extensions: .png, .jpg, .jpeg, .webp, .bmp, .gif", ErrPetAssetExtension, extension)
	}

	rootReal := rootPath
	if resolved, err := filepath.EvalSymlinks(rootPath); err == nil {
		rootReal = filepath.Clean(resolved)
	}
	if err := rejectSymlinkEscape(rootReal, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

// ResolvePetAssetPath 是 ValidatePetAssetPath 的语义别名，方便调用方按“解析”命名接入。
func ResolvePetAssetPath(root, requested string) (string, error) {
	return ValidatePetAssetPath(root, requested)
}

func hasParentPathSegment(path string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isCrossPlatformAbsolute(path string) bool {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return true
	}
	if len(path) >= 2 && unicode.IsLetter(rune(path[0])) && path[1] == ':' {
		return true
	}
	return false
}

func canonicalPathForCompare(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func pathWithin(root, candidate string) bool {
	root = canonicalPathForCompare(root)
	candidate = canonicalPathForCompare(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func rejectSymlinkEscape(rootReal, candidate string) error {
	probe := candidate
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !pathWithin(rootReal, resolved) {
				return fmt.Errorf("%w: symlink resolves outside the root", ErrPetAssetPathTraversal)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect pet asset path %q: %w", candidate, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
	}
}

// PetChromaKeyOptions 控制背景色键的硬阈值和边缘渐变宽度，颜色通道使用 0-255。
type PetChromaKeyOptions struct {
	KeyColor  color.RGBA
	Tolerance uint8
	Softness  uint8
}

// DefaultPetChromaKeyOptions 使用常见的绿色幕布参数；调用方仍可以显式指定任意颜色。
func DefaultPetChromaKeyOptions() PetChromaKeyOptions {
	return PetChromaKeyOptions{
		KeyColor:  color.RGBA{R: 0, G: 255, B: 0, A: 255},
		Tolerance: 32,
		Softness:  64,
	}
}

// ApplyPetChromaKey 只操作 image.Image，不依赖 Electron、命令行工具或临时文件。
// 输出 alpha 是原始 alpha 与色键 alpha 的乘积，避免把原图已有的半透明边缘误改成不透明。
func ApplyPetChromaKey(source image.Image, options PetChromaKeyOptions) (*image.NRGBA, error) {
	if source == nil {
		return nil, errors.New("pet chroma key source image is nil")
	}
	bounds := source.Bounds()
	if bounds.Empty() {
		return nil, errors.New("pet chroma key source image is empty")
	}
	output := image.NewNRGBA(bounds)
	softness := int(options.Softness)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(source.At(x, y)).(color.NRGBA)
			if pixel.A == 0 {
				output.SetNRGBA(x, y, pixel)
				continue
			}
			distance := maxColorChannelDistance(pixel, options.KeyColor)
			alpha := chromaAlpha(distance, int(options.Tolerance), softness)
			pixel.A = uint8(math.Round(float64(pixel.A) * alpha))
			if pixel.A == 0 {
				// 清掉完全透明像素的隐藏 RGB，避免后续缩放或导出重新带出色键污染。
				pixel.R, pixel.G, pixel.B = 0, 0, 0
			}
			output.SetNRGBA(x, y, pixel)
		}
	}
	return output, nil
}

// ChromaKeyImage 是 ApplyPetChromaKey 的简短别名。
func ChromaKeyImage(source image.Image, options PetChromaKeyOptions) (*image.NRGBA, error) {
	return ApplyPetChromaKey(source, options)
}

func maxColorChannelDistance(pixel color.NRGBA, key color.RGBA) int {
	red := absInt(int(pixel.R) - int(key.R))
	green := absInt(int(pixel.G) - int(key.G))
	blue := absInt(int(pixel.B) - int(key.B))
	return maxInt(red, maxInt(green, blue))
}

func chromaAlpha(distance, tolerance, softness int) float64 {
	if distance <= tolerance {
		return 0
	}
	if softness <= 0 || distance >= tolerance+softness {
		return 1
	}
	t := float64(distance-tolerance) / float64(softness)
	return t * t * (3 - 2*t)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// PetSpriteBounds 使用与 OpenCowork manifest 相同的 x/y/width/height 语义。
type PetSpriteBounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (bounds PetSpriteBounds) rectangle() image.Rectangle {
	return image.Rect(bounds.X, bounds.Y, bounds.X+bounds.Width, bounds.Y+bounds.Height)
}

func spriteBoundsFromRectangle(bounds image.Rectangle) PetSpriteBounds {
	return PetSpriteBounds{
		X:      bounds.Min.X,
		Y:      bounds.Min.Y,
		Width:  bounds.Dx(),
		Height: bounds.Dy(),
	}
}

// FindPetSpriteSubjectBounds 根据 alpha 自动生成主体框；不接受外部传入坐标，避免模型生成的
// 越界或负数几何进入裁剪和 atlas 渲染流程。threshold 默认为 1，兼容源实现对极小 alpha 的忽略。
func FindPetSpriteSubjectBounds(source image.Image, threshold ...uint8) (PetSpriteBounds, error) {
	if source == nil {
		return PetSpriteBounds{}, errors.New("pet sprite source image is nil")
	}
	if len(threshold) > 1 {
		return PetSpriteBounds{}, errors.New("pet sprite alpha threshold accepts at most one value")
	}
	alphaThreshold := uint8(1)
	if len(threshold) == 1 {
		alphaThreshold = threshold[0]
	}
	bounds := source.Bounds()
	if bounds.Empty() {
		return PetSpriteBounds{}, errors.New("pet sprite source image is empty")
	}
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X-1, bounds.Min.Y-1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(source.At(x, y)).(color.NRGBA)
			if pixel.A <= alphaThreshold {
				continue
			}
			minX = petMediaMinInt(minX, x)
			minY = petMediaMinInt(minY, y)
			maxX = maxInt(maxX, x)
			maxY = maxInt(maxY, y)
		}
	}
	if maxX < minX || maxY < minY {
		return PetSpriteBounds{}, errors.New("pet sprite does not contain any visible pixels")
	}
	return spriteBoundsFromRectangle(image.Rect(minX, minY, maxX+1, maxY+1)), nil
}

// FindPetSpriteSubjectRect 返回标准库 image.Rectangle 版本的主体框。
func FindPetSpriteSubjectRect(source image.Image, threshold ...uint8) (image.Rectangle, error) {
	bounds, err := FindPetSpriteSubjectBounds(source, threshold...)
	if err != nil {
		return image.Rectangle{}, err
	}
	return bounds.rectangle(), nil
}

type PetSpriteNormalizationOptions struct {
	TargetHeight   int
	AlphaThreshold uint8
	PaddingX       int
	PaddingY       int
}

type PetSpriteNormalizationResult struct {
	PNG            []byte
	OriginalBounds PetSpriteBounds
	SourceBounds   PetSpriteBounds
	SubjectBounds  PetSpriteBounds
	OutputWidth    int
	OutputHeight   int
}

// NormalizePetSprite 裁剪透明边缘、按主体高度等比缩放，并以 PNG bytes 返回。
// Padding 只产生透明画布，不拉伸主体；主体落在画布底部中心，后续 atlas 可以复用同一锚点。
func NormalizePetSprite(source image.Image, options ...PetSpriteNormalizationOptions) (PetSpriteNormalizationResult, error) {
	if len(options) > 1 {
		return PetSpriteNormalizationResult{}, errors.New("pet sprite normalization accepts at most one options value")
	}
	config := PetSpriteNormalizationOptions{TargetHeight: PetSpriteTargetHeight}
	if len(options) == 1 {
		config = options[0]
		if config.TargetHeight == 0 {
			config.TargetHeight = PetSpriteTargetHeight
		}
	}
	if config.TargetHeight < 1 || config.TargetHeight > PetAtlasMaxTextureSize {
		return PetSpriteNormalizationResult{}, fmt.Errorf("pet sprite target height must be between 1 and %d", PetAtlasMaxTextureSize)
	}
	if config.PaddingX < 0 || config.PaddingY < 0 {
		return PetSpriteNormalizationResult{}, errors.New("pet sprite padding cannot be negative")
	}
	alphaThreshold := config.AlphaThreshold
	if alphaThreshold == 0 {
		// 与源实现一致，alpha 只有 1 的边缘噪声不应把主体框扩大；需要保留更弱像素时由调用方传入更高精度的输入策略。
		alphaThreshold = 1
	}
	sourceBounds, err := FindPetSpriteSubjectBounds(source, alphaThreshold)
	if err != nil {
		return PetSpriteNormalizationResult{}, err
	}
	if sourceBounds.Width > PetAtlasMaxTextureSize || sourceBounds.Height > PetAtlasMaxTextureSize {
		return PetSpriteNormalizationResult{}, fmt.Errorf("pet sprite subject exceeds %dx%d", PetAtlasMaxTextureSize, PetAtlasMaxTextureSize)
	}
	local := imageToNRGBA(source, source.Bounds())
	localSubject := image.Rect(
		sourceBounds.X-source.Bounds().Min.X,
		sourceBounds.Y-source.Bounds().Min.Y,
		sourceBounds.X-source.Bounds().Min.X+sourceBounds.Width,
		sourceBounds.Y-source.Bounds().Min.Y+sourceBounds.Height,
	)
	cropped := image.NewNRGBA(image.Rect(0, 0, localSubject.Dx(), localSubject.Dy()))
	imagedraw.Draw(cropped, cropped.Bounds(), local, localSubject.Min, imagedraw.Src)

	resizedWidth := scaledDimension(cropped.Bounds().Dx(), config.TargetHeight, cropped.Bounds().Dy())
	outputWidth := resizedWidth + config.PaddingX*2
	outputHeight := config.TargetHeight + config.PaddingY*2
	if !validImageDimensions(outputWidth, outputHeight) {
		return PetSpriteNormalizationResult{}, errors.New("normalized pet sprite output dimensions are invalid")
	}
	resized := image.NewNRGBA(image.Rect(0, 0, resizedWidth, config.TargetHeight))
	xdraw.ApproxBiLinear.Scale(resized, resized.Bounds(), cropped, cropped.Bounds(), xdraw.Src, nil)
	output := image.NewNRGBA(image.Rect(0, 0, outputWidth, outputHeight))
	outputX := config.PaddingX
	outputY := outputHeight - config.PaddingY - config.TargetHeight
	imagedraw.Draw(output, image.Rect(outputX, outputY, outputX+resizedWidth, outputY+config.TargetHeight), resized, image.Point{}, imagedraw.Src)

	pngBytes, err := encodePNG(output)
	if err != nil {
		return PetSpriteNormalizationResult{}, fmt.Errorf("encode normalized pet sprite: %w", err)
	}
	return PetSpriteNormalizationResult{
		PNG:            pngBytes,
		OriginalBounds: spriteBoundsFromRectangle(source.Bounds()),
		SourceBounds:   sourceBounds,
		SubjectBounds:  PetSpriteBounds{X: outputX, Y: outputY, Width: resizedWidth, Height: config.TargetHeight},
		OutputWidth:    outputWidth,
		OutputHeight:   outputHeight,
	}, nil
}

// NormalizePetSpriteToPNG 提供只需要编码结果的便捷入口。
func NormalizePetSpriteToPNG(source image.Image, targetHeight int) ([]byte, PetSpriteBounds, error) {
	result, err := NormalizePetSprite(source, PetSpriteNormalizationOptions{TargetHeight: targetHeight})
	if err != nil {
		return nil, PetSpriteBounds{}, err
	}
	return result.PNG, result.SubjectBounds, nil
}

func petMediaMinInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxIntValue(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func imageToNRGBA(source image.Image, bounds image.Rectangle) *image.NRGBA {
	output := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			pixel := color.NRGBAModel.Convert(source.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			output.SetNRGBA(x, y, pixel)
		}
	}
	return output
}

func scaledDimension(value, target, divisor int) int {
	if value <= 0 || target <= 0 || divisor <= 0 {
		return 1
	}
	scaled := (int64(value)*int64(target) + int64(divisor)/2) / int64(divisor)
	if scaled < 1 {
		return 1
	}
	return int(scaled)
}

func validImageDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= PetAtlasMaxTextureSize && height <= PetAtlasMaxTextureSize
}

func encodePNG(source image.Image) ([]byte, error) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, source); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

type PetAtlasBounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type PetAtlasFrame struct {
	X             int            `json:"x"`
	Y             int            `json:"y"`
	Width         int            `json:"width"`
	Height        int            `json:"height"`
	DurationMS    int            `json:"durationMs"`
	SubjectBounds PetAtlasBounds `json:"subjectBounds"`
}

type PetAtlasAnimation struct {
	Loop        bool            `json:"loop"`
	Label       string          `json:"label,omitempty"`
	Description string          `json:"description,omitempty"`
	Frames      []PetAtlasFrame `json:"frames"`
}

// PetAtlasBehavior 把运行时行为映射到一个或多个动作。行为属于 manifest，
// 这样不同皮肤可以在不改 Go 运行时的情况下调整 feed/drag 等动作组合。
type PetAtlasBehavior struct {
	Label   string   `json:"label,omitempty"`
	Actions []string `json:"actions"`
}

// PetAtlasManifestMetadata 是生成和读取 atlas 时共享的非几何元数据。
// 指针不需要出现在这里：零值表示“未设置”，而 builtin=false 的默认语义不影响运行时。
type PetAtlasManifestMetadata struct {
	Subject                    string `json:"subject,omitempty"`
	ModelID                    string `json:"modelId,omitempty"`
	CreatedAt                  int64  `json:"createdAt,omitempty"`
	UpdatedAt                  int64  `json:"updatedAt,omitempty"`
	Builtin                    bool   `json:"builtin,omitempty"`
	AssetVersion               int    `json:"assetVersion,omitempty"`
	SpriteNormalizationVersion int    `json:"spriteNormalizationVersion,omitempty"`
}

// PetAtlasManifest 是与源项目 action-rows manifest 对齐的内存表示。
type PetAtlasManifest struct {
	Name                       string                       `json:"name"`
	Subject                    string                       `json:"subject,omitempty"`
	ModelID                    string                       `json:"modelId,omitempty"`
	CreatedAt                  int64                        `json:"createdAt,omitempty"`
	UpdatedAt                  int64                        `json:"updatedAt,omitempty"`
	Builtin                    bool                         `json:"builtin,omitempty"`
	AssetVersion               int                          `json:"assetVersion,omitempty"`
	SpriteNormalizationVersion int                          `json:"spriteNormalizationVersion,omitempty"`
	AtlasVersion               int                          `json:"atlasVersion"`
	Atlas                      PetAtlasMetadata             `json:"atlas"`
	Animations                 map[string]PetAtlasAnimation `json:"animations"`
	Behaviors                  map[string]PetAtlasBehavior  `json:"behaviors,omitempty"`
}

type PetAtlasAction struct {
	ID          string
	Frames      []image.Image
	DurationsMS []int
	Loop        bool
	Label       string
	Description string
}

type PetAtlasPackRequest struct {
	Name      string
	Metadata  PetAtlasManifestMetadata
	Actions   []PetAtlasAction
	Behaviors map[string]PetAtlasBehavior
	MaxWidth  int
	MaxHeight int
	Padding   int
}

type PetAtlasPackOptions struct {
	Name      string
	MaxWidth  int
	MaxHeight int
	Padding   int
}

type PetAtlasPackResult struct {
	PNG      []byte
	Manifest PetAtlasManifest
	Atlas    PetAtlasMetadata
}

// PackPetAtlas 接受请求、动作切片或 action->images map，统一归一化后执行确定性布局。
// 允许 map 只是为了兼容已有调用方，真正的动作顺序仍由 canonicalActionOrder 决定。
func PackPetAtlas(input any, options ...PetAtlasPackOptions) (PetAtlasPackResult, error) {
	request, err := normalizePetAtlasRequest(input, options...)
	if err != nil {
		return PetAtlasPackResult{}, err
	}
	return packPetAtlasRequest(request)
}

func PackPetAtlasActions(actions []PetAtlasAction, options PetAtlasPackOptions) (PetAtlasPackResult, error) {
	return PackPetAtlas(actions, options)
}

func normalizePetAtlasRequest(input any, options ...PetAtlasPackOptions) (PetAtlasPackRequest, error) {
	if len(options) > 1 {
		return PetAtlasPackRequest{}, errors.New("pet atlas accepts at most one options value")
	}
	var request PetAtlasPackRequest
	switch value := input.(type) {
	case PetAtlasPackRequest:
		request = value
	case *PetAtlasPackRequest:
		if value == nil {
			return PetAtlasPackRequest{}, errors.New("pet atlas request is nil")
		}
		request = *value
	case []PetAtlasAction:
		request.Actions = value
	case map[string][]image.Image:
		request.Actions = make([]PetAtlasAction, 0, len(value))
		for actionID, frames := range value {
			request.Actions = append(request.Actions, PetAtlasAction{ID: actionID, Frames: frames, Loop: true})
		}
	case map[string]PetAtlasAction:
		request.Actions = make([]PetAtlasAction, 0, len(value))
		for actionID, action := range value {
			if strings.TrimSpace(action.ID) == "" {
				action.ID = actionID
			}
			request.Actions = append(request.Actions, action)
		}
	default:
		return PetAtlasPackRequest{}, errors.New("pet atlas input must be a request, action slice, or image map")
	}
	if len(options) == 1 {
		option := options[0]
		if option.Name != "" {
			request.Name = option.Name
		}
		if option.MaxWidth != 0 {
			request.MaxWidth = option.MaxWidth
		}
		if option.MaxHeight != 0 {
			request.MaxHeight = option.MaxHeight
		}
		if option.Padding != 0 {
			request.Padding = option.Padding
		}
	}
	return request, nil
}

type packedPetAtlasFrame struct {
	actionID   string
	index      int
	image      *image.NRGBA
	subject    PetAtlasBounds
	durationMS int
	x          int
	y          int
}

func packPetAtlasRequest(request PetAtlasPackRequest) (PetAtlasPackResult, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = "pet"
	}
	if len(name) > 128 {
		return PetAtlasPackResult{}, errors.New("pet atlas name is too long")
	}
	maxWidth, maxHeight := request.MaxWidth, request.MaxHeight
	if maxWidth == 0 {
		maxWidth = PetAtlasMaxTextureSize
	}
	if maxHeight == 0 {
		maxHeight = PetAtlasMaxTextureSize
	}
	if maxWidth < 1 || maxHeight < 1 || maxWidth > PetAtlasMaxTextureSize || maxHeight > PetAtlasMaxTextureSize {
		return PetAtlasPackResult{}, fmt.Errorf("pet atlas limits must be between 1 and %d", PetAtlasMaxTextureSize)
	}
	padding := request.Padding
	if request.Padding == 0 {
		padding = PetAtlasFramePadding
	}
	if padding < 0 || padding > PetAtlasMaxTextureSize/2 {
		return PetAtlasPackResult{}, errors.New("pet atlas padding is invalid")
	}
	if len(request.Actions) == 0 {
		return PetAtlasPackResult{}, errors.New("pet atlas requires at least one action")
	}

	actions, err := normalizePetAtlasActions(request.Actions)
	if err != nil {
		return PetAtlasPackResult{}, err
	}
	frames := make([]packedPetAtlasFrame, 0)
	atlasWidth, atlasHeight := 0, 0
	rowY := 0
	for _, action := range actions {
		prepared, contentHeight, err := preparePetAtlasFrames(action)
		if err != nil {
			return PetAtlasPackResult{}, err
		}
		if contentHeight > maxHeight-2*padding {
			return PetAtlasPackResult{}, fmt.Errorf("pet atlas action %q exceeds texture height", action.ID)
		}
		rowWidth := padding
		for _, frame := range prepared {
			if frame.image.Bounds().Dx() > maxWidth-2*padding || frame.image.Bounds().Dy() > maxHeight-2*padding {
				return PetAtlasPackResult{}, fmt.Errorf("pet atlas frame exceeds texture limit: %s[%d]", action.ID, frame.index)
			}
			if rowWidth > maxWidth-frame.image.Bounds().Dx()-2*padding {
				return PetAtlasPackResult{}, fmt.Errorf("pet atlas action row %q exceeds texture width", action.ID)
			}
			frame.x = rowWidth
			frame.y = rowY + padding + contentHeight - frame.image.Bounds().Dy()
			rowWidth += frame.image.Bounds().Dx() + 2*padding
			frames = append(frames, frame)
		}
		rowWidth -= padding
		atlasWidth = maxIntValue(atlasWidth, rowWidth)
		rowHeight := contentHeight + 2*padding
		if rowY > maxHeight-rowHeight {
			return PetAtlasPackResult{}, fmt.Errorf("pet atlas action rows exceed texture height at %q", action.ID)
		}
		rowY += rowHeight
		atlasHeight = rowY
	}
	if atlasWidth < 1 || atlasHeight < 1 || !validImageDimensions(atlasWidth, atlasHeight) {
		return PetAtlasPackResult{}, errors.New("pet atlas layout produced invalid dimensions")
	}

	atlasImage := image.NewNRGBA(image.Rect(0, 0, atlasWidth, atlasHeight))
	animations := make(map[string]PetAtlasAnimation, len(actions))
	for _, action := range actions {
		manifestFrames := make([]PetAtlasFrame, 0, len(action.Frames))
		for _, frame := range frames {
			if frame.actionID != action.ID {
				continue
			}
			frameBounds := image.Rect(frame.x, frame.y, frame.x+frame.image.Bounds().Dx(), frame.y+frame.image.Bounds().Dy())
			if !petAtlasBoundsWithin(frameBounds, atlasWidth, atlasHeight) {
				return PetAtlasPackResult{}, fmt.Errorf("pet atlas generated frame is outside atlas: %s[%d]", frame.actionID, frame.index)
			}
			imagedraw.Draw(atlasImage, frameBounds, frame.image, image.Point{}, imagedraw.Src)
			manifestFrames = append(manifestFrames, PetAtlasFrame{
				X: frame.x, Y: frame.y, Width: frame.image.Bounds().Dx(), Height: frame.image.Bounds().Dy(),
				DurationMS: frame.durationMS, SubjectBounds: frame.subject,
			})
		}
		animations[action.ID] = PetAtlasAnimation{
			Loop: action.Loop, Label: strings.TrimSpace(action.Label), Description: strings.TrimSpace(action.Description), Frames: manifestFrames,
		}
	}
	atlas := PetAtlasMetadata{
		AtlasVersion: PetAtlasVersion,
		Image:        "atlas.png",
		Width:        atlasWidth,
		Height:       atlasHeight,
		Anchor:       "bottom-center",
		Layout:       "action-rows",
	}
	metadata, err := normalizePetAtlasManifestMetadata(request.Metadata, name)
	if err != nil {
		return PetAtlasPackResult{}, err
	}
	behaviors, err := normalizePetAtlasBehaviors(request.Behaviors)
	if err != nil {
		return PetAtlasPackResult{}, err
	}
	manifest := PetAtlasManifest{
		Name:                       name,
		Subject:                    metadata.Subject,
		ModelID:                    metadata.ModelID,
		CreatedAt:                  metadata.CreatedAt,
		UpdatedAt:                  metadata.UpdatedAt,
		Builtin:                    metadata.Builtin,
		AssetVersion:               metadata.AssetVersion,
		SpriteNormalizationVersion: metadata.SpriteNormalizationVersion,
		AtlasVersion:               PetAtlasVersion,
		Atlas:                      atlas,
		Animations:                 animations,
		Behaviors:                  behaviors,
	}
	pngBytes, err := encodePNG(atlasImage)
	if err != nil {
		return PetAtlasPackResult{}, fmt.Errorf("encode pet atlas: %w", err)
	}
	return PetAtlasPackResult{PNG: pngBytes, Manifest: manifest, Atlas: atlas}, nil
}

func normalizePetAtlasManifestMetadata(metadata PetAtlasManifestMetadata, fallbackName string) (PetAtlasManifestMetadata, error) {
	metadata.Subject = strings.TrimSpace(metadata.Subject)
	metadata.ModelID = strings.TrimSpace(metadata.ModelID)
	if utf8.RuneCountInString(metadata.Subject) > petAtlasMaxSubjectLen {
		return PetAtlasManifestMetadata{}, fmt.Errorf("pet atlas subject is too long")
	}
	if utf8.RuneCountInString(metadata.ModelID) > petAtlasMaxModelIDLen {
		return PetAtlasManifestMetadata{}, fmt.Errorf("pet atlas modelId is too long")
	}
	if metadata.CreatedAt < 0 || metadata.UpdatedAt < 0 || metadata.AssetVersion < 0 || metadata.SpriteNormalizationVersion < 0 {
		return PetAtlasManifestMetadata{}, errors.New("pet atlas metadata contains a negative value")
	}
	if strings.TrimSpace(fallbackName) == "" {
		return PetAtlasManifestMetadata{}, errors.New("pet atlas metadata requires a name")
	}
	return metadata, nil
}

func normalizePetAtlasBehaviors(input map[string]PetAtlasBehavior) (map[string]PetAtlasBehavior, error) {
	if len(input) == 0 {
		return nil, nil
	}
	if len(input) > PetAtlasMaxBehaviors {
		return nil, fmt.Errorf("pet atlas behaviors contain too many entries")
	}
	result := make(map[string]PetAtlasBehavior, len(input))
	for id, behavior := range input {
		if !validPetActionID(id) {
			return nil, fmt.Errorf("invalid pet atlas behavior ID: %q", id)
		}
		if len(behavior.Label) > petAtlasMaxLabelLen {
			return nil, fmt.Errorf("pet atlas behavior %q label is too long", id)
		}
		if len(behavior.Actions) == 0 || len(behavior.Actions) > petAtlasMaxActionIDLen {
			return nil, fmt.Errorf("pet atlas behavior %q actions are invalid", id)
		}
		seen := make(map[string]struct{}, len(behavior.Actions))
		actions := make([]string, 0, len(behavior.Actions))
		for _, actionID := range behavior.Actions {
			actionID = strings.TrimSpace(actionID)
			if !validPetActionID(actionID) {
				return nil, fmt.Errorf("invalid action ID in pet atlas behavior %q", id)
			}
			if _, exists := seen[actionID]; exists {
				return nil, fmt.Errorf("pet atlas behavior %q contains duplicate action", id)
			}
			seen[actionID] = struct{}{}
			actions = append(actions, actionID)
		}
		result[id] = PetAtlasBehavior{Label: strings.TrimSpace(behavior.Label), Actions: actions}
	}
	return result, nil
}

func normalizePetAtlasActions(input []PetAtlasAction) ([]PetAtlasAction, error) {
	seen := make(map[string]struct{}, len(input))
	actions := make([]PetAtlasAction, 0, len(input))
	for _, action := range input {
		id := strings.TrimSpace(action.ID)
		if !validPetActionID(id) {
			return nil, fmt.Errorf("invalid pet atlas action ID: %q", action.ID)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("pet atlas contains duplicate action: %q", id)
		}
		seen[id] = struct{}{}
		action.ID = id
		if len(action.Frames) == 0 {
			return nil, fmt.Errorf("pet atlas action %q contains no frames", id)
		}
		if len(action.DurationsMS) != 0 && len(action.DurationsMS) != len(action.Frames) {
			return nil, fmt.Errorf("pet atlas action %q durations do not match frame count", id)
		}
		if len(action.Label) > petAtlasMaxLabelLen || len(action.Description) > petAtlasMaxDescription {
			return nil, fmt.Errorf("pet atlas action %q metadata is too long", id)
		}
		actions = append(actions, action)
	}
	if _, ok := seen["idle"]; !ok {
		return nil, errors.New("pet atlas requires an idle action")
	}
	sort.SliceStable(actions, func(left, right int) bool {
		leftIndex := canonicalActionIndex(actions[left].ID)
		rightIndex := canonicalActionIndex(actions[right].ID)
		if leftIndex != rightIndex {
			return leftIndex < rightIndex
		}
		return actions[left].ID < actions[right].ID
	})
	return actions, nil
}

func validPetActionID(id string) bool {
	if id == "" || len(id) > petAtlasMaxActionIDLen {
		return false
	}
	for index, character := range []byte(id) {
		if index == 0 {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')) {
				return false
			}
			continue
		}
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func canonicalActionIndex(actionID string) int {
	for index, builtin := range []string{"idle", "walk", "sleep", "beg", "eat", "munch", "bathe", "soak", "swim", "zen", "play", "held", "report-time"} {
		if actionID == builtin {
			return index
		}
	}
	return 1000
}

func preparePetAtlasFrames(action PetAtlasAction) ([]packedPetAtlasFrame, int, error) {
	prepared := make([]packedPetAtlasFrame, 0, len(action.Frames))
	contentHeight := 0
	for index, source := range action.Frames {
		if source == nil || source.Bounds().Empty() {
			return nil, 0, fmt.Errorf("pet atlas action %q frame %d is empty", action.ID, index)
		}
		local := imageToNRGBA(source, source.Bounds())
		subject, err := FindPetSpriteSubjectBounds(local)
		if err != nil {
			return nil, 0, fmt.Errorf("pet atlas action %q frame %d: %w", action.ID, index, err)
		}
		duration := PetAtlasDefaultDuration
		if len(action.DurationsMS) != 0 && action.DurationsMS[index] != 0 {
			duration = action.DurationsMS[index]
		}
		if duration < 16 || duration > 60_000 {
			return nil, 0, fmt.Errorf("pet atlas action %q frame %d has invalid duration", action.ID, index)
		}
		prepared = append(prepared, packedPetAtlasFrame{
			actionID: action.ID, index: index, image: local,
			subject:    PetAtlasBounds{X: subject.X, Y: subject.Y, Width: subject.Width, Height: subject.Height},
			durationMS: duration,
		})
		contentHeight = maxIntValue(contentHeight, local.Bounds().Dy())
	}
	return prepared, contentHeight, nil
}

func petAtlasBoundsWithin(bounds image.Rectangle, width, height int) bool {
	return bounds.Min.X >= 0 && bounds.Min.Y >= 0 && bounds.Max.X <= width && bounds.Max.Y <= height && !bounds.Empty()
}
