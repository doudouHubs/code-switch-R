package services

import "context"

// PetImageAPIService 是 Wails 与 PetImageService 之间的薄 bridge。
// provider reader、HTTP transport、凭据处理和图片校验全部由核心服务拥有，
// bridge 不复制任何 provider 配置，也不把 context/HTTP 细节暴露给前端。
type PetImageAPIService struct {
	service *PetImageService
}

func NewPetImageAPIService(service *PetImageService) *PetImageAPIService {
	return &PetImageAPIService{service: service}
}

// GenerateImage 使用 Wails 可调用的无 context 签名；核心服务仍通过
// context.Background() 和内部超时边界保证请求可收敛。参考图优先接受
// referenceImage.data 中的已裁 idle 单帧 bare base64/data URL，也兼容受控的
// idle 单帧本地路径；核心服务会解码、校验后决定 generations/edits 路径。
// []byte 会由 Wails JSON 编码为 base64，前端拿到的是本地图片 bytes，而不是远程 URL。
func (api *PetImageAPIService) GenerateImage(request PetImageRequest) (PetImageResult, error) {
	service, err := api.getService()
	if err != nil {
		return PetImageResult{}, err
	}
	return service.GenerateImage(context.Background(), request)
}

func (api *PetImageAPIService) getService() (*PetImageService, error) {
	if api == nil || api.service == nil {
		return nil, newPetAIError(PET_IMAGE_DEPENDENCY_UNAVAILABLE, 0, nil)
	}
	return api.service, nil
}
