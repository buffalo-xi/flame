<template>
  <div class="process-node-props">
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
        <OutputVarsConfig
          :selected-node="selectedNode"
          :existing-names="existingOutputNames"
          :reserved-names="['index', 'item']"
          @update-node-data="(data) => emit('update-node-data', data)"
        />

        <div class="props-section">
          <div class="section-title">
            <span>{{ t('flow.common.localVars') }}</span>
            <button class="add-variable-icon-btn" ref="addLocalBtnRef" @click="toggleLocalDropdown">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="output-variables-cards">
            <div v-if="Object.keys(localVariables).length === 0" class="output-empty-state">
              <p>{{ t('flow.common.noLocalVars') }}</p>
            </div>

            <div
              v-for="(config, key) in localVariables"
              :key="key"
              class="output-variable-row"
            >
              <div class="output-variable-header">
                <span class="output-type-badge" :class="getOutputTypeClass(config.type)">
                  {{ getOutputTypeIcon(config.type) }}
                </span>
                <input
                  type="text"
                  :value="key"
                  :data-variable-key="`local-${key}`"
                  class="output-variable-name-input"
                  @change="handleRenameLocalVariable(key, $event.target.value)"
                  :placeholder="t('flow.common.varName')"
                />
              </div>
              <div class="output-variable-content">
                <input
                  type="text"
                  :value="config.description"
                  class="output-description-input"
                  :placeholder="t('flow.common.addDescription')"
                  @change="handleUpdateLocalVariable(key, 'description', $event.target.value)"
                />
                <button class="output-delete-btn" @click="handleDeleteLocalVariable(key)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
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

    <Teleport to="body">
      <div v-if="showLocalDropdown" class="local-dropdown-overlay" @click="closeLocalDropdown">
        <div class="local-dropdown-menu" :style="localDropdownStyle" @click.stop>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('string')"><span class="dropdown-icon icon-string-inline">S</span>{{ t('flow.typeOptions.string') }}</div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('number')"><span class="dropdown-icon icon-number-inline">N</span>{{ t('flow.typeOptions.number') }}</div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('boolean')"><span class="dropdown-icon icon-boolean-inline">B</span>{{ t('flow.typeOptions.boolean') }}</div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('object')"><span class="dropdown-icon icon-object-inline">O</span>{{ t('flow.typeOptions.object') }}</div>
          <div class="local-dropdown-divider"></div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('array[string]')"><span class="dropdown-icon icon-string-array-inline">S</span>{{ t('flow.typeOptions.arrayString') }}</div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('array[number]')"><span class="dropdown-icon icon-number-array-inline">N</span>{{ t('flow.typeOptions.arrayNumber') }}</div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('array[boolean]')"><span class="dropdown-icon icon-boolean-array-inline">B</span>{{ t('flow.typeOptions.arrayBoolean') }}</div>
          <div class="local-dropdown-divider"></div>
          <div class="local-dropdown-item" @click="handleAddLocalVariable('array[object]')"><span class="dropdown-icon icon-object-array-inline">O</span>{{ t('flow.typeOptions.arrayObject') }}</div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, watch, computed, onMounted, onBeforeUnmount } from 'vue';
import NodeLastRun from './NodeLastRun.vue'
import { useI18n } from 'vue-i18n'
import { Plus, Delete } from '@element-plus/icons-vue';
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js';
import OnErrorConfig from './OnErrorConfig.vue';
import OutputVarsConfig from './OutputVarsConfig.vue';
import { useEscClose } from '../../../composables/useEscClose';

const { t } = useI18n()

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  },
  flowFlowVariables: {
    type: Object,
    default: () => ({})
  },
  flowEnvironmentVariables: {
    type: Object,
    default: () => ({})
  },
  flowArguments: {
    type: Object,
    default: () => ({})
  }
});

const emit = defineEmits(['update-node-data', 'show-variable-select']);
const activeTab = ref('settings');
const localVariables = ref({});

// ── 自定义局部变量下拉菜单 ──
const showLocalDropdown = ref(false)
const addLocalBtnRef = ref(null)
const localDropdownPos = ref({ left: 0, top: 0 })

const localDropdownStyle = computed(() => ({
  position: 'fixed',
  left: localDropdownPos.value.left + 'px',
  top: localDropdownPos.value.top + 'px',
  zIndex: 9999
}))

let localDropdownRafId = null

const updateLocalDropdownPosition = () => {
  if (!showLocalDropdown.value) return
  const btn = addLocalBtnRef.value
  const propsPanel = document.querySelector('.props-panel')
  if (!btn || !propsPanel) return
  const panelRect = propsPanel.getBoundingClientRect()
  const btnRect = btn.getBoundingClientRect()
  const menuEl = document.querySelector('.local-dropdown-menu')
  const menuWidth = (menuEl && menuEl.offsetWidth > 0) ? menuEl.offsetWidth : 160
  localDropdownPos.value = {
    left: panelRect.right - menuWidth,
    top: btnRect.bottom + 4
  }
  localDropdownRafId = requestAnimationFrame(updateLocalDropdownPosition)
}

const toggleLocalDropdown = () => {
  showLocalDropdown.value = !showLocalDropdown.value
  if (showLocalDropdown.value) {
    const btn = addLocalBtnRef.value
    const propsPanel = document.querySelector('.props-panel')
    if (btn && propsPanel) {
      const btnRect = btn.getBoundingClientRect()
      const panelRect = propsPanel.getBoundingClientRect()
      localDropdownPos.value = { left: panelRect.right - 160, top: btnRect.bottom + 4 }
    }
    localDropdownRafId = requestAnimationFrame(updateLocalDropdownPosition)
  } else {
    if (localDropdownRafId) cancelAnimationFrame(localDropdownRafId)
  }
}

const closeLocalDropdown = () => {
  showLocalDropdown.value = false
  if (localDropdownRafId) cancelAnimationFrame(localDropdownRafId)
}

// 自定义下拉浮层：ESC 收下拉，属性面板本身不会跟着关
useEscClose(showLocalDropdown, closeLocalDropdown)

onMounted(() => {
  window.addEventListener('resize', closeLocalDropdown)
})

onBeforeUnmount(() => {
  if (localDropdownRafId) cancelAnimationFrame(localDropdownRafId)
  window.removeEventListener('resize', closeLocalDropdown)
})





watch(() => props.selectedNode, (node) => {
  if (node && node.data && node.data.config) {
    const config = node.data.config;
    localVariables.value = config.locals ? { ...config.locals } : {};
  }
  if (node && node.data) {

  }
}, { immediate: true });

const isNameTaken = (name, excludeKey = '') => {
  if (name === excludeKey) return false;
  if (localVariables.value[name]) return true;
  if (props.selectedNode?.data?.config?.outputs?.[name] !== undefined) return true;
  return false;
};

const existingOutputNames = computed(() => {
  return Object.keys(localVariables.value)
})

const getOutputTypeClass = getDataTypeIconClass
const getOutputTypeIcon = getDataTypeIcon

const handleAddLocalVariable = (type) => {
  closeLocalDropdown()
  const sanitizedType = type.replace('[', '_').replace(']', '');
  let fieldName = `${sanitizedType}_1`;
  let counter = 1;
  while (isNameTaken(fieldName)) {
    fieldName = `${sanitizedType}_${counter}`;
    counter++;
  }
  const newVariables = { ...localVariables.value };
  newVariables[fieldName] = {
    type,
    description: '',
    time: Date.now()
  };
  localVariables.value = newVariables;
  const localsForSave = {};
  Object.keys(newVariables).forEach(key => {
    const { time, ...rest } = newVariables[key];
    localsForSave[key] = rest;
  });

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.locals = localsForSave;

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

const handleRenameLocalVariable = (oldKey, newKey) => {
  const reservedNames = ['index', 'item'];
  const trimmedKey = newKey ? newKey.trim() : '';

  if (!trimmedKey || reservedNames.includes(trimmedKey.toLowerCase())) {
    const inputElement = document.querySelector(`input[data-variable-key="local-${oldKey}"]`);
    if (inputElement) {
      inputElement.value = oldKey;
    }
    return;
  }

  if (trimmedKey === oldKey) return;
  if (isNameTaken(trimmedKey, oldKey)) {
    localVariables.value = { ...localVariables.value };
    return;
  }
  const newVariables = { ...localVariables.value };
  newVariables[trimmedKey] = { ...newVariables[oldKey] };
  delete newVariables[oldKey];
  localVariables.value = newVariables;

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.locals = localVariables.value;

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

const handleUpdateLocalVariable = (key, field, value) => {
  if (localVariables.value[key]) {
    localVariables.value[key][field] = value;

    const newConfig = { ...props.selectedNode.data.config };
    newConfig.locals = localVariables.value;

    emit('update-node-data', {
      nodeId: props.selectedNode.id,
      data: { config: newConfig }
    });
  }
};

const handleDeleteLocalVariable = (key) => {
  delete localVariables.value[key];

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.locals = localVariables.value;

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

</script>

<style scoped>
.process-node-props {
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
  padding: 5px;
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

.add-variable-icon-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 4px;
  background: #1890ff;
  color: white;
  cursor: pointer;
  font-size: 14px;
  transition: background-color 0.2s;
}

.add-variable-icon-btn:hover {
  background: #40a9ff;
}

.output-variables-cards {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.output-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 8px;
  text-align: center;
}

.output-empty-state p {
  color: #999;
  margin: 0;
  font-size: 12px;
}

.output-variable-row {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 0px solid #e8e8e8;
  border-radius: 6px;
  padding: 0px 0px;
}

.output-variable-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  max-width: 33.33%;
}

.output-variable-content {
  flex: 2;
  display: flex;
  align-items: center;
  gap: 8px;
}

.output-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 12px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
}

.output-type-badge.type-string {
  background: #67c23a;
  border-radius: 50%;
}

.output-type-badge.type-number {
  background: #409eff;
  border-radius: 50%;
}

.output-type-badge.type-boolean {
  background: #9c27b0;
  border-radius: 50%;
}

.output-type-badge.type-object {
  background: #909399;
  border-radius: 50%;
}

.output-type-badge.type-array-string {
  background: #f56c6c;
  border-radius: 4px;
}

.output-type-badge.type-array-number {
  background: #e6a23c;
  border-radius: 4px;
}

.output-type-badge.type-array-boolean {
  background: #9c27b0;
  border-radius: 4px;
}

.output-type-badge.type-array-object {
  background: #909399;
  border-radius: 4px;
}

.output-variable-name-input {
  flex: 1;
  min-width: 0;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
  transition: border-color 0.2s;
}

.output-variable-name-input:focus {
  outline: none;
}

.output-variable-name-input::placeholder {
  color: #bbb;
}

.output-description-input {
  flex: 1;
  min-width: 0;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #666;
  background-color: #eeeeee;
  outline: none;
  transition: border-color 0.2s;
}

.output-description-input:focus {
  outline: none;
}

.output-description-input::placeholder {
  color: #bbb;
}

.output-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: 3px;
  font-size: 12px;
  color: #999;
  background: transparent;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.output-delete-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.dropdown-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  margin-right: 6px;
  flex-shrink: 0;
}

.dropdown-icon.icon-string-inline {
  background: #67c23a;
  border-radius: 50%;
}

.dropdown-icon.icon-number-inline {
  background: #409eff;
  border-radius: 50%;
}

.dropdown-icon.icon-boolean-inline {
  background: #9c27b0;
  border-radius: 50%;
}

.dropdown-icon.icon-object-inline {
  background: #909399;
  border-radius: 50%;
}

.dropdown-icon.icon-string-array-inline {
  background: #f56c6c;
  border-radius: 4px;
}

.dropdown-icon.icon-number-array-inline {
  background: #e6a23c;
  border-radius: 4px;
}

.dropdown-icon.icon-boolean-array-inline {
  background: #9c27b0;
  border-radius: 4px;
}

.dropdown-icon.icon-object-array-inline {
  background: #909399;
  border-radius: 4px;
}

/* 自定义局部变量下拉菜单 */
.local-dropdown-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  z-index: 9998;
  background: transparent;
}
.local-dropdown-menu {
  background: #fff;
  border-radius: 4px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  padding: 4px 0;
  min-width: 140px;
  font-size: 13px;
}
.local-dropdown-item {
  padding: 6px 16px;
  cursor: pointer;
  color: #333;
  display: flex;
  align-items: center;
  white-space: nowrap;
  transition: background 0.15s;
}
.local-dropdown-item:hover {
  background: #f5f7fa;
  color: #1890ff;
}
.local-dropdown-divider {
  height: 1px;
  background: #ebeef5;
  margin: 4px 0;
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

<style>
/* 下拉菜单右侧与窗口对齐 */
.dropdown-align-right {
  margin-right: 8px !important;
}
</style>
