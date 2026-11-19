<template>
  <div class="fork-node-props">
    <div class="tabs">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'settings' }"
        @click="activeTab = 'settings'"
      >
        {{ t('flow.nodeProps.tabConfig') }}
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'lastRun' }"
        @click="activeTab = 'lastRun'"
      >
        {{ t('flow.nodeProps.tabResult') }}
      </button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.node.concurrency.title') }}</span>
          </div>

          <div class="conn-control-row">
            <div class="conn-control-switch">
              <el-switch
                v-model="connEnabled"
                @change="handleConnEnabledChange"
              />
              <span class="conn-label">{{ t('flow.nodeProps.fork.maxConcurrency') }}</span>
            </div>
            <div class="conn-control-input" v-if="connEnabled">
              <el-input
                type="number"
                v-model="maxConcurrency"
                class="conn-input"
                @blur="handleMaxConcurrencyBlur"
                :min="1"
                :max="9999"
                :placeholder="t('flow.nodeProps.fork.positiveInt')"
              />
            </div>
            <div class="conn-control-hint" v-else>
              <span class="hint-text">{{ t('flow.nodeProps.fork.unlimited') }}</span>
            </div>
          </div>
        </div>

        <OnErrorConfig
          :selected-node="selectedNode"
          @update-node-data="(data) => emit('update-node-data', data)"
        />
      </div>

      <div v-if="activeTab === 'lastRun'">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch, computed } from 'vue';
import NodeLastRun from './NodeLastRun.vue'
import { useI18n } from 'vue-i18n'
import { ElSwitch, ElInput } from 'element-plus';
import OnErrorConfig from './OnErrorConfig.vue';

const { t } = useI18n()

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
});

const emit = defineEmits(['update-node-data']);
const activeTab = ref('settings');
const connEnabled = ref(false);
const maxConcurrency = ref(1);




watch(() => props.selectedNode, (node) => {
  if (node && node.data && node.data.config) {
    const config = node.data.config;
    const connValue = config.max_concurrency;
    if (connValue !== undefined && connValue > 0) {
      connEnabled.value = true;
      maxConcurrency.value = connValue;
    } else {
      connEnabled.value = false;
      maxConcurrency.value = 1;
    }
  }
  if (node && node.data) {

  }
}, { immediate: true });

const saveMaxConcurrency = () => {
  const value = connEnabled.value ? Math.max(1, Math.floor(maxConcurrency.value)) : -1;
  const newConfig = { ...props.selectedNode.data.config };
  newConfig.max_concurrency = value;

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

const handleConnEnabledChange = () => {
  if (connEnabled.value && maxConcurrency.value < 1) {
    maxConcurrency.value = 1;
  }
  saveMaxConcurrency();
};

const handleMaxConcurrencyBlur = () => {
  let value = Math.floor(maxConcurrency.value);
  if (value < 1) {
    value = 1;
    maxConcurrency.value = 1;
  }
  saveMaxConcurrency();
};
</script>

<style scoped>
.fork-node-props {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}

.tabs {
  display: flex;
  border-bottom: 1px solid #eee;
  flex-shrink: 0;
}

.tab-btn {
  padding: 10px;
  border: none;
  background: transparent;
  font-size: 13px;
  color: #666;
  cursor: pointer;
  position: relative;
  transition: color 0.2s;
  margin-right: 20px;
}

.tab-btn:hover {
  color: #1890ff;
}

.tab-btn.active {
  color: #1890ff;
  font-weight: 500;
}

.tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 1px;
  background: #1890ff;
}

.tab-content {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
}

.settings-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.props-section {
  background: #fafafa;
  border-radius: 8px;
  padding: 12px;
}

.section-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
  font-weight: 600;
  color: #333;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}

.conn-control-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.conn-control-switch {
  display: flex;
  align-items: center;
  gap: 8px;
}

.conn-label {
  font-size: 13px;
  color: #333;
}

.conn-control-input {
  flex: 1;
  max-width: 120px;
}

.conn-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.conn-input :deep(.el-input__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.conn-input :deep(.el-input__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.conn-control-hint {
  flex: 1;
}

.hint-text {
  font-size: 12px;
  color: #999;
}

.last-run-panel {
  padding: 0;
}

.result-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.result-label {
  font-size: 12px;
  color: #666;
}

.result-value {
  font-size: 13px;
  color: #333;
}

.status-success {
  color: #67c23a;
}

.status-error {
  color: #f56565;
}

.status-running {
  color: #409eff;
}

.status-default {
  color: #999;
}

.result-output {
  width: 100%;
  min-height: 100px;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #333;
  background: #fafafa;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
