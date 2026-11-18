<template>
  <div class="variables-panel" :style="panelStyle">
    <div class="panel-resizer" @mousedown="startResize"></div>
    <div class="panel-header">
      <div class="tabs">
        <button
          class="tab-btn"
          :class="{ active: props.activeTab === 'flow' }"
          @click="emit('update:activeTab', 'flow')"
        >
          <el-icon class="tab-icon"><ChromeFilled /></el-icon>
          流程变量
        </button>
        <button
          class="tab-btn"
          :class="{ active: props.activeTab === 'environment' }"
          @click="emit('update:activeTab', 'environment')"
        >
          <el-icon class="tab-icon"><Notification /></el-icon>
          环境变量
        </button>
        <!-- 全局变量：租户级，这里只读（维护在「设置 → 全局变量」） -->
        <button
          class="tab-btn"
          :class="{ active: props.activeTab === 'global' }"
          @click="emit('update:activeTab', 'global')"
        >
          <el-icon class="tab-icon"><Key /></el-icon>
          全局变量
        </button>
      </div>
      <span class="close-icon" @click="$emit('close')">×</span>
    </div>
    <div class="panel-body">
      <div v-if="props.activeTab === 'flow'" class="tab-content">
        <div class="sub-tabs">
          <button class="sub-tab-btn" :class="{ active: flowSubTab === 'settings' }" @click="flowSubTab = 'settings'">设置</button>
          <button class="sub-tab-btn" :class="{ active: flowSubTab === 'run' }" @click="flowSubTab = 'run'">执行结果</button>
        </div>

        <div v-if="flowSubTab === 'settings'">
        <div class="description-text">流程变量用于存储工作流运行中的数据信息，可在任何工作流节点中读写。</div>
        <div class="add-button-container">
          <el-dropdown @command="handleAddFlowVariable" trigger="click">
            <button class="add-variable-btn">
              <el-icon><Plus /></el-icon>
              添加流程变量
            </button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="string">
                  <span class="dropdown-icon icon-string-inline">S</span>字符串
                </el-dropdown-item>
                <el-dropdown-item command="number">
                  <span class="dropdown-icon icon-number-inline">N</span>数字
                </el-dropdown-item>
                <el-dropdown-item command="boolean">
                  <span class="dropdown-icon icon-boolean-inline">B</span>布尔值
                </el-dropdown-item>
                <el-dropdown-item command="object">
                  <span class="dropdown-icon icon-object-inline">O</span>对象
                </el-dropdown-item>
                <el-dropdown-item command="array[string]">
                  <span class="dropdown-icon icon-string-array-inline">S</span>字符串数组
                </el-dropdown-item>
                <el-dropdown-item command="array[number]">
                  <span class="dropdown-icon icon-number-array-inline">N</span>数字数组
                </el-dropdown-item>
                <el-dropdown-item command="array[boolean]">
                  <span class="dropdown-icon icon-boolean-array-inline">B</span>布尔数组
                </el-dropdown-item>
                <el-dropdown-item command="array[object]">
                  <span class="dropdown-icon icon-object-array-inline">O</span>对象数组
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
        
        <div class="flow-variables-cards">
          <div v-if="Object.keys(sortedFlowVariables).length === 0" class="flow-empty-state">
            <p>暂无流程变量</p>
          </div>
          
          <div
            v-for="(config, key) in sortedFlowVariables"
            :key="key"
            class="flow-variable-card"
          >
            <div class="flow-card-header">
              <span class="flow-type-badge" :class="getTypeClass(config.type)">
                {{ getTypeIcon(config.type) }}
              </span>
              <div class="flow-card-content">
                <input
                  v-if="editingNameKey === key"
                  ref="nameInputRef"
                  v-model="editingNameTemp"
                  class="flow-variable-name-input"
                  @click.stop
                  @blur="finishEditFlowName(key)"
                  @keydown.enter="finishEditFlowName(key)"
                  @keydown.esc="cancelEditName"
                />
                <span
                  v-else
                  class="flow-variable-name flow-variable-name-editable"
                  @click.stop="startEditName(key)"
                >{{ key }}</span>
                <input
                  v-if="editingDescKey === key"
                  v-model="editingDescTemp"
                  class="flow-card-description-input"
                  placeholder="添加描述"
                  @click.stop
                  @blur="finishEditFlowDesc(key)"
                  @keydown.enter="finishEditFlowDesc(key)"
                  @keydown.esc="cancelEditDesc"
                />
                <span
                  v-else
                  class="flow-card-description flow-card-description-editable"
                  @click.stop="startEditDesc(key, config.description)"
                >{{ config.description || '无描述' }}</span>
              </div>
              <el-icon class="flow-expand-icon" :class="{ rotated: expandedCards.has(key) }" @click="toggleCard(key)">
                <ArrowDown />
              </el-icon>
              <el-icon class="flow-delete-icon" @click="handleDeleteVariable(key)">
                <Delete />
              </el-icon>
            </div>
            
            <div class="flow-card-body" v-show="expandedCards.has(key)">
              <template v-if="config.type === 'string'">
                <div class="flow-form-item">
                  <label>默认值</label>
                  <textarea
                    class="flow-form-textarea"
                    :value="config.default"
                    @change="handleUpdateVariable(key, 'default', $event.target.value)"
                  ></textarea>
                </div>
              </template>
              
              <template v-else-if="config.type === 'number'">
                <div class="flow-form-item">
                  <label>默认值</label>
                  <input
                    type="number"
                    :value="parseNumber(config.default)"
                    class="flow-form-input"
                    @change="handleUpdateVariable(key, 'default', $event.target.value)"
                  />
                </div>
              </template>
              
              <template v-else-if="config.type === 'boolean'">
                <div class="flow-form-item">
                  <label>默认值</label>
                  <div class="flow-boolean-selector">
                    <button
                      class="flow-boolean-btn"
                      :class="{ active: config.default === 'true' }"
                      @click="handleUpdateVariable(key, 'default', 'true')"
                    >true</button>
                    <button
                      class="flow-boolean-btn"
                      :class="{ active: config.default === 'false' }"
                      @click="handleUpdateVariable(key, 'default', 'false')"
                    >false</button>
                  </div>
                </div>
              </template>
              
              <template v-else-if="config.type === 'object'">
                <div class="flow-form-item">
                  <label>成员属性</label>
                  <div class="properties-container">
                    <div v-if="Object.keys(config.properties || {}).length === 0" class="empty-properties">
                      <p>暂无成员属性</p>
                    </div>
                    <div
                      v-for="(propConfig, propKey) in config.properties || {}"
                      :key="propKey"
                      class="property-card"
                    >
                      <div class="property-card-header">
                        <span class="prop-type-badge" :class="getTypeClass(propConfig.type)">
                          {{ getTypeIcon(propConfig.type) }}
                        </span>
                        <input
                          type="text"
                          :value="propKey"
                          class="flow-form-input property-name-input"
                          @change="handleRenameProperty(key, propKey, $event.target.value)"
                        />
                        <button
                          class="prop-delete-btn"
                          @click="handleDeleteProperty(key, propKey)"
                        >
                          <el-icon><Delete /></el-icon>
                        </button>
                      </div>
                      <div class="property-card-body">
                        <template v-if="propConfig.type === 'boolean'">
                          <div class="flow-boolean-selector">
                            <button
                              class="flow-boolean-btn"
                              :class="{ active: propConfig.default === 'true' }"
                              @click="handleUpdateProperty(key, propKey, 'default', 'true')"
                            >true</button>
                            <button
                              class="flow-boolean-btn"
                              :class="{ active: propConfig.default === 'false' }"
                              @click="handleUpdateProperty(key, propKey, 'default', 'false')"
                            >false</button>
                          </div>
                        </template>
                        <template v-else>
                          <input
                            type="text"
                            :value="propConfig.default"
                            class="flow-form-input property-default-input"
                            @change="handleUpdateProperty(key, propKey, 'default', $event.target.value)"
                            :placeholder="getDefaultPlaceholder(propConfig.type)"
                          />
                        </template>
                      </div>
                    </div>
                  </div>
                  <el-dropdown @command="(type) => handleAddProperty(key, type)" trigger="click">
                    <button class="add-property-btn">
                      <el-icon><Plus /></el-icon>
                      添加成员属性
                    </button>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item command="string">
                          <span class="dropdown-icon icon-string-inline">S</span>字符串
                        </el-dropdown-item>
                        <el-dropdown-item command="number">
                          <span class="dropdown-icon icon-number-inline">N</span>数字
                        </el-dropdown-item>
                        <el-dropdown-item command="boolean">
                          <span class="dropdown-icon icon-boolean-inline">B</span>布尔值
                        </el-dropdown-item>
                      </el-dropdown-menu>
                    </template>
                  </el-dropdown>
                </div>
              </template>
              
              <template v-else-if="config.type === 'array[string]'">
                <div class="flow-form-item">
                  <label>默认值 (字符串数组)</label>
                  <textarea
                    class="flow-form-textarea"
                    :value="formatJson(config.default)"
                    @change="handleUpdateVariable(key, 'default', $event.target.value)"
                  ></textarea>
                </div>
              </template>
              
              <template v-else-if="config.type === 'array[number]'">
                <div class="flow-form-item">
                  <label>默认值 (数字数组)</label>
                  <textarea
                    class="flow-form-textarea"
                    :value="formatJson(config.default)"
                    @change="handleUpdateVariable(key, 'default', $event.target.value)"
                  ></textarea>
                </div>
              </template>
              
              <template v-else-if="config.type === 'array[boolean]'">
                <div class="flow-form-item">
                  <label>默认值 (布尔数组)</label>
                  <textarea
                    class="flow-form-textarea"
                    :value="formatJson(config.default)"
                    @change="handleUpdateVariable(key, 'default', $event.target.value)"
                  ></textarea>
                </div>
              </template>
              
              <template v-else-if="config.type === 'array[object]'">
                <div class="flow-form-item">
                  <label>默认值 (对象数组)</label>
                  <textarea
                    class="flow-form-textarea"
                    :value="formatJson(config.default)"
                    @change="handleUpdateVariable(key, 'default', $event.target.value)"
                  ></textarea>
                </div>
              </template>
              
            </div>
          </div>
        </div>
        </div>

        <div v-else class="flow-run-result">
          <pre>{{ flowRunVarsText }}</pre>
        </div>
      </div>
      
      <!-- 全局变量：租户级配置，在这个面板里只读 —— 维护入口统一在「设置 → 全局变量」，
           免得同一个值在两处都能改。引用方式是在节点「输入变量」里选来源。 -->
      <div v-else-if="props.activeTab === 'global'" class="tab-content">
        <div class="description-text">
          全局变量是本租户所有工作流 / 智能体 / 技能共用的配置，这里只读，维护在「设置 → 全局变量」。
          引用方式：在节点的「输入变量」里把来源选为「全局变量」，模板里写输入变量名。
        </div>
        <div class="gv-toolbar">
          <span class="gv-count">共 {{ globalVars.length }} 个</span>
          <button class="gv-refresh-btn" :disabled="globalVarsLoading" @click="loadGlobalVars">
            {{ globalVarsLoading ? '加载中…' : '刷新' }}
          </button>
        </div>
        <div v-if="!globalVarsLoading && !globalVars.length" class="flow-empty-state">
          <p>暂无全局变量（在「设置 → 全局变量」里创建）</p>
        </div>
        <div class="gv-list">
          <div v-for="gv in globalVars" :key="gv.name" class="gv-row">
            <span class="gv-kind" :class="'gv-kind--' + gv.kind">{{ GLOBAL_KIND_LABEL[gv.kind] || gv.kind }}</span>
            <div class="gv-row-main">
              <div class="gv-row-name" :title="gv.name">{{ gv.name }}</div>
              <div class="gv-row-desc" :title="gv.description || ''">{{ gv.description || '无描述' }}</div>
            </div>
            <span class="gv-row-value" :class="{ 'is-secret': gv.kind === 'secret' }">{{ globalVarDisplay(gv) }}</span>
          </div>
        </div>
      </div>

      <div v-else class="tab-content">
        <div class="description-text">
          环境变量用于配置工作流运行时的环境信息，包含字符串、数字和密钥类型。
          租户级配置（地址、账号名、密钥等）不在这里：请在节点的输入变量里把来源选为「全局变量」，模板里用输入变量引用。
        </div>
        <div class="add-button-container">
          <el-dropdown @command="handleAddEnvironmentVariable" trigger="click">
            <button class="add-variable-btn">
              <el-icon><Plus /></el-icon>
              添加环境变量
            </button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="string">
                  <span class="dropdown-icon icon-string-inline">S</span>字符串
                </el-dropdown-item>
                <el-dropdown-item command="number">
                  <span class="dropdown-icon icon-number-inline">N</span>数字
                </el-dropdown-item>
                <el-dropdown-item command="secret">
                  <span class="dropdown-icon icon-secret-inline">K</span>密钥
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
        
        <div class="env-variables-list">
          <div v-if="Object.keys(environmentVariables).length === 0" class="env-empty-state">
            <p>暂无环境变量</p>
          </div>
          <div
            v-for="(config, key) in environmentVariables"
            :key="key"
            class="env-variable-card"
          >
            <div class="env-card-header">
              <span class="env-type-badge" :class="getEnvTypeClass(config.type)">
                {{ getEnvTypeIcon(config.type) }}
              </span>
              <div class="env-card-content">
                <input
                  v-if="editingNameKey === key"
                  v-model="editingNameTemp"
                  class="env-variable-name-input"
                  @click.stop
                  @blur="finishEditEnvName(key)"
                  @keydown.enter="finishEditEnvName(key)"
                  @keydown.esc="cancelEditName"
                />
                <span
                  v-else
                  class="env-variable-name env-variable-name-editable"
                  @click.stop="startEditName(key)"
                >{{ key }}</span>
                <input
                  v-if="editingDescKey === key"
                  v-model="editingDescTemp"
                  class="env-card-description-input"
                  placeholder="添加描述"
                  @click.stop
                  @blur="finishEditEnvDesc(key)"
                  @keydown.enter="finishEditEnvDesc(key)"
                  @keydown.esc="cancelEditDesc"
                />
                <span
                  v-else
                  class="env-card-description env-card-description-editable"
                  @click.stop="startEditDesc(key, config.description)"
                >{{ config.description || '无描述' }}</span>
              </div>
              <el-icon class="env-expand-icon" :class="{ rotated: expandedEnvCards.has(key) }" @click="toggleEnvCard(key)">
                <ArrowDown />
              </el-icon>
              <el-icon class="env-delete-icon" @click="handleDeleteEnvVariable(key)">
                <Delete />
              </el-icon>
            </div>
            
            <div class="env-card-body" v-show="expandedEnvCards.has(key)">
              <!-- 已废弃的类型 global（全局变量改成节点输入的来源了）：老数据仍可能躺在这里，
                   给一句迁移提示，而不是让它退化成「一个叫 value 的普通字符串」。 -->
              <div v-if="config.type === 'global'" class="gv-hint warn">
                该类型已废弃：请在节点的输入变量里新增一项，来源选「全局变量」，模板里用 &#123;&#123; .input.变量名 &#125;&#125; 引用。
              </div>
              <template v-else-if="config.type === 'secret'">
                <div class="env-form-item">
                  <label>配置值</label>
                  <!-- 后端不回显明文：value 恒为空，留空提交 = 沿用原值（后端按名字合并）。
                       所以这里不能预填，也必须把「留空不改」写出来 ——
                       否则用户会以为输入框空着就是「密码被清空了」。 -->
                  <input
                    type="password"
                    :value="config.value"
                    :placeholder="config.has_value ? '已设置，留空表示不修改' : '变量值'"
                    class="env-form-input"
                    @change="handleUpdateEnvVariable(key, 'value', $event.target.value)"
                  />
                  <div v-if="config.has_value" class="gv-hint">
                    已设置值：界面上不回显，留空保存即沿用原值；要清掉只能删掉这个变量重建
                  </div>
                </div>
              </template>
              <template v-else-if="config.type === 'number'">
                <div class="env-form-item">
                  <label>配置值</label>
                  <input
                    type="number"
                    :value="config.value"
                    class="env-form-input"
                    @change="handleUpdateEnvVariable(key, 'value', $event.target.value)"
                  />
                </div>
              </template>
              <template v-else>
                <div class="env-form-item">
                  <label>配置值</label>
                  <textarea
                    class="env-form-textarea"
                    :value="config.value"
                    @change="handleUpdateEnvVariable(key, 'value', $event.target.value)"
                  ></textarea>
                </div>
              </template>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted, watch, nextTick, inject } from 'vue'
import { ElMessage } from 'element-plus'
import { ChromeFilled, Notification, Plus, ArrowDown, Delete, Key } from '@element-plus/icons-vue'
import { runInfo } from '@/tenant/composables/runData.js'
import { getDataTypeIcon, getDataTypeIconClass, getDataTypeLabel } from './dataTypeBadge.js'
import { useEscClose } from '../../../composables/useEscClose'
import adminApi from '../../../config/admin.js'
import { authHeaders, checkBizOk } from '../../settings/http.js'

const props = defineProps({
  activeTab: {
    type: String,
    default: 'flow'
  },
  runPanelVisible: Boolean,
  runPanelWidth: {
    type: Number,
    default: 300
  },
  propsPanelVisible: Boolean,
  propsPanelWidth: {
    type: Number,
    default: 300
  },
  width: {
    type: Number,
    default: 380
  },
  flowVariables: {
    type: Object,
    default: () => ({})
  },
  environmentVariables: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits(['close', 'resize', 'update:activeTab', 'update:flowVariables', 'update:environmentVariables'])

// 父级 v-if 挂载：挂上即开着。行内改名 / 改描述时 ESC 先给输入框，不关面板
useEscClose(true, () => emit('close'), { skipWhenEditable: true })

const flowDebug = inject('flowDebug', null)
const flowSubTab = inject('flowSubTab', ref('settings'))

const panelWidth = ref(props.width)
const isResizing = ref(false)
const expandedCards = ref(new Set())
const expandedEnvCards = ref(new Set())
const editingNameKey = ref(null)
const editingNameTemp = ref('')
const editingDescKey = ref(null)
const editingDescTemp = ref('')

const localFlowVariables = ref({})

watch(() => props.width, (newWidth) => {
  panelWidth.value = newWidth
})

watch(() => props.flowVariables, (newVariables) => {
  const variablesWithTime = {}
  let maxTime = 0
  
  for (const key in localFlowVariables.value) {
    if (localFlowVariables.value.hasOwnProperty(key) && localFlowVariables.value[key].time) {
      maxTime = Math.max(maxTime, localFlowVariables.value[key].time)
    }
  }
  
  let timeCounter = maxTime + 1
  for (const key in newVariables) {
    if (newVariables.hasOwnProperty(key)) {
      if (localFlowVariables.value[key] && localFlowVariables.value[key].time) {
        variablesWithTime[key] = {
          ...newVariables[key],
          time: localFlowVariables.value[key].time
        }
      } else {
        variablesWithTime[key] = {
          ...newVariables[key],
          time: timeCounter++
        }
      }
    }
  }
  
  localFlowVariables.value = variablesWithTime
}, { immediate: true, deep: true })

// ── 全局变量（只读浏览）──
// 维护入口在「设置 → 全局变量」。这里只列名字 / 类型 / 描述：接口不回 secret 的值，
// 所以密钥类只显示「已设置 / 未设置」。
const GLOBAL_KIND_LABEL = { text: '文本', number: '数字', bool: '布尔', secret: '密钥' }
const globalVars = ref([])
const globalVarsLoading = ref(false)
const globalVarsLoaded = ref(false)

const globalVarDisplay = (v) => {
  if (v.kind === 'secret') return v.has_value ? '已设置' : '未设置'
  if (v.kind === 'bool') return String(v.value).trim().toLowerCase() === 'true' ? '真' : '假'
  return v.value === '' || v.value == null ? '（空）' : v.value
}

const loadGlobalVars = async () => {
  globalVarsLoading.value = true
  try {
    const res = await fetch(adminApi.globalVars.list, { headers: authHeaders() })
    const data = await checkBizOk(res)
    globalVars.value = data.data?.items || []
    globalVarsLoaded.value = true
  } catch (e) {
    ElMessage.error('读取全局变量失败：' + e.message)
  } finally {
    globalVarsLoading.value = false
  }
}

// 切到这个页签才拉：编流程时大部分时间不看它，没必要每次开面板都请求
watch(() => props.activeTab, (tab) => {
  if (tab === 'global' && !globalVarsLoaded.value) loadGlobalVars()
})

onMounted(() => {
  emit('resize', panelWidth.value)
  if (props.activeTab === 'global') loadGlobalVars()
})

const panelStyle = computed(() => {
  let rightOffset = 0
  if (props.runPanelVisible) {
    rightOffset += props.runPanelWidth + 6
  }
  return {
    right: rightOffset + 'px',
    width: panelWidth.value + 'px'
  }
})

// ── 流程变量运行时值（执行结果）──
const flowRunVars = computed(() => {
  if (flowDebug?.connected?.value && flowDebug.variables?.value?.flow_vars != null) {
    return flowDebug.variables.value.flow_vars
  }
  return runInfo.value?.run_flow_vars ?? null
})
const flowRunVarsText = computed(() => {
  const v = flowRunVars.value
  if (v == null) return '暂无运行数据'
  return JSON.stringify(v, null, 2)
})

function requestFlowVars() {
  if (flowDebug && flowDebug.connected.value) flowDebug.inspect()
}

// ① 打开「执行结果」子 tab 时拉取（immediate：面板挂载时若已是 run 也拉取）
watch(flowSubTab, (tab) => {
  if (tab === 'run') requestFlowVars()
}, { immediate: true })

// ② 断点停止/单步暂停（mode 变 pause）时，若「执行结果」子 tab 打开则拉取
watch(() => flowDebug?.mode?.value, (m) => {
  if (m === 'pause' && flowSubTab.value === 'run') requestFlowVars()
})

const sortedFlowVariables = computed(() => {
  const variables = props.flowVariables
  const sorted = Object.keys(variables)
    .sort((a, b) => {
      const timeA = localFlowVariables.value[a]?.time || 0
      const timeB = localFlowVariables.value[b]?.time || 0
      return timeA - timeB
    })
    .reduce((acc, key) => {
      acc[key] = variables[key]
      return acc
    }, {})
  return sorted
})

const startResize = (e) => {
  isResizing.value = true
  document.addEventListener('mousemove', onResize)
  document.addEventListener('mouseup', stopResize)
  e.preventDefault()
}

const onResize = (e) => {
  if (!isResizing.value) return
  const runPanelOffset = props.runPanelVisible ? props.runPanelWidth + 3 : 0
  const newWidth = window.innerWidth - e.clientX - runPanelOffset
  panelWidth.value = Math.max(380, Math.min(newWidth, 1000))
  emit('resize', panelWidth.value)
}

const stopResize = () => {
  isResizing.value = false
  document.removeEventListener('mousemove', onResize)
  document.removeEventListener('mouseup', stopResize)
  sessionStorage.setItem('variablesPanelWidth', panelWidth.value.toString())
  emit('resize', panelWidth.value)
}

const formatValue = (value) => {
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value, null, 2)
    } catch {
      return String(value)
    }
  }
  return String(value)
}

const formatJson = (value) => {
  if (!value || value === 'null') return ''
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value)
      return JSON.stringify(parsed, null, 2)
    } catch {
      return value
    }
  }
  return JSON.stringify(value, null, 2)
}

const parseNumber = (value) => {
  if (!value || value === 'null') return ''
  const num = parseFloat(value)
  return isNaN(num) ? '' : num
}

// 类型徽标的文案 / 配色 / 字母统一在 dataTypeBadge.js（模板里沿用这些旧名字）
const getTypeLabel = getDataTypeLabel
const getTypeClass = getDataTypeIconClass
const getTypeIcon = getDataTypeIcon

const toggleCard = (key) => {
  if (expandedCards.value.has(key)) {
    expandedCards.value.delete(key)
  } else {
    expandedCards.value.add(key)
  }
  expandedCards.value = new Set(expandedCards.value)
}

// 名字内联编辑
const startEditName = (key) => {
  editingNameKey.value = key
  editingNameTemp.value = key
  nextTick(() => {
    const el = document.querySelector('.flow-variable-name-input, .env-variable-name-input')
    if (el) el.focus()
  })
}

const finishEditFlowName = (oldKey) => {
  const newName = editingNameTemp.value.trim()
  if (!newName || newName === oldKey) {
    editingNameKey.value = null
    return
  }
  handleRenameVariable(oldKey, newName)
  editingNameKey.value = null
}

const finishEditEnvName = (oldKey) => {
  const newName = editingNameTemp.value.trim()
  if (!newName || newName === oldKey) {
    editingNameKey.value = null
    return
  }
  handleRenameEnvVariable(oldKey, newName)
  editingNameKey.value = null
}

const cancelEditName = () => {
  editingNameKey.value = null
}

// 描述内联编辑
const startEditDesc = (key, currentDesc) => {
  editingDescKey.value = key
  editingDescTemp.value = currentDesc || ''
  nextTick(() => {
    const el = document.querySelector('.flow-card-description-input, .env-card-description-input')
    if (el) el.focus()
  })
}

const finishEditFlowDesc = (key) => {
  handleUpdateVariable(key, 'description', editingDescTemp.value)
  editingDescKey.value = null
}

const finishEditEnvDesc = (key) => {
  handleUpdateEnvVariable(key, 'description', editingDescTemp.value)
  editingDescKey.value = null
}

const cancelEditDesc = () => {
  editingDescKey.value = null
}

const generateRandomId = () => {
  return Math.random().toString(36).substring(2, 8)
}

const removeTimeFromVariables = (variables) => {
  const cleaned = {}
  for (const key in variables) {
    if (variables.hasOwnProperty(key)) {
      const { time, ...rest } = variables[key]
      cleaned[key] = rest
    }
  }
  return cleaned
}

const handleAddFlowVariable = (type) => {
  const defaultValues = {
    'string': '',
    'number': '',
    'boolean': 'false',
    'object': '{}',
    'array[string]': '[]',
    'array[number]': '[]',
    'array[boolean]': '[]',
    'array[object]': '[]'
  }
  
  const sanitizedType = type.replace('[', '_').replace(']', '')
  const fieldName = `${sanitizedType}_${generateRandomId()}`
  const newTime = Date.now()
  
  const newVariables = { ...props.flowVariables }
  if (type === 'object') {
    newVariables[fieldName] = {
      type,
      description: '',
      properties: {}
    }
  } else {
    newVariables[fieldName] = {
      type,
      description: '',
      default: defaultValues[type]
    }
  }
  
  localFlowVariables.value[fieldName] = {
    ...newVariables[fieldName],
    time: newTime
  }
  
  emit('update:flowVariables', removeTimeFromVariables({
    ...localFlowVariables.value,
    ...newVariables
  }))
}

const handleUpdateVariable = (key, field, value) => {
  const newVariables = { ...props.flowVariables }
  newVariables[key] = {
    ...newVariables[key],
    [field]: value
  }
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const validateKey = (key) => {
  const regex = /^[a-zA-Z][a-zA-Z0-9_]*$/
  return regex.test(key)
}

const handleRenameVariable = (oldKey, newKey) => {
  if (!newKey.trim()) {
    alert('变量名不能为空')
    return
  }
  
  if (!validateKey(newKey)) {
    alert('变量名必须以英文字母开头，且只能包含英文字母、数字和下划线')
    return
  }
  
  if (oldKey === newKey) return
  
  const newVariables = { ...props.flowVariables }
  newVariables[newKey] = { ...newVariables[oldKey] }
  delete newVariables[oldKey]
  
  if (localFlowVariables.value[oldKey]) {
    localFlowVariables.value[newKey] = {
      ...localFlowVariables.value[oldKey]
    }
    delete localFlowVariables.value[oldKey]
  }
  
  if (expandedCards.value.has(oldKey)) {
    expandedCards.value.delete(oldKey)
    expandedCards.value.add(newKey)
  }
  
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const handleAddProperty = (varKey, type) => {
  const newVariables = { ...props.flowVariables }
  const properties = { ...newVariables[varKey].properties } || {}
  const fieldName = `field_${generateRandomId()}`
  const defaultValues = {
    'string': '',
    'number': '',
    'boolean': 'false'
  }
  properties[fieldName] = {
    type,
    default: defaultValues[type] || ''
  }
  newVariables[varKey] = {
    ...newVariables[varKey],
    properties
  }
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const handleUpdateProperty = (varKey, propKey, field, value) => {
  const newVariables = { ...props.flowVariables }
  const properties = { ...newVariables[varKey].properties } || {}
  properties[propKey] = {
    ...properties[propKey],
    [field]: value
  }
  newVariables[varKey] = {
    ...newVariables[varKey],
    properties
  }
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const handleRenameProperty = (varKey, oldPropKey, newPropKey) => {
  if (!newPropKey.trim()) {
    alert('属性名不能为空')
    return
  }
  
  if (!validateKey(newPropKey)) {
    alert('属性名必须以英文字母开头，且只能包含英文字母、数字和下划线')
    return
  }
  
  if (oldPropKey === newPropKey) return
  
  const newVariables = { ...props.flowVariables }
  const properties = { ...newVariables[varKey].properties } || {}
  properties[newPropKey] = { ...properties[oldPropKey] }
  delete properties[oldPropKey]
  newVariables[varKey] = {
    ...newVariables[varKey],
    properties
  }
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const handleDeleteProperty = (varKey, propKey) => {
  const newVariables = { ...props.flowVariables }
  const properties = { ...newVariables[varKey].properties } || {}
  delete properties[propKey]
  newVariables[varKey] = {
    ...newVariables[varKey],
    properties
  }
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const getDefaultPlaceholder = (type) => {
  const placeholders = {
    'string': '字符串默认值',
    'number': '数字默认值',
    'boolean': 'true 或 false'
  }
  return placeholders[type] || ''
}

const handleDeleteVariable = (key) => {
  if (!confirm(`确定要删除变量 "${key}" 吗？`)) return
  
  const newVariables = { ...props.flowVariables }
  delete newVariables[key]
  expandedCards.value.delete(key)
  delete localFlowVariables.value[key]
  emit('update:flowVariables', removeTimeFromVariables(newVariables))
}

const toggleEnvCard = (key) => {
  if (expandedEnvCards.value.has(key)) {
    expandedEnvCards.value.delete(key)
  } else {
    expandedEnvCards.value.add(key)
  }
  expandedEnvCards.value = new Set(expandedEnvCards.value)
}

const getEnvTypeClass = (type) => {
  const typeClasses = {
    'string': 'type-string',
    'number': 'type-number',
    'secret': 'type-secret'
  }
  return typeClasses[type] || 'type-string'
}

const getEnvTypeIcon = (type) => {
  const typeIcons = {
    'string': 'S',
    'number': 'N',
    'secret': 'K'
  }
  return typeIcons[type] || 'S'
}

const handleAddEnvironmentVariable = (type) => {
  const defaultValues = {
    'string': '',
    'number': '0',
    'secret': ''
  }
  
  const sanitizedType = type.replace('[', '_').replace(']', '')
  const fieldName = `${sanitizedType}_${generateRandomId()}`
  
  const newEnvVariables = { ...props.environmentVariables }
  newEnvVariables[fieldName] = {
    type,
    value: defaultValues[type],
    description: ''
  }
  emit('update:environmentVariables', newEnvVariables)
}

const handleUpdateEnvVariable = (key, field, value) => {
  const newEnvVariables = { ...props.environmentVariables }
  const currentConfig = { ...newEnvVariables[key] }
  
  if (field === 'value' && currentConfig.type === 'number') {
    if (!value || value === '' || value === null) {
      value = '0'
    }
  }
  
  newEnvVariables[key] = {
    ...currentConfig,
    [field]: value
  }
  emit('update:environmentVariables', newEnvVariables)
}

const handleRenameEnvVariable = (oldKey, newKey) => {
  if (!newKey.trim()) {
    alert('变量名不能为空')
    return
  }
  
  if (!validateKey(newKey)) {
    alert('变量名必须以英文字母开头，且只能包含英文字母、数字和下划线')
    return
  }
  
  if (oldKey === newKey) return
  
  const newEnvVariables = { ...props.environmentVariables }
  newEnvVariables[newKey] = { ...newEnvVariables[oldKey] }
  delete newEnvVariables[oldKey]
  
  if (expandedEnvCards.value.has(oldKey)) {
    expandedEnvCards.value.delete(oldKey)
    expandedEnvCards.value.add(newKey)
  }
  
  emit('update:environmentVariables', newEnvVariables)
}

const handleDeleteEnvVariable = (key) => {
  if (!confirm(`确定要删除环境变量 "${key}" 吗？`)) return
  
  const newEnvVariables = { ...props.environmentVariables }
  delete newEnvVariables[key]
  expandedEnvCards.value.delete(key)
  emit('update:environmentVariables', newEnvVariables)
}

onUnmounted(() => {
  document.removeEventListener('mousemove', onResize)
  document.removeEventListener('mouseup', stopResize)
})
</script>

<style scoped>
.variables-panel {
  position: absolute;
  z-index: 99;
  top: 2%;
  bottom: 2%;
  background: #ffffff;
  border-radius: 8px;
  box-shadow: -4px 0 20px rgba(0, 0, 0, 0.2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.panel-resizer {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  cursor: col-resize;
  background: transparent;
  z-index: 10;
}

.panel-resizer:hover {
  background: #e5e7eb;
}

.panel-resizer:active {
  background: #9ca3af;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  border-bottom: 0px solid #eee;
}

/* 三个页签（流程变量 / 环境变量 / 全局变量）要能挤进默认宽度：
   gap 收到 10px、按钮内边距收到 6px，并禁止文字换行 */
.tabs {
  display: flex;
  gap: 10px;
  min-width: 0;
}

.tab-btn {
  display: flex;
  align-items: center;
  padding: 4px 6px;
  border: none;
  background: transparent;
  font-size: 13px;
  font-weight: 600;
  color: #777;
  white-space: nowrap;
  cursor: pointer;
  position: relative;
  transition: color 0.2s;
}

.tab-btn:hover {
  color: #1890ff;
}

.tab-btn.active {
  color: #1890ff;
  font-weight: 800;
}

.tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -6px;
  left: 0;
  right: 0;
  height: 1px;
  background: #1890ff;
}

.tab-icon {
  margin-right: 4px;
  font-size: 14px;
  vertical-align: middle;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
}

.close-icon:hover {
  background: #f0f0f0;
}

.panel-body {
  flex: 1;
  overflow: hidden;
}

.tab-content {
  height: 100%;
  overflow-y: auto;
  padding: 16px;
}

.sub-tabs {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
  border-bottom: 1px solid #eee;
}
.sub-tab-btn {
  padding: 6px 4px;
  border: none;
  background: transparent;
  font-size: 13px;
  color: #666;
  cursor: pointer;
  position: relative;
}
.sub-tab-btn.active {
  color: #1890ff;
  font-weight: 600;
}
.sub-tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 1px;
  background: #1890ff;
}
.flow-run-result {
  padding: 8px 0;
}
.flow-run-result pre {
  margin: 0;
  padding: 12px;
  background: #fafafa;
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  font-size: 12px;
  font-family: monospace;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 60vh;
  overflow: auto;
}

.description-text {
  font-size: 12px;
  color: #999;
  margin-bottom: 16px;
  padding: 12px;
  background: #f8f9fa;
  border-radius: 4px;
  line-height: 1.5;
}

.add-button-container {
  margin-bottom: 16px;
}

.add-variable-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 8px 16px;
  background: #1890ff;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 13px;
  transition: background-color 0.2s;
}

.add-variable-btn:hover {
  background: #40a9ff;
}

.add-variable-btn:active {
  background: #096dd9;
}

.flow-variables-cards {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.flow-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px;
  text-align: center;
}

.flow-empty-state p {
  color: #999;
  margin: 0;
}

.flow-variable-card {
  background: #ffffff;
  border: 1px solid #e8e8e8;
  border-radius: 8px;
  overflow: hidden;
  transition: box-shadow 0.2s;
}

.flow-variable-card:hover {
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}

.flow-card-header {
  display: flex;
  align-items: center;
  padding: 7px 14px;
  cursor: pointer;
  background: #fafafa;
  border-bottom: 1px solid #e8e8e8;
  gap: 10px;
}

.flow-card-content {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
}

.flow-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  font-size: 13px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
}

.flow-type-badge.type-string {
  background: #67c23a;
  border-radius: 50%;
}

.flow-type-badge.type-number {
  background: #409eff;
  border-radius: 50%;
}

.flow-type-badge.type-boolean {
  background: #9c27b0;
  border-radius: 50%;
}

.flow-type-badge.type-object {
  background: #909399;
  border-radius: 50%;
}

.flow-type-badge.type-array-string {
  background: #f56c6c;
  border-radius: 4px;
}

.flow-type-badge.type-array-number {
  background: #e6a23c;
  border-radius: 4px;
}

.flow-type-badge.type-array-boolean {
  background: #9c27b0;
  border-radius: 4px;
}

.flow-type-badge.type-array-object {
  background: #909399;
  border-radius: 4px;
}

.flow-variable-name {
  font-size: 14px;
  font-weight: 500;
  color: #333;
}

.flow-variable-name-editable {
  cursor: text;
  padding: 2px 4px;
  border-radius: 3px;
  transition: background 0.15s;
}

.flow-variable-name-editable:hover {
  background: #e8f0fe;
}

.flow-variable-name-input {
  font-size: 14px;
  font-weight: 500;
  color: #333;
  padding: 2px 4px;
  border: 1px solid #1890ff;
  border-radius: 3px;
  outline: none;
  background: #fff;
  width: 100%;
  box-sizing: border-box;
}

.flow-card-description {
  font-size: 12px;
  color: #999;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  margin-top:6px;
}

.flow-card-description-editable {
  cursor: text;
  padding: 2px 4px;
  border-radius: 3px;
  transition: background 0.15s;
}

.flow-card-description-editable:hover {
  background: #e8f0fe;
}

.flow-card-description-input {
  font-size: 12px;
  color: #666;
  padding: 2px 4px;
  border: 1px solid #1890ff;
  border-radius: 3px;
  outline: none;
  background: #fff;
  width: 100%;
  box-sizing: border-box;
  margin-top: 6px;
}

.flow-delete-icon {
  width: 14px;
  height: 14px;
  color: #ccc;
  transition: color 0.2s;
  flex-shrink: 0;
  cursor: pointer;
}

.flow-delete-icon:hover {
  color: #f56c6c;
}

.flow-expand-icon {
  width: 16px;
  height: 16px;
  color: #999;
  transition: transform 0.2s;
  flex-shrink: 0;
  cursor: pointer;
}

.flow-expand-icon.rotated {
  transform: rotate(180deg);
}

.flow-card-body {
  padding: 14px;
  background: #ffffff;
}

.flow-form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.flow-form-item label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
}

.flow-form-input {
  padding: 8px 12px;
  border: none;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
  background: #eee;
}

.flow-form-textarea {
  padding: 8px 12px;
  border: none;
  border-radius: 4px;
  font-size: 12px;
  min-height: 80px;
  resize: vertical;
  outline: none;
  font-family: monospace;
  background: #eee;
}

.flow-boolean-selector {
  display: flex;
  gap: 8px;
}

.flow-boolean-btn {
  flex: 1;
  padding: 8px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 13px;
  background: white;
  cursor: pointer;
  transition: all 0.2s;
}

.flow-boolean-btn:hover {
  border-color: #1890ff;
}

.flow-boolean-btn.active {
  background: #1890ff;
  border-color: #1890ff;
  color: white;
}

.flow-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  padding: 8px;
  border: 1px solid #f56c6c;
  border-radius: 4px;
  font-size: 13px;
  color: #f56c6c;
  background: white;
  cursor: pointer;
  transition: all 0.2s;
}

.flow-delete-btn:hover {
  background: #fff5f5;
}

/* 环境变量样式 */
.env-variables-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.env-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px;
  text-align: center;
}

.env-empty-state p {
  color: #999;
  margin: 0;
}

.env-variable-card {
  background: #ffffff;
  border: 1px solid #e8e8e8;
  border-radius: 8px;
  overflow: hidden;
  transition: box-shadow 0.2s;
}

.env-variable-card:hover {
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}

.env-card-header {
  display: flex;
  align-items: center;
  padding: 7px 14px;
  cursor: pointer;
  background: #fafafa;
  border-bottom: 1px solid #e8e8e8;
  gap: 10px;
}

.env-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  font-size: 13px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
}

.env-type-badge.type-string {
  background: #67c23a;
  border-radius: 50%;
}

.env-type-badge.type-number {
  background: #409eff;
  border-radius: 50%;
}

.env-type-badge.type-secret {
  background: #f56c6c;
  border-radius: 50%;
}

.env-card-content {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-width: 0;
}

.env-variable-name {
  font-size: 14px;
  font-weight: 500;
  color: #333;
}

.env-variable-name-editable {
  cursor: text;
  padding: 2px 4px;
  border-radius: 3px;
  transition: background 0.15s;
}

.env-variable-name-editable:hover {
  background: #e8f0fe;
}

.env-variable-name-input {
  font-size: 14px;
  font-weight: 500;
  color: #333;
  padding: 2px 4px;
  border: 1px solid #1890ff;
  border-radius: 3px;
  outline: none;
  background: #fff;
  width: 100%;
  box-sizing: border-box;
}

.env-card-description {
  font-size: 12px;
  color: #999;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  margin-top: 6px;
}

.env-card-description-editable {
  cursor: text;
  padding: 2px 4px;
  border-radius: 3px;
  transition: background 0.15s;
}

.env-card-description-editable:hover {
  background: #e8f0fe;
}

.env-card-description-input {
  font-size: 12px;
  color: #666;
  padding: 2px 4px;
  border: 1px solid #1890ff;
  border-radius: 3px;
  outline: none;
  background: #fff;
  width: 100%;
  box-sizing: border-box;
  margin-top: 6px;
}

.env-delete-icon {
  width: 14px;
  height: 14px;
  color: #ccc;
  transition: color 0.2s;
  flex-shrink: 0;
  cursor: pointer;
}

.env-delete-icon:hover {
  color: #f56c6c;
}

.env-expand-icon {
  width: 16px;
  height: 16px;
  color: #999;
  transition: transform 0.2s;
  flex-shrink: 0;
  cursor: pointer;
}

.env-expand-icon.rotated {
  transform: rotate(180deg);
}

.env-card-body {
  padding: 14px;
  background: #ffffff;
}

.env-form-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 12px;
}

.env-form-item label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
}

.env-form-input {
  padding: 8px 12px;
  border: none;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
  background: #eee;
}

/* 全局变量下拉：外观对齐原生 .env-form-input（灰底无边框）。
   el-autocomplete 内部是 el-input，只能从容器往下覆盖。 */
.env-form-autocomplete {
  width: 100%;
  padding: 0;
  background: transparent;
}

.env-form-autocomplete :deep(.el-input__wrapper) {
  background: #eee;
  box-shadow: none;
  border-radius: 4px;
  padding: 2px 10px;
}

.env-form-autocomplete :deep(.el-input__inner) {
  font-size: 13px;
  height: 28px;
}

/* ── 全局变量（只读列表）── */
.gv-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 10px 0 8px;
}

.gv-count {
  font-size: 12px;
  color: #909399;
}

.gv-refresh-btn {
  padding: 2px 8px;
  font-size: 12px;
  color: #409eff;
  background: #fff;
  border: 1px solid #d9ecff;
  border-radius: 4px;
  cursor: pointer;
}

.gv-refresh-btn:disabled {
  color: #c0c4cc;
  border-color: #ebeef5;
  cursor: default;
}

.gv-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.gv-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
}

.gv-kind {
  flex: 0 0 auto;
  padding: 0 6px;
  font-size: 11px;
  line-height: 16px;
  color: #fff;
  background: #409eff;
  border-radius: 4px;
}

.gv-kind--secret { background: #e6a23c; }
.gv-kind--number { background: #67c23a; }
.gv-kind--bool { background: #909399; }

.gv-row-main {
  flex: 1;
  min-width: 0;
}

.gv-row-name,
.gv-row-desc {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gv-row-name {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  color: #303133;
}

.gv-row-desc {
  font-size: 11px;
  color: #909399;
}

.gv-row-value {
  flex: 0 0 auto;
  max-width: 40%;
  overflow: hidden;
  font-size: 12px;
  color: #606266;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gv-row-value.is-secret { color: #e6a23c; }

/* 提示文案：warn = 需要处理（例如已废弃的类型） */
.gv-hint {
  font-size: 12px;
  line-height: 1.5;
}

.gv-hint.warn {
  color: #e6a23c;
}

.env-form-textarea {
  padding: 8px 12px;
  border: none;
  border-radius: 4px;
  font-size: 12px;
  min-height: 80px;
  resize: vertical;
  outline: none;
  font-family: monospace;
  background: #eee;
}

.env-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  padding: 8px;
  border: 1px solid #f56c6c;
  border-radius: 4px;
  font-size: 13px;
  color: #f56c6c;
  background: white;
  cursor: pointer;
  transition: all 0.2s;
}

.env-delete-btn:hover {
  background: #fff5f5;
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

.dropdown-icon.icon-secret-inline {
  background: #f56c6c;
  border-radius: 50%;
}

/* 成员属性样式 */
.properties-container {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-bottom: 12px;
}

.property-card {
  background: #ffffff;
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  overflow: hidden;
}

.property-card-header {
  display: flex;
  align-items: center;
  padding: 8px 10px;
  background: #f1f1ff;
  gap: 8px;
}

.prop-type-badge {
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

.prop-type-badge.type-string {
  background: #67c23a;
  border-radius: 50%;
}

.prop-type-badge.type-number {
  background: #409eff;
  border-radius: 50%;
}

.prop-type-badge.type-boolean {
  background: #9c27b0;
  border-radius: 50%;
}

.property-name-input {
  flex: 1;
  padding: 4px 8px;
  border: none;
  background: transparent;
  font-size: 13px;
  font-weight: 500;
  color: #333;
  outline: none;
}

.property-card-body {
  padding: 10px;
}

.property-default-input {
  width: 100%;
}

.prop-delete-btn {
  flex: 0 0 24px;
  height: 24px;
  padding: 0;
  border: none;
  border-radius: 4px;
  background: transparent;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #999;
  transition: all 0.2s;
}

.prop-delete-btn:hover {
  color: #f56c6c;
  background: #fff5f5;
}

.add-property-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 6px 12px;
  border: 1px dashed #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #999;
  background: white;
  cursor: pointer;
  transition: all 0.2s;
}

.add-property-btn:hover {
  border-color: #1890ff;
  color: #1890ff;
}

.empty-properties {
  padding: 20px;
  text-align: center;
  color: #999;
  font-size: 12px;
}
</style>

<!-- 与 PropsPanel 同一套口径：滚动条常驻占位但透明，鼠标进面板才显形。
     变量列表与运行结果里的 textarea / pre 也走元素自身的 :hover，不进框就不出现 -->
<style>
.variables-panel * {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.variables-panel *:hover {
  scrollbar-color: #c1c1c1 transparent;
}
.variables-panel *::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.variables-panel *::-webkit-scrollbar-track {
  background: transparent;
}
.variables-panel *::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.variables-panel *:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>