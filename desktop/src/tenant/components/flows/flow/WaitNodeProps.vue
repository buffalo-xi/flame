<template>
  <div class="wait-node-props">
    <div class="tabs">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'settings' }"
        @click="activeTab = 'settings'"
      >
        配置
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'lastRun' }"
        @click="activeTab = 'lastRun'"
      >
        执行结果
      </button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <div class="props-section">
          <div class="section-title">
            <span>并发组同步</span>
          </div>

          <div class="fork-select-row">
            <span class="fork-label">选择并发组:</span>
            <el-select
              v-model="selectedForkID"
              class="fork-select"
              placeholder="请选择"
              @change="handleForkChange"
            >
              <el-option
                v-for="node in forkNodes"
                :key="node.id"
                :label="node.data?.label || node.id"
                :value="node.id"
              />
            </el-select>
            <button
              v-if="selectedForkID"
              class="clear-btn"
              title="清除选择"
              @click="clearForkSelection"
            >
              ✕
            </button>
          </div>
          <div class="fork-hint">
            <span class="hint-text">选择要等待的 并发组 节点</span>
          </div>
        </div>

        <div class="props-section">
          <div class="section-title">
            <span>超时控制</span>
          </div>

          <div class="timeout-row">
            <el-switch 
              v-model="timeoutEnabled" 
              @change="handleTimeoutEnabledChange"
            />
            <el-input 
              v-if="timeoutEnabled"
              type="number" 
              v-model="timeout" 
              class="timeout-input"
              @blur="handleTimeoutChange"
              :min="1"
              :max="3600"
              placeholder="30"
            />
            <span v-if="timeoutEnabled" class="unit-text">秒</span>
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
import { ref, watch, computed, inject } from 'vue';
import NodeLastRun from './NodeLastRun.vue'
import OnErrorConfig from './OnErrorConfig.vue';

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
});

const emit = defineEmits(['update-node-data']);
const FlowNodes = inject('FlowNodes', ref([]));
const FlowEdges = inject('FlowEdges', ref([]));
const activeTab = ref('settings');
const selectedForkID = ref('');
const timeout = ref(30);
const timeoutEnabled = ref(false);




// 沿入边向上查找 fork/wait 节点，无入边时跳到父节点继续
const forkNodes = computed(() => {
  if (!props.selectedNode) return [];

  const nodes = FlowNodes.value;
  const edges = FlowEdges.value;
  const currentId = props.selectedNode.id;
  const nodeMap = new Map(nodes.map(n => [n.id, n]));

  const foundForks = [];
  const foundWaits = [];
  const visited = new Set();
  visited.add(currentId); // 排除自身

  const queue = [currentId];

  while (queue.length > 0) {
    const nodeId = queue.shift();

    // 查找所有入边
    const incomingEdges = edges.filter(e => String(e.target) === String(nodeId));

    if (incomingEdges.length === 0) {
      // 无入边，尝试跳到父节点
      const node = nodeMap.get(nodeId);
      if (node?.parentNode && !visited.has(node.parentNode)) {
        visited.add(node.parentNode);
        const parentNode = nodeMap.get(node.parentNode);
        if (parentNode) {
          queue.push(parentNode.id);
        }
      }
      continue;
    }

    for (const edge of incomingEdges) {
      const sourceId = edge.source;
      if (visited.has(sourceId)) continue;
      visited.add(sourceId);

      const sourceNode = nodeMap.get(sourceId);
      if (!sourceNode) continue;

      if (sourceNode.type === 'fork') {
        foundForks.push(sourceNode);
      } else if (sourceNode.type === 'wait') {
        foundWaits.push(sourceNode);
      }
      queue.push(sourceId);
    }
  }

  // 收集已被查找到的其他 wait 节点占用的 forkID（排除当前节点自身）
  const usedForkIDs = new Set();
  for (const n of foundWaits) {
    const forkID = n.data?.config?.fork_node_id;
    if (forkID) usedForkIDs.add(String(forkID));
  }

  return foundForks.filter(n => !usedForkIDs.has(String(n.id)));
});

watch(() => props.selectedNode, (node) => {
  if (node && node.data && node.data.config) {
    selectedForkID.value = node.data.config.fork_node_id || '';
    timeout.value = node.data.config.timeout?.seconds || 30;
    timeoutEnabled.value = node.data.config.timeout?.enabled ?? false;
  } else {
    selectedForkID.value = '';
  }
  if (node && node.data) {

  }
}, { immediate: true });

const buildConfig = () => {
  return {
    ...props.selectedNode.data.config,
    fork_node_id: selectedForkID.value,
    timeout: {
      enabled: timeoutEnabled.value,
      seconds: Math.floor(timeout.value)
    }
  };
};

const handleForkChange = (value) => {
  selectedForkID.value = value || '';
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: buildConfig() }
  });
};

const handleTimeoutChange = () => {
  let validTimeout = Math.floor(timeout.value);
  if (validTimeout < 1) {
    validTimeout = 1;
    timeout.value = validTimeout;
  }
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: buildConfig() }
  });
};

const handleTimeoutEnabledChange = () => {
  let validTimeout = Math.floor(timeout.value);
  if (validTimeout < 1) {
    validTimeout = 1;
    timeout.value = validTimeout;
  }
  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: buildConfig() }
  });
};

const clearForkSelection = () => {
  selectedForkID.value = '';
  handleForkChange('');
};
</script>

<style scoped>
.wait-node-props {
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

.fork-select-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}

.fork-label {
  font-size: 13px;
  color: #333;
  white-space: nowrap;
}

.fork-select {
  flex: 1;
}

.fork-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.fork-select :deep(.el-select__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.fork-select :deep(.el-select__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.clear-btn {
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border: 1px solid #ddd;
  border-radius: 4px;
  background: #fff;
  color: #999;
  font-size: 12px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.2s;
}

.clear-btn:hover {
  border-color: #f56565;
  color: #f56565;
  background: #fff5f5;
}

.fork-hint {
  padding-left: 4px;
}

.hint-text {
  font-size: 11px;
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

.timeout-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.timeout-input {
  width: 80px;
  flex-shrink: 0;
}

.timeout-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.timeout-input :deep(.el-input__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.timeout-input :deep(.el-input__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.unit-text {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
}
</style>
