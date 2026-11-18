package agent

import (
	"flame/nexus/shared/config"
	"flame/nexus/shared/storage"
)

// agentStorage agent 模块持有的存储实例（local/nfs/s3）。
var agentStorage storage.Storage

// InitStorage 初始化 agent 存储，需在 config 加载之后调用。
func InitStorage() error {
	st, err := storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	if err != nil {
		return err
	}
	agentStorage = st
	return nil
}
