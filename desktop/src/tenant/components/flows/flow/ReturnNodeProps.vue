<template>
  <div class="control-node-props">
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
            <span>节点说明</span>
          </div>
          <div class="node-description">
            <p>返回节点用于退出当前函数。</p>
            <p>当工作流执行到该节点时，将退出当前函数，返回到调用处继续执行。</p>
            <p><strong>注意：</strong>该节点只能创建在函数节点内部。</p>
          </div>
        </div>
      </div>

      <div v-if="activeTab === 'lastRun'">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue';
import NodeLastRun from './NodeLastRun.vue'

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
});

const activeTab = ref('settings');
</script>

<style scoped>
.control-node-props {
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
  font-size: 13px;
  font-weight: 600;
  color: #333;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}

.node-description {
  font-size: 13px;
  color: #555;
  line-height: 1.8;
}

.node-description p {
  margin: 0 0 8px 0;
}

.node-description p:last-child {
  margin-bottom: 0;
}

.node-description strong {
  color: #e6a23c;
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
