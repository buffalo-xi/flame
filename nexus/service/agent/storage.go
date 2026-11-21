package agent

import (
	"flame/nexus/shared/config"
	"flame/nexus/shared/storage"
)

// agentStorage is the storage instance held by the agent module (local/nfs/s3).
var agentStorage storage.Storage

// InitStorage initialises the agent storage; it must be called after the config is loaded.
func InitStorage() error {
	st, err := storage.New(config.Cfg.Storage)
	if err != nil {
		return err
	}
	agentStorage = st
	return nil
}
