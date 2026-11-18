<template>
  <div class="start-node-props">
    <!-- 标签页导航 -->
    <div class="tabs">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'settings' }"
        @click="activeTab = 'settings'"
      >
        设置
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'lastRun' }"
        @click="activeTab = 'lastRun'"
      >
        执行结果
      </button>
    </div>

    
    <!-- 标签页内容 -->
    <div class="tab-content">
      <!-- 设置页面 - 树形结构 -->
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <!-- 入口参数 -->
        <div class="props-section">
          <div class="section-title">
            <span>入口参数</span>
            <el-dropdown @command="handleAddField" trigger="click" placement="bottom-end" popper-class="dropdown-align-right">
              <button class="add-variable-icon-btn">
                <el-icon><Plus /></el-icon>
              </button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="number">
                    <span class="dropdown-icon icon-number-inline">N</span>数字
                  </el-dropdown-item>
                  <el-dropdown-item command="string">
                    <span class="dropdown-icon icon-string-inline">S</span>字符串
                  </el-dropdown-item>
                  <el-dropdown-item command="boolean">
                    <span class="dropdown-icon icon-boolean-inline">B</span>布尔值
                  </el-dropdown-item>
                  <el-dropdown-item command="object">
                    <span class="dropdown-icon icon-object-inline">O</span>对象
                  </el-dropdown-item>
                  <el-dropdown-item command="numberArray">
                    <span class="dropdown-icon icon-number-array-inline">N</span>数字数组
                  </el-dropdown-item>
                  <el-dropdown-item command="stringArray">
                    <span class="dropdown-icon icon-string-array-inline">S</span>字符串数组
                  </el-dropdown-item>
                  <el-dropdown-item command="booleanArray">
                    <span class="dropdown-icon icon-boolean-array-inline">B</span>布尔数组
                  </el-dropdown-item>
                  <el-dropdown-item command="objectArray">
                    <span class="dropdown-icon icon-object-array-inline">O</span>对象数组
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>
        <div class="tree-container">
          <template v-if="treeData.length > 0">
            <StartNodePropsTreeItem
        v-for="item in treeData"
        :key="item.key"
        :item="item"
        :level="0"
        :required-keys-map="requiredKeysMap"
        @toggle-expand="toggleExpand"
        @add-child="handleAddChild"
        @toggle-required="handleToggleRequired"
        @delete-node="handleDeleteNode"
        @rename-node="handleRenameNode"
        @update-description="handleUpdateDescription"
      />
          </template>
          <div v-else class="empty-tree">
            <p>暂无入口参数</p>
          </div>
        </div>
      </div>

      <!-- 流程配置 -->
        <div class="props-section">
          <div class="section-title">
            <span>流程配置</span>
          </div>
          <div class="config-item">
            <div class="config-item-header">
              <label class="config-label">超时时间（秒）<span class="limit-hint">/ {{ limitHintText('timeout') }}</span></label>
              <div class="config-controls">
                <el-input
                  v-if="isLimitEnabled('timeout')"
                  :model-value="String(flowLimits.timeout)"
                  class="config-input"
                  type="number"
                  :max="systemLimits.max_timeout"
                  @update:model-value="(val) => handleUpdateLimit('timeout', val)"
                  @blur="(e) => clampOnBlur('timeout', e)"
                />
                <el-switch :model-value="isLimitEnabled('timeout')" @update:model-value="(v) => toggleLimit('timeout', v)" />
              </div>
            </div>
          </div>
          <div class="config-item">
            <div class="config-item-header">
              <label class="config-label">最大递归深度<span class="limit-hint">/ {{ limitHintText('max_recursion_depth') }}</span></label>
              <div class="config-controls">
                <el-input
                  v-if="isLimitEnabled('max_recursion_depth')"
                  :model-value="String(flowLimits.max_recursion_depth)"
                  class="config-input"
                  type="number"
                  :max="systemLimits.max_recursion_depth"
                  @update:model-value="(val) => handleUpdateLimit('max_recursion_depth', val)"
                  @blur="(e) => clampOnBlur('max_recursion_depth', e)"
                />
                <el-switch :model-value="isLimitEnabled('max_recursion_depth')" @update:model-value="(v) => toggleLimit('max_recursion_depth', v)" />
              </div>
            </div>
          </div>
          <div class="config-item">
            <div class="config-item-header">
              <label class="config-label">最大节点数<span class="limit-hint">/ {{ limitHintText('max_node_count') }}</span></label>
              <div class="config-controls">
                <el-input
                  v-if="isLimitEnabled('max_node_count')"
                  :model-value="String(flowLimits.max_node_count)"
                  class="config-input"
                  type="number"
                  :max="systemLimits.max_node_count"
                  @update:model-value="(val) => handleUpdateLimit('max_node_count', val)"
                  @blur="(e) => clampOnBlur('max_node_count', e)"
                />
                <el-switch :model-value="isLimitEnabled('max_node_count')" @update:model-value="(v) => toggleLimit('max_node_count', v)" />
              </div>
            </div>
          </div>
          <div class="config-item">
            <div class="config-item-header">
              <label class="config-label">最大执行次数<span class="limit-hint">/ {{ limitHintText('max_exec_count') }}</span></label>
              <div class="config-controls">
                <el-input
                  v-if="isLimitEnabled('max_exec_count')"
                  :model-value="String(flowLimits.max_exec_count)"
                  class="config-input"
                  type="number"
                  :max="systemLimits.max_exec_count"
                  @update:model-value="(val) => handleUpdateLimit('max_exec_count', val)"
                  @blur="(e) => clampOnBlur('max_exec_count', e)"
                />
                <el-switch :model-value="isLimitEnabled('max_exec_count')" @update:model-value="(v) => toggleLimit('max_exec_count', v)" />
              </div>
            </div>
          </div>
          <div class="config-item">
            <div class="config-item-header">
              <label class="config-label">运行记录保留<span class="limit-hint">/ 系统上限 {{ systemLimits.max_run_records }}，0 表示不存储</span></label>
              <div class="config-controls">
                <el-input
                  v-if="isLimitEnabled('max_run_records')"
                  :model-value="String(flowLimits.max_run_records)"
                  class="config-input"
                  type="number"
                  :min="0"
                  :max="systemLimits.max_run_records"
                  @update:model-value="(val) => handleUpdateLimit('max_run_records', val)"
                  @blur="(e) => clampOnBlur('max_run_records', e)"
                />
                <el-switch :model-value="isLimitEnabled('max_run_records')" @update:model-value="(v) => toggleLimit('max_run_records', v)" />
              </div>
            </div>
          </div>
          <div class="config-item">
            <div class="config-item-header">
              <label class="config-label">会话记忆保留<span class="limit-hint">/ 系统上限 {{ systemLimits.max_session_records }}，关闭表示不做记忆</span></label>
              <div class="config-controls">
                <el-input
                  v-if="isLimitEnabled('max_session_records')"
                  :model-value="String(flowLimits.max_session_records)"
                  class="config-input"
                  type="number"
                  :min="1"
                  :max="systemLimits.max_session_records"
                  @update:model-value="(val) => handleUpdateLimit('max_session_records', val)"
                  @blur="(e) => clampOnBlur('max_session_records', e)"
                />
                <el-switch :model-value="isLimitEnabled('max_session_records')" @update:model-value="(v) => toggleLimit('max_session_records', v)" />
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 上次运行页面 -->
      <div v-if="activeTab === 'lastRun'" class="last-run-panel">
        <div class="props-section">
          <div class="result-item result-item-inline">
            <label class="result-label">开始时间</label>
            <span class="result-value">{{ lastRunTime || '暂无数据' }}</span>
          </div>
          <div class="result-item">
            <label class="result-label">输入参数</label>
            <pre class="result-output">{{ lastRunArgsText || '暂无输入参数' }}</pre>
          </div>
          <div class="result-item">
            <label class="result-label">限额最终值</label>
            <pre class="result-output">{{ lastRunLimitsText || '暂无限额' }}</pre>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, provide } from 'vue'
import { runInfo } from '@/tenant/composables/runData.js'
import { Plus } from '@element-plus/icons-vue'
import StartNodePropsTreeItem from './StartNodePropsTreeItem.vue'

// ==================== Props ====================
const props = defineProps({
  selectedNode: {
    type: Object,
    default: null
  },
  flowArguments: {
    type: Object,
    default: () => ({})
  },
  flowLimits: {
    type: Object,
    default: () => ({ timeout: -1, max_recursion_depth: -1, max_node_count: -1, max_exec_count: -1, max_run_records: -1, max_session_records: -1 })
  },
  flowSystemLimits: {
    type: Object,
    default: () => ({ max_timeout: 300, max_recursion_depth: 50, max_node_count: 1000, max_exec_count: 20000, max_run_records: 100, max_session_records: 50 })
  }
})

// 运行信息（runData 共享 store：调试 finished / 历史 GetRunDetail 统一填充）
// 开始时间（runInfo.started_at）
const lastRunTime = computed(() => {
  const t = runInfo.value.started_at
  if (!t) return ''
  try { return new Date(t).toLocaleString() } catch (e) { return t }
})

// 输入参数（runInfo.run_args，实际入参）
const lastRunArgsText = computed(() => {
  const args = runInfo.value.run_args
  if (args == null) return ''
  return JSON.stringify(args, null, 2)
})

// 限额最终值（runInfo.run_limits，实际生效限额）
const lastRunLimitsText = computed(() => {
  const limits = runInfo.value.run_limits
  if (limits == null) return ''
  return JSON.stringify(limits, null, 2)
})

// 将系统限制字段映射为 flowLimits 的字段名
const systemLimits = computed(() => ({
  max_timeout: props.flowSystemLimits.max_timeout,
  max_recursion_depth: props.flowSystemLimits.max_recursion_depth,
  max_node_count: props.flowSystemLimits.max_node_count,
  max_exec_count: props.flowSystemLimits.max_exec_count,
  max_run_records: props.flowSystemLimits.max_run_records,
  max_session_records: props.flowSystemLimits.max_session_records
}))

// flowLimits key -> systemLimits key 映射
const SYS_LIMIT_KEY_MAP = {
  timeout: 'max_timeout',
  max_recursion_depth: 'max_recursion_depth',
  max_node_count: 'max_node_count',
  max_exec_count: 'max_exec_count',
  max_run_records: 'max_run_records',
  max_session_records: 'max_session_records'
}

/**
 * 生成配置项提示文本：显示系统级上限
 * @param {string} key - flowLimits 字段名
 * @returns {string}
 */
function limitHintText(key) {
  const max = systemLimits.value[SYS_LIMIT_KEY_MAP[key]]
  return `上限 ${max}`
}

// ==================== Emits ====================
const emit = defineEmits(['update-node', 'update-node-data', 'update-arguments', 'update-limits'])

// ==================== 状态管理 ====================
const activeTab = ref('settings')

const expandedKeys = ref(new Set())
const requiredKeysMap = ref({})

// 向子组件提供 expandedKeys（传递 ref 本身以保持响应式）
provide('expandedKeys', expandedKeys)

// ==================== 计算属性 ====================

/**
 * 树形数据
 */
const treeData = computed(() => {
  const args = props.flowArguments
  if (!args || !args.properties) {
    return []
  }
  // 初始化 requiredKeysMap
  initRequiredKeysMap(args)
  return buildTree(args.properties)
})

/**
 * 状态样式
 */


// ==================== 工具函数 ====================

/**
 * 生成5位随机字符串
 * @returns {string} 随机字符串
 */
function generateRandomId() {
  return Math.random().toString(36).substring(2, 7)
}

/**
 * 初始化 requiredKeysMap
 * @param {Object} args - flowArguments 对象
 */
function initRequiredKeysMap(args) {
  const newMap = {}
  collectRequiredKeys(args, '', newMap)
  requiredKeysMap.value = newMap
}

/**
 * 递归收集每个对象节点的 required keys
 * @param {Object} node - 当前节点
 * @param {string} parentKey - 父级 key
 * @param {Object} map - requiredKeysMap
 */
function collectRequiredKeys(node, parentKey, map) {
  if (!node || !node.properties) return

  for (const key of Object.keys(node.properties)) {
    const prop = node.properties[key]
    const fullKey = parentKey ? `${parentKey}.${key}` : key

    if (prop.type === 'object' && prop.properties) {
      // 递归处理子对象
      collectRequiredKeys(prop, fullKey, map)
    } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
      // 递归处理数组中的对象
      collectRequiredKeys(prop.items, fullKey, map)
    }
  }

  // 如果当前节点有 required 属性，添加到 map
  if (node.required && Array.isArray(node.required)) {
    map[parentKey] = [...node.required]
  } else if (parentKey) {
    map[parentKey] = []
  }
}

/**
 * 构建树形结构
 * @param {Object} properties - 属性对象
 * @param {string} parentKey - 父级key
 * @returns {Array} 树形节点数组
 */
function buildTree(properties, parentKey = '') {
  const result = []
  for (const key of Object.keys(properties)) {
    const prop = properties[key]
    const fullKey = parentKey ? `${parentKey}.${key}` : key
    const isLeaf = isLeafNode(prop)
    const children = isLeaf ? [] : getChildren(prop, fullKey)

    result.push({
      key: fullKey,
      label: key,
      type: prop.type,
      items: prop.items,
      properties: prop.properties,
      description: prop.description,
      isLeaf,
      children
    })
  }
  return result
}

/**
 * 判断是否为叶子节点
 * @param {Object} prop - 属性定义
 * @returns {boolean} 是否为叶子节点
 */
function isLeafNode(prop) {
  if (!prop || !prop.type) return true
  if (prop.type === 'string' || prop.type === 'number' || prop.type === 'boolean') return true
  if (prop.type === 'array') {
    if (!prop.items || !prop.items.type) return true
    if (prop.items.type === 'string' || prop.items.type === 'number' || prop.items.type === 'boolean') return true
  }
  return false
}

/**
 * 获取子节点
 * @param {Object} prop - 属性定义
 * @param {string} parentKey - 父级key
 * @returns {Array} 子节点数组
 */
function getChildren(prop, parentKey) {
  if (prop.type === 'object' && prop.properties) {
    return buildTree(prop.properties, parentKey)
  }
  if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
    return buildTree(prop.items.properties, parentKey)
  }
  return []
}

/**
 * 切换节点展开/折叠状态
 * @param {string} key - 节点key
 */
function toggleExpand(key) {
  if (expandedKeys.value.has(key)) {
    expandedKeys.value.delete(key)
  } else {
    expandedKeys.value.add(key)
  }
  expandedKeys.value = new Set(expandedKeys.value)
}

/**
 * 处理切换 required 状态
 * @param {string} key - 节点key
 */
function handleToggleRequired(key) {
  const parentKey = getParentKey(key)
  const label = getLabelFromKey(key)

  if (!requiredKeysMap.value[parentKey]) {
    requiredKeysMap.value[parentKey] = []
  }

  const parentRequiredKeys = requiredKeysMap.value[parentKey]
  const index = parentRequiredKeys.indexOf(label)

  if (index === -1) {
    parentRequiredKeys.push(label)
  } else {
    parentRequiredKeys.splice(index, 1)
  }

  requiredKeysMap.value = { ...requiredKeysMap.value }
  updateArgumentsWithRequired()
}

/**
 * 处理删除节点
 * @param {string} key - 节点key
 */
function handleDeleteNode(key) {
  const newArguments = JSON.parse(JSON.stringify(props.flowArguments))
  deleteNodeAndChildren(newArguments, key)
  emit('update-arguments', newArguments)
}

/**
 * 处理重命名节点
 * @param {string} oldKey - 旧的节点key
 * @param {string} newKey - 新的节点key
 */
function handleRenameNode(oldKey, newKey) {
  const newArguments = JSON.parse(JSON.stringify(props.flowArguments))
  
  renameNodeInData(newArguments, oldKey, newKey)
  
  const oldExpandedKeys = new Set(expandedKeys.value)
  const newExpandedKeys = new Set()
  for (const key of oldExpandedKeys) {
    if (key === oldKey) {
      newExpandedKeys.add(newKey)
    } else if (key.startsWith(oldKey + '.')) {
      newExpandedKeys.add(key.replace(oldKey + '.', newKey + '.'))
    } else {
      newExpandedKeys.add(key)
    }
  }
  expandedKeys.value = newExpandedKeys
  
  const oldRequiredMap = { ...requiredKeysMap.value }
  const newRequiredMap = {}
  for (const key of Object.keys(oldRequiredMap)) {
    let newMapKey = key
    if (key === oldKey) {
      newMapKey = newKey
    } else if (key.startsWith(oldKey + '.')) {
      newMapKey = key.replace(oldKey + '.', newKey + '.')
    }
    newRequiredMap[newMapKey] = oldRequiredMap[key]
  }
  requiredKeysMap.value = newRequiredMap
  
  emit('update-arguments', newArguments)
}

/**
 * 递归重命名节点数据
 * @param {Object} node - 当前节点
 * @param {string} oldKey - 旧的节点key
 * @param {string} newKey - 新的节点key
 * @param {string} parentKey - 父级key
 */
function renameNodeInData(node, oldKey, newKey, parentKey = '') {
  if (!node || !node.properties) return
  
  const keys = Object.keys(node.properties)
  for (const key of keys) {
    const prop = node.properties[key]
    const fullKey = parentKey ? `${parentKey}.${key}` : key
    
    if (fullKey === oldKey) {
      const oldLabel = key
      const newLabel = newKey.split('.').pop()
      
      node.properties[newLabel] = prop
      delete node.properties[oldLabel]
      
      if (node.required && Array.isArray(node.required)) {
        const index = node.required.indexOf(oldLabel)
        if (index !== -1) {
          node.required[index] = newLabel
        }
      }
      
      if (prop.type === 'object' && prop.properties) {
        renameChildKeys(prop, oldKey, newKey)
      } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
        renameChildKeys(prop.items, oldKey, newKey)
      }
    } else {
      if (prop.type === 'object' && prop.properties) {
        renameNodeInData(prop, oldKey, newKey, fullKey)
      } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
        renameNodeInData(prop.items, oldKey, newKey, fullKey)
      }
    }
  }
}

/**
 * 重命名子节点的key（更新嵌套路径）
 * @param {Object} node - 当前节点
 * @param {string} oldParentKey - 旧的父节点key
 * @param {string} newParentKey - 新的父节点key
 */
function renameChildKeys(node, oldParentKey, newParentKey) {
  if (!node || !node.properties) return
  
  const keys = Object.keys(node.properties)
  for (const key of keys) {
    const prop = node.properties[key]
    
    if (node.required && Array.isArray(node.required)) {
      const index = node.required.indexOf(key)
      if (index !== -1) {
        node.required[index] = key
      }
    }
    
    if (prop.type === 'object' && prop.properties) {
      renameChildKeys(prop, oldParentKey, newParentKey)
    } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
      renameChildKeys(prop.items, oldParentKey, newParentKey)
    }
  }
}

/**
 * 处理更新节点描述
 * @param {string} key - 节点key
 * @param {string} description - 新的描述内容
 */
function handleUpdateDescription(key, description) {
  const newArguments = JSON.parse(JSON.stringify(props.flowArguments))
  updateNodeDescription(newArguments, key, description)
  emit('update-arguments', newArguments)
}

/**
 * 递归更新节点描述
 * @param {Object} node - 当前节点
 * @param {string} targetKey - 目标节点key
 * @param {string} description - 新的描述内容
 * @param {string} parentKey - 父级key
 */
function updateNodeDescription(node, targetKey, description, parentKey = '') {
  if (!node || !node.properties) return
  
  for (const key of Object.keys(node.properties)) {
    const prop = node.properties[key]
    const fullKey = parentKey ? `${parentKey}.${key}` : key
    
    if (fullKey === targetKey) {
      if (description) {
        prop.description = description
      } else {
        delete prop.description
      }
      return
    }
    
    if (prop.type === 'object' && prop.properties) {
      updateNodeDescription(prop, targetKey, description, fullKey)
    } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
      updateNodeDescription(prop.items, targetKey, description, fullKey)
    }
  }
}

/**
 * 递归删除节点及其子节点
 * @param {Object} node - 当前节点
 * @param {string} targetKey - 要删除的目标key
 * @param {string} parentKey - 父级key
 * @returns {boolean} 是否删除了节点
 */
function deleteNodeAndChildren(node, targetKey, parentKey = '') {
  if (!node || !node.properties) return false

  for (const key of Object.keys(node.properties)) {
    const prop = node.properties[key]
    const fullKey = parentKey ? `${parentKey}.${key}` : key

    if (fullKey === targetKey) {
      // 删除目标节点
      delete node.properties[key]
      
      // 从 required 中移除
      if (node.required && Array.isArray(node.required)) {
        const index = node.required.indexOf(key)
        if (index !== -1) {
          node.required.splice(index, 1)
          if (node.required.length === 0) {
            delete node.required
          }
        }
      }
      
      // 清理 expandedKeys
      cleanupExpandedKeys(targetKey)
      
      return true
    }

    // 递归处理子节点
    let deleted = false
    if (prop.type === 'object' && prop.properties) {
      deleted = deleteNodeAndChildren(prop, targetKey, fullKey)
    } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
      deleted = deleteNodeAndChildren(prop.items, targetKey, fullKey)
    }

    if (deleted) return true
  }

  return false
}

/**
 * 清理 expandedKeys 中的相关项
 * @param {string} deletedKey - 被删除的节点key
 */
function cleanupExpandedKeys(deletedKey) {
  const newExpandedKeys = new Set()
  for (const key of expandedKeys.value) {
    if (!key.startsWith(deletedKey + '.') && key !== deletedKey) {
      newExpandedKeys.add(key)
    }
  }
  expandedKeys.value = newExpandedKeys
}

/**
 * 获取父级 key
 * @param {string} key - 完整 key
 * @returns {string} 父级 key
 */
function getParentKey(key) {
  const lastDotIndex = key.lastIndexOf('.')
  return lastDotIndex === -1 ? '' : key.substring(0, lastDotIndex)
}

/**
 * 从完整 key 获取最后的 label
 * @param {string} key - 完整 key
 * @returns {string} 最后的 label
 */
function getLabelFromKey(key) {
  const lastDotIndex = key.lastIndexOf('.')
  return lastDotIndex === -1 ? key : key.substring(lastDotIndex + 1)
}

/**
 * 更新 arguments 中的 required 字段
 */
function updateArgumentsWithRequired() {
  const newArguments = JSON.parse(JSON.stringify(props.flowArguments))
  updateRequiredInNode(newArguments, '', requiredKeysMap.value)
  emit('update-arguments', newArguments)
}

/**
 * 递归更新节点中的 required 属性
 * @param {Object} node - 当前节点
 * @param {string} parentKey - 父级 key
 * @param {Object} map - requiredKeysMap
 */
function updateRequiredInNode(node, parentKey, map) {
  if (!node || !node.properties) return

  // 更新当前节点的 required
  if (map[parentKey] && map[parentKey].length > 0) {
    node.required = [...map[parentKey]]
  } else {
    delete node.required
  }

  // 递归处理子节点
  for (const key of Object.keys(node.properties)) {
    const prop = node.properties[key]
    const fullKey = parentKey ? `${parentKey}.${key}` : key

    if (prop.type === 'object' && prop.properties) {
      updateRequiredInNode(prop, fullKey, map)
    } else if (prop.type === 'array' && prop.items && prop.items.type === 'object' && prop.items.properties) {
      updateRequiredInNode(prop.items, fullKey, map)
    }
  }
}

/**
 * 处理添加字段
 * @param {string} type - 字段类型
 */
function handleAddField(type) {
  const typeMap = {
    'number': { type: 'number', label: '数字' },
    'string': { type: 'string', label: '字符串' },
    'boolean': { type: 'boolean', label: '布尔值' },
    'object': { type: 'object', label: '对象' },
    'numberArray': { type: 'array', label: '数字数组', items: { type: 'number' } },
    'stringArray': { type: 'array', label: '字符串数组', items: { type: 'string' } },
    'booleanArray': { type: 'array', label: '布尔数组', items: { type: 'boolean' } },
    'objectArray': { type: 'array', label: '对象数组', items: { type: 'object', properties: {} } }
  }

  const config = typeMap[type]
  if (!config) return

  const fieldName = `${type}_${generateRandomId()}`

  const newProperties = { ...(props.flowArguments.properties || {}) }
  const fieldConfig = { type: config.type }

  if (config.items) {
    fieldConfig.items = config.items
  }

  if (config.type === 'object') {
    fieldConfig.properties = {}
  }

  newProperties[fieldName] = fieldConfig

  const newArguments = {
    ...props.flowArguments,
    type: 'object',
    properties: newProperties
  }

  emit('update-arguments', newArguments)
}

/**
 * 处理添加子节点
 * @param {string} type - 字段类型
 * @param {string} parentKey - 父节点key
 */
function handleAddChild(type, parentKey) {
  const typeMap = {
    'number': { type: 'number', label: '数字' },
    'string': { type: 'string', label: '字符串' },
    'boolean': { type: 'boolean', label: '布尔值' },
    'object': { type: 'object', label: '对象' },
    'numberArray': { type: 'array', label: '数字数组', items: { type: 'number' } },
    'stringArray': { type: 'array', label: '字符串数组', items: { type: 'string' } },
    'booleanArray': { type: 'array', label: '布尔数组', items: { type: 'boolean' } },
    'objectArray': { type: 'array', label: '对象数组', items: { type: 'object', properties: {} } }
  }

  const config = typeMap[type]
  if (!config) return

  const fieldName = `${type}_${generateRandomId()}`

  // 深度复制当前的 arguments
  const newArguments = JSON.parse(JSON.stringify(props.flowArguments))

  // 根据 parentKey 找到对应的位置并添加子节点
  const keys = parentKey.split('.')
  let current = newArguments

  for (let i = 0; i < keys.length; i++) {
    const key = keys[i]
    if (!current.properties) {
      current.properties = {}
    }

    if (i < keys.length - 1) {
      // 不是最后一个 key，继续深入
      current = current.properties[key]
      // 如果是数组，需要进入 items
      if (current.type === 'array' && current.items) {
        current = current.items
      }
    } else {
      // 最后一个 key，添加子节点
      const targetNode = current.properties[key]

      const fieldConfig = { type: config.type }
      if (config.items) {
        fieldConfig.items = config.items
      }
      if (config.type === 'object') {
        fieldConfig.properties = {}
      }

      if (targetNode.type === 'object') {
        if (!targetNode.properties) {
          targetNode.properties = {}
        }
        targetNode.properties[fieldName] = fieldConfig
      } else if (targetNode.type === 'array' && targetNode.items && targetNode.items.type === 'object') {
        if (!targetNode.items.properties) {
          targetNode.items.properties = {}
        }
        targetNode.items.properties[fieldName] = fieldConfig
      }
    }
  }

  // 自动展开父节点
  if (!expandedKeys.value.has(parentKey)) {
    expandedKeys.value.add(parentKey)
    expandedKeys.value = new Set(expandedKeys.value)
  }

  emit('update-arguments', newArguments)
}

/**
 * 判断某项流程配置是否启用（非 -1）
 * @param {string} key - 字段名
 * @returns {boolean}
 */
function isLimitEnabled(key) {
  return Number(props.flowLimits[key]) !== -1
}

/**
 * 切换流程配置开关
 * @param {string} key - 字段名
 * @param {boolean} enabled - 是否启用
 */
function toggleLimit(key, enabled) {
  const current = Number(props.flowLimits[key])
  let v
  if (enabled) {
    if (current >= 1) {
      v = current
    } else {
      // 本来是关闭的，点开时当前值 = 系统上限的 50%
      const sysMax = systemLimits.value[SYS_LIMIT_KEY_MAP[key]]
      v = (sysMax && sysMax > 0) ? Math.max(1, Math.floor(sysMax / 2)) : 1
    }
    // 检查系统上限
    const sysMax = systemLimits.value[SYS_LIMIT_KEY_MAP[key]]
    if (sysMax !== undefined && sysMax > 0 && v > sysMax) {
      v = sysMax
    }
  } else {
    v = -1
  }
  const newLimits = { ...props.flowLimits, [key]: v }
  emit('update-limits', newLimits)
}

/**
 * 处理流程配置字段更新
 * @param {string} key - 字段名
 * @param {string} value - 新值
 */
function handleUpdateLimit(key, value) {
  // max_run_records 最小值为 0（0=不存储）；max_session_records 最小值为 1（0 非法）；其余字段最小值为 2
  const minVal = key === 'max_run_records' ? 0 : (key === 'max_session_records' ? 1 : 2)
  let v = (value === null || value === undefined || value === '') ? minVal : Number(value)
  if (!Number.isFinite(v) || v < minVal) v = minVal
  // 检查系统上限，若超过则回退到最大值
  const sysMax = systemLimits.value[SYS_LIMIT_KEY_MAP[key]]
  if (sysMax !== undefined && sysMax > 0 && v > sysMax) {
    v = sysMax
  }
  const newLimits = { ...props.flowLimits, [key]: v }
  emit('update-limits', newLimits)
}

/**
 * 失焦时强制检查并回退到系统上限
 * @param {string} key - 字段名
 * @param {Event} e - 失焦事件
 */
function clampOnBlur(key, e) {
  const inputVal = e?.target?.value
  if (inputVal === undefined || inputVal === null || inputVal === '') return
  const sysMax = systemLimits.value[SYS_LIMIT_KEY_MAP[key]]
  if (sysMax === undefined || sysMax <= 0) return
  const num = Number(inputVal)
  if (Number.isFinite(num) && num > sysMax) {
    // 超出上限，重新触发更新为最大值
    handleUpdateLimit(key, String(sysMax))
  }
}
</script>

<style scoped>
/* ==================== 容器样式 ==================== */
.start-node-props {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}

/* ==================== 标签页导航 ==================== */
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

/* ==================== 标签页内容 ==================== */
.tab-content {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
}

/* ==================== 设置面板 - 与 CodeNodeProps 保持一致 ==================== */
.settings-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.tree-container {
  padding: 0 0 0 8px;
}

.empty-tree {
  text-align: center;
  padding: 20px;
  color: #999;
}

.empty-tree p {
  margin: 0;
}

/* ==================== 上次运行面板 ==================== */
.last-run-panel {
  padding: 0px 0px;
}

/* ==================== 设置面板分段 - 与 CodeNodeProps 保持一致 ==================== */
.props-section {
  background: #fafafa;
  border-radius: 8px;
  padding: 5px;
}

/* 执行结果页的 section 单独加足内边距（不动设置页共用的 .props-section 5px） */
.last-run-panel .props-section {
  padding: 16px 12px;
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

.result-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}

.result-item-inline {
  flex-direction: row;
  align-items: center;
  gap: 8px;
}

.result-label {
  font-size: 12px;
  color: #333;
  font-weight: 600;
}

.result-value {
  font-size: 13px;
  color: #333;
}

/* 状态颜色 */
.status-success {
  color: #67c23a;
}

.status-failed {
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

/* 下拉菜单图标样式 */
.dropdown-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  margin-right: 8px;
  flex-shrink: 0;
}

.dropdown-icon.icon-number-inline {
  background: #409eff;
  border-radius: 50%;
}

.dropdown-icon.icon-string-inline {
  background: #67c23a;
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

.dropdown-icon.icon-number-array-inline {
  background: #e6a23c;
  border-radius: 4px;
}

.dropdown-icon.icon-string-array-inline {
  background: #f56c6c;
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

/* ==================== 流程配置 ==================== */
.config-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 2px;
  padding-left: 8px;
}

.config-item:last-child {
  margin-bottom: 0;
}

.config-item-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.config-controls {
  display: flex;
  align-items: center;
  gap: 8px;
}

.config-label {
  font-size: 12px;
  color: #666;
}

.config-label .limit-hint {
  color: #999;
  font-weight: 400;
}

.config-input {
  width: 90px;
  height: 22px;
}

.config-input :deep(.el-input__wrapper) {
  border: none;
  box-shadow: none;
  background: #f5f5f5;
  border-radius: 4px;
  height: 22px;
  min-height: 22px;
  padding: 0 6px;
}

.config-input :deep(.el-input__inner) {
  text-align: left;
  padding: 0;
  border: none;
  background: #f5f5f5;
  height: 22px;
  line-height: 22px;
}
</style>

<style>
/* 下拉菜单右侧与窗口对齐 */
.dropdown-align-right {
  margin-right: 8px !important;
}
</style>