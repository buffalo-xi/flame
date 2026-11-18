<template>
  <div class="function-node-props">
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
            <span>函数参数</span>
            <button class="add-variable-icon-btn" ref="addParamBtnRef" @click="toggleParamDropdown">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="output-variables-cards">
            <div v-if="Object.keys(paramVariables).length === 0" class="output-empty-state">
              <p>暂无参数</p>
            </div>

            <div
              v-for="(config, key) in paramVariables"
              :key="key"
              class="output-variable-row param-variable-row"
            >
              <div class="output-variable-header">
                <span class="output-type-badge" :class="getOutputTypeClass(config.type)">
                  {{ getOutputTypeIcon(config.type) }}
                </span>
                <input
                  type="text"
                  :value="key"
                  :data-variable-key="`param-${key}`"
                  class="output-variable-name-input"
                  @change="handleRenameParamVariable(key, $event.target.value)"
                  placeholder="参数名"
                />
              </div>
              <div class="output-variable-content param-content">
                <input
                  type="text"
                  :value="config.description"
                  class="output-description-input"
                  placeholder="添加描述"
                  @change="handleUpdateParamVariable(key, 'description', $event.target.value)"
                />
                <div class="required-wrapper" title="是否必填">
                  <el-switch
                    :model-value="config.required"
                    size="small"
                    @change="(val) => handleUpdateParamVariable(key, 'required', val)"
                  />
                  <span class="required-label">必填</span>
                </div>
                <button class="output-delete-btn" @click="handleDeleteParamVariable(key)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="props-section">
          <div class="section-title">
            <span>局部变量</span>
            <button class="add-variable-icon-btn" ref="addLocalBtnRef" @click="toggleLocalDropdown">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="output-variables-cards">
            <div v-if="Object.keys(localVariables).length === 0" class="output-empty-state">
              <p>暂无局部变量</p>
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
                  placeholder="变量名"
                />
              </div>
              <div class="output-variable-content">
                <input
                  type="text"
                  :value="config.description"
                  class="output-description-input"
                  placeholder="添加描述"
                  @change="handleUpdateLocalVariable(key, 'description', $event.target.value)"
                />
                <button class="output-delete-btn" @click="handleDeleteLocalVariable(key)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <OutputVarsConfig
          :selected-node="selectedNode"
          :existing-names="existingOutputNames"
          @update-node-data="(data) => emit('update-node-data', data)"
        />
      </div>

      <div v-if="activeTab === 'lastRun'">
        <NodeLastRun :selected-node="selectedNode" />
      </div>
    </div>

    <Teleport to="body">
      <!-- 函数参数下拉菜单 -->
      <div v-if="showParamDropdown" class="fn-dropdown-overlay" @click="closeParamDropdown">
        <div class="fn-dropdown-menu fn-param-menu" :style="paramDropdownStyle" @click.stop>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('string')"><span class="dropdown-icon icon-string-inline">S</span>字符串</div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('number')"><span class="dropdown-icon icon-number-inline">N</span>数字</div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('boolean')"><span class="dropdown-icon icon-boolean-inline">B</span>布尔值</div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('object')"><span class="dropdown-icon icon-object-inline">O</span>对象</div>
          <div class="fn-dropdown-divider"></div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('array[string]')"><span class="dropdown-icon icon-string-array-inline">S</span>字符串数组</div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('array[number]')"><span class="dropdown-icon icon-number-array-inline">N</span>数字数组</div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('array[boolean]')"><span class="dropdown-icon icon-boolean-array-inline">B</span>布尔数组</div>
          <div class="fn-dropdown-divider"></div>
          <div class="fn-dropdown-item" @click="handleAddParamVariable('array[object]')"><span class="dropdown-icon icon-object-array-inline">O</span>对象数组</div>
        </div>
      </div>
      <!-- 局部变量下拉菜单 -->
      <div v-if="showLocalDropdown" class="fn-dropdown-overlay" @click="closeLocalDropdown">
        <div class="fn-dropdown-menu fn-local-menu" :style="localDropdownStyle" @click.stop>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('string')"><span class="dropdown-icon icon-string-inline">S</span>字符串</div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('number')"><span class="dropdown-icon icon-number-inline">N</span>数字</div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('boolean')"><span class="dropdown-icon icon-boolean-inline">B</span>布尔值</div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('object')"><span class="dropdown-icon icon-object-inline">O</span>对象</div>
          <div class="fn-dropdown-divider"></div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('array[string]')"><span class="dropdown-icon icon-string-array-inline">S</span>字符串数组</div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('array[number]')"><span class="dropdown-icon icon-number-array-inline">N</span>数字数组</div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('array[boolean]')"><span class="dropdown-icon icon-boolean-array-inline">B</span>布尔数组</div>
          <div class="fn-dropdown-divider"></div>
          <div class="fn-dropdown-item" @click="handleAddLocalVariable('array[object]')"><span class="dropdown-icon icon-object-array-inline">O</span>对象数组</div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, watch, computed, onMounted, onBeforeUnmount } from 'vue';
import NodeLastRun from './NodeLastRun.vue'
import { ElSwitch } from 'element-plus';
import { Plus, Delete } from '@element-plus/icons-vue';
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js';
import OutputVarsConfig from './OutputVarsConfig.vue';
import { useEscClose } from '../../../composables/useEscClose';

const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  }
});

const emit = defineEmits(['update-node-data', 'show-variable-select']);
const activeTab = ref('settings');
const paramVariables = ref({});
const localVariables = ref({});




watch(() => props.selectedNode, (node) => {
  if (node && node.data && node.data.config) {
    const config = node.data.config;
    // 兼容旧数据：params 中缺失 required 字段时补为 true
    const params = {};
    if (config.params) {
      Object.keys(config.params).forEach(key => {
        const v = config.params[key];
        params[key] = {
          ...v,
          required: v.required === undefined ? true : v.required
        };
      });
    }
    paramVariables.value = params;
    localVariables.value = config.locals ? { ...config.locals } : {};
  }
  if (node && node.data) {

  }
}, { immediate: true });

// 三组变量名互斥校验（param/local/output 三组两两互斥）
const isNameTaken = (name, excludeKey = '') => {
  if (name === excludeKey) return false;
  if (paramVariables.value[name]) return true;
  if (localVariables.value[name]) return true;
  if (props.selectedNode?.data?.config?.outputs?.[name] !== undefined) return true;
  return false;
};

const existingOutputNames = computed(() => {
  return [...Object.keys(paramVariables.value), ...Object.keys(localVariables.value)]
})

const getOutputTypeClass = getDataTypeIconClass
const getOutputTypeIcon = getDataTypeIcon

const stripTempField = (vars) => {
  const result = {};
  Object.keys(vars).forEach(key => {
    const { time, ...rest } = vars[key];
    result[key] = rest;
  });
  return result;
};

// ── 自定义下拉菜单（参数 + 局部变量） ──
const showParamDropdown = ref(false)
const addParamBtnRef = ref(null)
const paramDropdownPos = ref({ left: 0, top: 0 })
const paramDropdownStyle = computed(() => ({
  position: 'fixed',
  left: paramDropdownPos.value.left + 'px',
  top: paramDropdownPos.value.top + 'px',
  zIndex: 9999
}))

const showLocalDropdown = ref(false)
const addLocalBtnRef = ref(null)
const localDropdownPos = ref({ left: 0, top: 0 })
const localDropdownStyle = computed(() => ({
  position: 'fixed',
  left: localDropdownPos.value.left + 'px',
  top: localDropdownPos.value.top + 'px',
  zIndex: 9999
}))

let paramRafId = null
let localRafId = null

const calcDropdownPos = (btnRef, menuSelector) => {
  const btn = btnRef.value
  const propsPanel = document.querySelector('.props-panel')
  if (!btn || !propsPanel) return null
  const panelRect = propsPanel.getBoundingClientRect()
  const btnRect = btn.getBoundingClientRect()
  const menuEl = document.querySelector(menuSelector)
  const menuWidth = (menuEl && menuEl.offsetWidth > 0) ? menuEl.offsetWidth : 160
  return { left: panelRect.right - menuWidth, top: btnRect.bottom + 4 }
}

const updateParamDropdownPosition = () => {
  if (!showParamDropdown.value) return
  const pos = calcDropdownPos(addParamBtnRef, '.fn-param-menu')
  if (pos) paramDropdownPos.value = pos
  paramRafId = requestAnimationFrame(updateParamDropdownPosition)
}

const updateLocalDropdownPosition = () => {
  if (!showLocalDropdown.value) return
  const pos = calcDropdownPos(addLocalBtnRef, '.fn-local-menu')
  if (pos) localDropdownPos.value = pos
  localRafId = requestAnimationFrame(updateLocalDropdownPosition)
}

const toggleParamDropdown = () => {
  showParamDropdown.value = !showParamDropdown.value
  if (showParamDropdown.value) {
    const pos = calcDropdownPos(addParamBtnRef, null)
    if (pos) paramDropdownPos.value = pos
    paramRafId = requestAnimationFrame(updateParamDropdownPosition)
  } else {
    if (paramRafId) cancelAnimationFrame(paramRafId)
  }
}

const toggleLocalDropdown = () => {
  showLocalDropdown.value = !showLocalDropdown.value
  if (showLocalDropdown.value) {
    const pos = calcDropdownPos(addLocalBtnRef, null)
    if (pos) localDropdownPos.value = pos
    localRafId = requestAnimationFrame(updateLocalDropdownPosition)
  } else {
    if (localRafId) cancelAnimationFrame(localRafId)
  }
}

const closeParamDropdown = () => {
  showParamDropdown.value = false
  if (paramRafId) cancelAnimationFrame(paramRafId)
}

const closeLocalDropdown = () => {
  showLocalDropdown.value = false
  if (localRafId) cancelAnimationFrame(localRafId)
}

// 两个自定义下拉都是浮层：ESC 先收下拉，属性面板本身不会跟着关
useEscClose(showParamDropdown, closeParamDropdown)
useEscClose(showLocalDropdown, closeLocalDropdown)

onMounted(() => {
  window.addEventListener('resize', () => { closeParamDropdown(); closeLocalDropdown() })
})

onBeforeUnmount(() => {
  if (paramRafId) cancelAnimationFrame(paramRafId)
  if (localRafId) cancelAnimationFrame(localRafId)
  window.removeEventListener('resize', () => { closeParamDropdown(); closeLocalDropdown() })
})

// ============ 参数 params ============
const handleAddParamVariable = (type) => {
  closeParamDropdown()
  const sanitizedType = type.replace('[', '_').replace(']', '');
  let fieldName = `${sanitizedType}_1`;
  let counter = 1;
  while (isNameTaken(fieldName)) {
    fieldName = `${sanitizedType}_${counter}`;
    counter++;
  }
  const newVariables = { ...paramVariables.value };
  newVariables[fieldName] = {
    type,
    description: '',
    required: true,
    time: Date.now()
  };
  paramVariables.value = newVariables;

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.params = stripTempField(newVariables);

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

const handleRenameParamVariable = (oldKey, newKey) => {
  const reservedNames = ['index', 'item'];
  const trimmedKey = newKey ? newKey.trim() : '';

  if (!trimmedKey || reservedNames.includes(trimmedKey.toLowerCase())) {
    const inputElement = document.querySelector(`input[data-variable-key="param-${oldKey}"]`);
    if (inputElement) {
      inputElement.value = oldKey;
    }
    return;
  }

  if (trimmedKey === oldKey) return;
  if (isNameTaken(trimmedKey, oldKey)) {
    paramVariables.value = { ...paramVariables.value };
    return;
  }

  const newVariables = { ...paramVariables.value };
  newVariables[trimmedKey] = { ...newVariables[oldKey] };
  delete newVariables[oldKey];
  paramVariables.value = newVariables;

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.params = stripTempField(newVariables);

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

const handleUpdateParamVariable = (key, field, value) => {
  if (paramVariables.value[key]) {
    paramVariables.value[key][field] = value;

    const newConfig = { ...props.selectedNode.data.config };
    newConfig.params = stripTempField(paramVariables.value);

    emit('update-node-data', {
      nodeId: props.selectedNode.id,
      data: { config: newConfig }
    });
  }
};

const handleDeleteParamVariable = (key) => {
  delete paramVariables.value[key];

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.params = stripTempField(paramVariables.value);

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

// ============ 局部变量 locals ============
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

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.locals = stripTempField(newVariables);

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
  newConfig.locals = stripTempField(newVariables);

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

const handleUpdateLocalVariable = (key, field, value) => {
  if (localVariables.value[key]) {
    localVariables.value[key][field] = value;

    const newConfig = { ...props.selectedNode.data.config };
    newConfig.locals = stripTempField(localVariables.value);

    emit('update-node-data', {
      nodeId: props.selectedNode.id,
      data: { config: newConfig }
    });
  }
};

const handleDeleteLocalVariable = (key) => {
  delete localVariables.value[key];

  const newConfig = { ...props.selectedNode.data.config };
  newConfig.locals = stripTempField(localVariables.value);

  emit('update-node-data', {
    nodeId: props.selectedNode.id,
    data: { config: newConfig }
  });
};

</script>

<style scoped>
.function-node-props {
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

.param-content {
  flex: 2;
  display: flex;
  align-items: center;
  gap: 8px;
}

.required-wrapper {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}

.required-label {
  font-size: 11px;
  color: #999;
  white-space: nowrap;
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

/* 自定义下拉菜单（参数 + 局部变量） */
.fn-dropdown-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  z-index: 9998;
  background: transparent;
}
.fn-dropdown-menu {
  background: #fff;
  border-radius: 4px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  padding: 4px 0;
  min-width: 140px;
  font-size: 13px;
}
.fn-dropdown-item {
  padding: 6px 16px;
  cursor: pointer;
  color: #333;
  display: flex;
  align-items: center;
  white-space: nowrap;
  transition: background 0.15s;
}
.fn-dropdown-item:hover {
  background: #f5f7fa;
  color: #1890ff;
}
.fn-dropdown-divider {
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
