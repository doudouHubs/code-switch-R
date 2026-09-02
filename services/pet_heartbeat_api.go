package services

// PetHeartbeatAPI 是心跳 worker 的 Wails/浏览器薄适配层。配置、timer、AI 终态和
// 持久化都由 PetHeartbeatService 统一拥有，API 不复制任何状态，也不直接接触 SQL。
type PetHeartbeatAPI struct {
	service *PetHeartbeatService
}

func NewPetHeartbeatAPI(service *PetHeartbeatService) *PetHeartbeatAPI {
	return &PetHeartbeatAPI{service: service}
}

func (api *PetHeartbeatAPI) GetSnapshot() (PetHeartbeatSnapshot, error) {
	if api == nil || api.service == nil {
		return PetHeartbeatSnapshot{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务不可用", nil)
	}
	return api.service.GetSnapshot()
}

func (api *PetHeartbeatAPI) SaveConfig(config PetHeartbeatConfig) (PetHeartbeatSnapshot, error) {
	if api == nil || api.service == nil {
		return PetHeartbeatSnapshot{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务不可用", nil)
	}
	return api.service.SaveConfig(config)
}

func (api *PetHeartbeatAPI) RunNow() (PetHeartbeatSnapshot, error) {
	if api == nil || api.service == nil {
		return PetHeartbeatSnapshot{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务不可用", nil)
	}
	return api.service.RunNow()
}

func (api *PetHeartbeatAPI) Cancel() (PetHeartbeatSnapshot, error) {
	if api == nil || api.service == nil {
		return PetHeartbeatSnapshot{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务不可用", nil)
	}
	return api.service.Cancel()
}
