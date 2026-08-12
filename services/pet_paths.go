package services

import "path/filepath"

// OpenCowork 的宠物资源属于跨应用可迁移的数据，不跟随 CodeSwitch 的通用配置目录。
// 这样迁移器、Studio 和梦境图片服务使用同一事实目录，旧皮肤及旧梦境不会因为宿主
// 应用名称不同而变成“数据库里有记录、文件却找不到”的半失效状态。
const petDataDirectoryName = ".open-cowork"

func defaultPetDataRoot(home string) string {
	return filepath.Join(home, petDataDirectoryName)
}

func defaultPetSkinRoot(home string) string {
	return filepath.Join(defaultPetDataRoot(home), petMigrationPetsDir)
}

func defaultPetDreamArchiveRoot(home string) string {
	return filepath.Join(defaultPetDataRoot(home), petMigrationDreamsDir)
}
