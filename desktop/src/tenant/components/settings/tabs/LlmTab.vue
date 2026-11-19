<template>
  <div class="module-section">
          <div class="module-head">
            <h3>{{ t('settings.tabs.llm.title') }}</h3>
            <div class="head-actions">
              <el-button size="small" :icon="Refresh" :loading="llmProviderLoading || llmModelLoading" @click="fetchLLMProviders(); fetchLLMModels()">{{ t('settings.llm.refresh') }}</el-button>
              <el-button type="success" size="small" :loading="syncLLMLoading" @click="syncLLMModels">
                <el-icon><Refresh /></el-icon> {{ t('settings.llm.sync') }}
              </el-button>
              <el-button type="warning" size="small" :loading="deprovisionAllLoading" @click="deprovisionAllLLMModels">
                <el-icon><Remove /></el-icon> {{ t('settings.llm.deprovisionAll') }}
              </el-button>
              <el-button type="primary" size="small" @click="openAddLLMProviderDialog">
                <el-icon><Plus /></el-icon> {{ t('settings.llm.addProvider') }}
              </el-button>
            </div>
          </div>

          <div v-loading="llmProviderLoading" class="provider-list">
            <div v-for="p in llmProvidersData" :key="p.id" class="provider-card">
              <div class="provider-head">
                <div class="provider-title">
                  <span class="provider-logo">
                    <img v-if="p.icon" :src="resolveIcon(p.icon)" alt="" />
                    <el-icon v-else><ChatDotRound /></el-icon>
                  </span>
                  <span class="provider-name">{{ p.name }}</span>
                  <el-tag size="small" effect="plain" type="info">{{ p.protocol || 'openai-compatible' }}</el-tag>
                  <el-tag v-if="p.enabled === false" size="small" type="danger" effect="light">{{ t('settings.llm.providerDisabled') }}</el-tag>
                </div>
                <div class="item-actions">
                  <el-button link type="success" :icon="Plus" :title="t('settings.llm.addModel')" @click="openAddLLMModelDialog(p.id)" />
                  <el-button link type="primary" :icon="Edit" @click="openEditLLMProviderDialog(p)" />
                  <el-button link type="danger" :icon="Delete" @click="deleteLLMProvider(p)" />
                </div>
              </div>
              <div class="provider-meta">
                <span class="meta-value text-ellipsis">{{ p.url }}</span>
                <span class="secret-text">{{ maskSecret(p.api_key) }}</span>
                <el-button link size="small" :icon="CopyDocument" :title="t('settings.llm.copy')" @click="copyText(p.api_key)" />
              </div>

              <div class="model-list">
                <div v-if="modelsOf(p.id).length === 0" class="empty-inline">
                  {{ t('settings.llm.emptyModels') }}
                </div>
                <div v-for="m in modelsOf(p.id)" :key="m.id" class="model-item">
                  <div class="model-info">
                    <span class="model-name">{{ m.name }}</span>
                    <el-tag v-if="m.is_default" size="small" type="success" effect="light">{{ t('settings.llm.defaultTag') }}</el-tag>
                    <el-tag v-if="m.gateway_id" size="small" type="success" effect="light">{{ t('settings.llm.enabledTag') }}</el-tag>
                    <el-tag v-else size="small" type="info" effect="light">{{ t('settings.llm.disabledTag') }}</el-tag>
                  </div>
                  <div class="model-params">
                    {{ t('settings.llm.modelContext', { value: m.context_window || '-' }) }}
                    <template v-if="m.base_params && Object.keys(m.base_params).length">
                      <span v-if="Object.prototype.hasOwnProperty.call(m.base_params, 'max_tokens')">{{ t('settings.llm.modelMaxTokens', { value: m.base_params.max_tokens }) }}</span>
                      <span v-if="Object.prototype.hasOwnProperty.call(m.base_params, 'temperature')">{{ t('settings.llm.modelTemperature', { value: m.base_params.temperature }) }}</span>
                      <span v-if="Object.prototype.hasOwnProperty.call(m.base_params, 'top_p')">{{ t('settings.llm.modelTopP', { value: m.base_params.top_p }) }}</span>
                    </template>
                    <span v-else>{{ t('settings.llm.noGenParams') }}</span>
                    <span v-if="m.gateway_id" class="model-gateway-id">
                      {{ t('settings.llm.modelGateway', { id: m.gateway_id, model: m.gateway_model }) }}
                    </span>
                  </div>
                  <div class="item-actions">
                    <el-button
                      v-if="!m.gateway_id"
                      link type="success" size="small"
                      :loading="m._provisioning"
                      @click="provisionLLMModel(m)"
                    >{{ t('settings.llm.enable') }}</el-button>
                    <el-button
                      v-else
                      link type="warning" size="small"
                      :loading="m._deprovisioning"
                      @click="deprovisionLLMModel(m)"
                    >{{ t('settings.llm.disable') }}</el-button>
                    <el-button link type="primary" :icon="Edit" @click="openEditLLMModelDialog(m)" />
                    <el-button link type="danger" :icon="Delete" @click="deleteLLMModel(m)" />
                  </div>
                </div>
              </div>
            </div>

            <div v-if="!llmProviderLoading && llmProvidersData.length === 0" class="empty-block">
              <el-empty :description="t('settings.llm.emptyProviders')">
                <el-button type="primary" @click="openAddLLMProviderDialog">{{ t('settings.llm.addProvider') }}</el-button>
              </el-empty>
            </div>
          </div>

    <!-- 服务商 / 模型编辑面板：坐标由父级 useSidePanel 提供 -->
    <!-- LLM 服务商 -->
    <SidePanel :visible="llmProviderDialogVisible" :title="llmProviderDialogTitle"
               :panel-style="sidePanel.styleFor(820)"
               @close="llmProviderDialogVisible = false" @resize="sidePanel.startResize">
      <div class="provider-form-layout">
        <!-- 左侧：预设服务商卡片列表（仅新增时显示，编辑模式隐藏） -->
        <aside v-if="!currentLLMProvider" class="provider-preset-aside">
          <div class="provider-preset-title">{{ t('settings.llm.presetTitle') }}</div>
          <div v-loading="llmProviderPresetLoading" class="preset-grid">
            <div
              v-for="preset in llmProviderPresetsData"
              :key="preset.id"
              class="preset-card"
              :class="{ active: selectedPresetId === preset.id }"
              @click="selectLLMProviderPreset(preset)"
            >
              <div class="preset-logo">
                <img v-if="preset.icon" :src="resolveIcon(preset.icon)" alt="" />
                <el-icon v-else><ChatDotRound /></el-icon>
              </div>
              <div class="preset-info">
                <div class="preset-name">
                  {{ preset.name }}
                  <el-tag v-if="preset.is_popular" size="small" type="warning" effect="light">{{ t('settings.llm.popular') }}</el-tag>
                </div>
                <div v-if="preset.description" class="preset-desc">{{ preset.description }}</div>
                <div class="preset-tags">
                  <el-tag size="small" effect="plain" type="info">{{ preset.protocol }}</el-tag>
                </div>
              </div>
            </div>
            <div v-if="!llmProviderPresetLoading && llmProviderPresetsData.length === 0" class="preset-empty">
              {{ t('settings.llm.presetEmpty') }}
            </div>
          </div>
        </aside>
        <!-- 右侧：服务商表单 -->
        <div class="provider-form-main">
          <el-form label-position="top">
            <el-form-item :label="t('settings.llm.providerName')" required>
              <el-input v-model="llmProviderSettings.sourceName" :placeholder="t('settings.llm.providerNamePlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('settings.llm.apiUrl')" required>
              <el-input v-model="llmProviderSettings.url" :placeholder="t('settings.llm.apiUrlPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('settings.llm.apiKey')" required>
              <el-input v-model="llmProviderSettings.apiKey" :placeholder="t('settings.llm.apiKeyPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('settings.llm.protocol')" required>
              <el-input v-model="llmProviderSettings.protocol" :placeholder="t('settings.llm.protocol')" disabled />
            </el-form-item>
            <el-form-item :label="t('settings.llm.icon')">
              <el-input v-model="llmProviderSettings.icon" :placeholder="t('settings.llm.iconPlaceholder')" />
            </el-form-item>
            <el-form-item :label="t('settings.llm.enabled')">
              <el-switch v-model="llmProviderSettings.enabled" />
            </el-form-item>
            <el-form-item :label="t('settings.llm.extraConfig')">
              <el-input type="textarea" v-model="llmProviderSettings.extraConfigText"
                        :placeholder="t('settings.llm.extraConfigPlaceholder', { example: jsonExampleRegion })"
                        :rows="4" />
            </el-form-item>
          </el-form>
        </div>
      </div>
      <template #footer>
        <el-button @click="llmProviderDialogVisible = false">{{ t('settings.llm.cancel') }}</el-button>
        <el-button type="primary" @click="saveLLMProviderSettings">{{ t('settings.llm.save') }}</el-button>
      </template>
    </SidePanel>

    <!-- LLM 模型 -->
    <SidePanel :visible="llmModelDialogVisible" :title="llmModelDialogTitle"
               :panel-style="sidePanel.styleFor(500)"
               @close="llmModelDialogVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item :label="t('settings.llm.modelName')" required>
          <el-input v-model="llmModelSettings.name" :placeholder="t('settings.llm.modelNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('settings.llm.contextWindow')">
          <el-input-number v-model="llmModelSettings.contextWindow" :placeholder="t('settings.llm.contextWindowPlaceholder')" />
        </el-form-item>
        <!-- 基础生成参数：开关 + 值，仅开启的参数会写入 base_params（键存在=使用，键不存在=未使用） -->
        <div class="gen-param-row">
          <el-switch v-model="llmGenParams.maxTokens.enabled" />
          <span class="gen-param-label">{{ t('settings.llm.maxTokensLabel') }}</span>
          <el-input-number v-model="llmGenParams.maxTokens.value" :disabled="!llmGenParams.maxTokens.enabled"
                           :placeholder="t('settings.llm.maxTokensPlaceholder')" :min="0" />
        </div>
        <div class="gen-param-row">
          <el-switch v-model="llmGenParams.temperature.enabled" />
          <span class="gen-param-label">{{ t('settings.llm.temperatureLabel') }}</span>
          <el-input-number v-model="llmGenParams.temperature.value" :disabled="!llmGenParams.temperature.enabled"
                           :min="0" :max="1" :step="0.1" :placeholder="t('settings.llm.temperaturePlaceholder')" />
        </div>
        <div class="gen-param-row">
          <el-switch v-model="llmGenParams.topP.enabled" />
          <span class="gen-param-label">{{ t('settings.llm.topPLabel') }}</span>
          <el-input-number v-model="llmGenParams.topP.value" :disabled="!llmGenParams.topP.enabled"
                           :min="0" :max="1" :step="0.1" :placeholder="t('settings.llm.topPPlaceholder')" />
        </div>
        <el-form-item :label="t('settings.llm.providerLabel')" required>
          <el-input :model-value="getProviderName(llmModelSettings.providerId)"
                    :placeholder="t('settings.llm.providerPlaceholder')" disabled />
        </el-form-item>
        <el-form-item v-if="llmModelSettings.gatewayId" :label="t('settings.llm.gatewayModelId')">
          <el-input :model-value="llmModelSettings.gatewayId" disabled />
        </el-form-item>
        <el-form-item :label="t('settings.llm.gatewayModelName')" :disabled="!!llmModelSettings.gatewayId">
          <el-input
            v-model="llmModelSettings.gatewayModel"
            :placeholder="t('settings.llm.gatewayModelPlaceholder')"
            :disabled="!!llmModelSettings.gatewayId"
          />
          <template v-if="llmModelSettings.gatewayId" #help>
            {{ t('settings.llm.gatewayModelLocked') }}
          </template>
        </el-form-item>
        <div class="form-row-2">
          <el-form-item :label="t('settings.llm.setDefault')">
            <el-switch v-model="llmModelSettings.isDefault" />
          </el-form-item>
        </div>
        <el-form-item :label="t('settings.llm.extraParams')">
          <el-input type="textarea" v-model="llmModelSettings.extraParamsText"
                    :placeholder="t('settings.llm.extraParamsPlaceholder', { example: jsonExampleThinking })"
                    :rows="4" />
        </el-form-item>
        <el-form-item :label="t('settings.llm.capabilities')">
          <el-input type="textarea" v-model="llmModelSettings.capabilitiesText"
                    :placeholder="t('settings.llm.capabilitiesPlaceholder', { example: jsonExampleVision })"
                    :rows="4" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="llmModelDialogVisible = false">{{ t('settings.llm.cancel') }}</el-button>
        <el-button type="primary" @click="saveLLMModelSettings">{{ t('settings.llm.save') }}</el-button>
      </template>
    </SidePanel>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Edit, Delete, CopyDocument, Plus, Refresh, Remove, ChatDotRound } from '@element-plus/icons-vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import SidePanel from '../SidePanel.vue'
import { useSidePanelContext } from '../useSidePanel.js'
import { maskSecret, copyText } from '../format.js'
import { formatError } from '../../../../locales'

const props = defineProps({ active: { type: Boolean, default: false } })

const { t } = useI18n()

// JSON 示例带双引号与花括号：既不能直接写进模板属性（引号会截断属性），
// 也不能直接写进语言包（{...} 会被 vue-i18n 当命名参数），所以整段当参数传。
const jsonExampleRegion = '{"region":"us-east-1"}'
const jsonExampleThinking = '{"enable_thinking": true}'
const jsonExampleVision = '{"vision":true}'

// 左侧导航显示「N 服务商 · M 模型」，由本组件加载后回报
const emit = defineEmits(['count'])
const reportCount = () => {
  emit('count', {
    providers: llmProvidersData.value.length,
    models: llmModelsData.value.length
  })
}

// LLM服务商数据
const llmProvidersData = ref([])
const llmProviderLoading = ref(false)

// LLM服务商设置
const llmProviderSettings = ref({
  url: 'https://api.openai.com',
  sourceName: t('settings.llm.defaultProviderName'),
  apiKey: '',
  protocol: 'openai-compatible',
  icon: '',
  enabled: true,
  extraConfigText: ''
})

// LLM服务商对话框相关
const llmProviderDialogVisible = ref(false)
const currentLLMProvider = ref(null)
// 标题跟着 current 判断（有值 = 编辑）：做成 computed，切语言时才跟着变
const llmProviderDialogTitle = computed(() =>
  currentLLMProvider.value ? t('settings.llm.dialogEditProvider') : t('settings.llm.dialogAddProvider'))

// LLM服务商预设数据
const llmProviderPresetsData = ref([])
const llmProviderPresetLoading = ref(false)
const selectedPresetId = ref('')
// 标记预设是否已加载，避免重复请求
let llmProviderPresetsLoaded = false

// LLM模型数据
const llmModelsData = ref([])
const llmModelLoading = ref(false)
const deprovisionAllLoading = ref(false)
const syncLLMLoading = ref(false)

// LLM模型设置
const llmModelSettings = ref({
  name: '',
  contextWindow: 4096,
  // 基础生成参数（纯属性 map，键存在=使用，键不存在=未使用；无多余包装）
  baseParams: {},
  providerId: '',
  gatewayId: '',
  gatewayModel: '',
  isDefault: false,
  extraParamsText: '',
  capabilitiesText: ''
})

// 生成参数的 UI 态：开关 + 值。仅开启的参数在保存时写入 base_params
const llmGenParams = ref({
  maxTokens:   { enabled: false, value: 1024 },
  temperature: { enabled: false, value: 0.7 },
  topP:        { enabled: false, value: 0.9 }
})

// 从后端下发的 base_params（纯属性）回填到 UI 态
function fillGenParamsFromBase(baseParams = {}) {
  const bp = baseParams || {}
  llmGenParams.value.maxTokens.enabled   = Object.prototype.hasOwnProperty.call(bp, 'max_tokens')
  llmGenParams.value.maxTokens.value     = bp.max_tokens   ?? 1024
  llmGenParams.value.temperature.enabled = Object.prototype.hasOwnProperty.call(bp, 'temperature')
  llmGenParams.value.temperature.value   = bp.temperature ?? 0.7
  llmGenParams.value.topP.enabled        = Object.prototype.hasOwnProperty.call(bp, 'top_p')
  llmGenParams.value.topP.value          = bp.top_p        ?? 0.9
}

// 从 UI 态构建纯属性 base_params（只写开启的键）
function buildBaseParams() {
  const bp = {}
  if (llmGenParams.value.maxTokens.enabled)   bp.max_tokens   = llmGenParams.value.maxTokens.value
  if (llmGenParams.value.temperature.enabled) bp.temperature = llmGenParams.value.temperature.value
  if (llmGenParams.value.topP.enabled)        bp.top_p       = llmGenParams.value.topP.value
  return bp
}

// LLM模型对话框相关
const llmModelDialogVisible = ref(false)
const currentLLMModel = ref(null)
const llmModelDialogTitle = computed(() =>
  currentLLMModel.value ? t('settings.llm.dialogEditModel') : t('settings.llm.dialogAddModel'))

const sidePanel = useSidePanelContext()
sidePanel.register(llmProviderDialogVisible)
sidePanel.register(llmModelDialogVisible)

// 工具：解析 icon 路径，相对路径拼接 apiBase（dev 下走 /nexus 代理到后端 /static），已是完整 URL 直接返回
const resolveIcon = (icon) => {
  if (!icon) return ''
  if (/^(https?:)?\/\//.test(icon)) return icon
  // icon 形如 /static/llm/openai.png
  const base = apiConfig.baseUrl || ''
  return base.replace(/\/$/, '') + (icon.startsWith('/') ? icon : '/' + icon)
}

// 工具：检查后端响应是否业务成功
// 后端业务错误虽然 HTTP 状态码为 200，但 body.code 为 400
const checkBizOk = async (response) => {
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`)
  }
  const data = await response.json()
  if (data && typeof data.code !== 'undefined' && data.code !== 200) {
    throw new Error(formatError(data) || t('settings.llm.opFailed'))
  }
  return data
}


// 工具：根据服务商 ID 取模型列表
const modelsOf = (providerId) => {
  if (!providerId) return []
  return llmModelsData.value.filter(m => m.provider_id === providerId)
}

// 获取LLM服务商列表
const fetchLLMProviders = async () => {
  llmProviderLoading.value = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmProviders.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        page: 1,
        limit: -1,
        sort_by: ['name'],
        sort_order: ['desc']
      })
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const data = await response.json()
    llmProvidersData.value = data?.data?.items ?? []
    reportCount()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.loadProvidersFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('load llm providers failed:', error)
  } finally {
    llmProviderLoading.value = false
  }
}

// 获取LLM服务商预设列表
const fetchLLMProviderPresets = async () => {
  llmProviderPresetLoading.value = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmProviderPresets.list, {
      method: 'GET',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const data = await response.json()
    llmProviderPresetsData.value = data?.data?.items ?? []
    llmProviderPresetsLoaded = true
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.loadPresetsFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('load llm provider presets failed:', error)
  } finally {
    llmProviderPresetLoading.value = false
  }
}

// 选择预设服务商，自动填充表单
const selectLLMProviderPreset = (preset) => {
  selectedPresetId.value = preset.id
  llmProviderSettings.value.sourceName = preset.name || ''
  llmProviderSettings.value.protocol = preset.protocol || 'openai-compatible'
  llmProviderSettings.value.url = preset.url || ''
  llmProviderSettings.value.icon = preset.icon || ''
}

// 对话框打开时回调：仅新增模式下加载预设
const onLLMProviderDialogOpen = () => {
  selectedPresetId.value = ''
  if (!currentLLMProvider.value && !llmProviderPresetsLoaded) {
    fetchLLMProviderPresets()
  }
}

// 打开添加LLM服务商对话框
const openAddLLMProviderDialog = () => {
  currentLLMProvider.value = null
  llmProviderSettings.value = {
    url: 'https://api.openai.com',
    sourceName: '',
    apiKey: '',
    protocol: 'openai-compatible',
    icon: '',
    enabled: true,
    extraConfigText: ''
  }
  llmProviderDialogVisible.value = true
}

// 打开编辑LLM服务商对话框
const openEditLLMProviderDialog = (source) => {
  currentLLMProvider.value = source
  llmProviderSettings.value = {
    url: source.url,
    sourceName: source.name,
    apiKey: source.api_key,
    protocol: source.protocol || 'openai-compatible',
    icon: source.icon || '',
    enabled: source.enabled !== false,
    extraConfigText: source.extra_config ? JSON.stringify(source.extra_config, null, 2) : ''
  }
  llmProviderDialogVisible.value = true
}

// 保存LLM服务商
const saveLLMProviderSettings = async () => {
  if (!llmProviderSettings.value.sourceName || !llmProviderSettings.value.url || !llmProviderSettings.value.apiKey) {
    ElMessage.warning(t('settings.llm.needAllFields'))
    return
  }

  let extraConfig = {}
  if (llmProviderSettings.value.extraConfigText) {
    try {
      extraConfig = JSON.parse(llmProviderSettings.value.extraConfigText)
    } catch (e) {
      ElMessage.warning(t('settings.llm.extraConfigInvalid'))
      return
    }
  }

  const sourceData = {
    name: llmProviderSettings.value.sourceName,
    url: llmProviderSettings.value.url,
    api_key: llmProviderSettings.value.apiKey,
    protocol: llmProviderSettings.value.protocol,
    icon: llmProviderSettings.value.icon,
    enabled: llmProviderSettings.value.enabled,
    extra_config: extraConfig
  }

  llmProviderLoading.value = true
  try {
    const token = getToken()
    let response
    if (currentLLMProvider.value) {
      // 编辑现有LLM服务商
      response = await fetch(`${apiConfig.admin.llmProviders.base}${currentLLMProvider.value.id}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(sourceData)
      })
    } else {
      // 添加新LLM服务商
      response = await fetch(`${apiConfig.admin.llmProviders.base}create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(sourceData)
      })
    }

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    await checkBizOk(response)
    ElMessage.success({
      message: currentLLMProvider.value ? t('settings.llm.providerUpdated') : t('settings.llm.providerAdded'),
      duration: 700
    })
    llmProviderDialogVisible.value = false
    // 重新获取LLM服务商列表
    await fetchLLMProviders()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.saveProviderFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('save llm provider failed:', error)
  } finally {
    llmProviderLoading.value = false
  }
}

// 删除LLM服务商
const deleteLLMProvider = async (source) => {
  try {
    await ElMessageBox.confirm(
      t('settings.llm.deleteProviderConfirm'),
      t('settings.llm.tipTitle'),
      {
        confirmButtonText: t('settings.llm.delete'),
        cancelButtonText: t('settings.llm.cancel'),
        type: 'warning'
      }
    )
  } catch {
    return
  }
  llmProviderLoading.value = true
  try {
    const token = getToken()

    // 1. 删除LLM服务商
    const response = await fetch(`${apiConfig.admin.llmProviders.base}${source.id}`, {
      method: 'DELETE',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    ElMessage.success({
      message: t('settings.llm.providerDeleted'),
      duration: 700
    })
    // 重新获取LLM服务商列表
    await fetchLLMProviders()
    // 重新获取LLM模型列表
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.deleteProviderFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('delete llm provider failed:', error)
  } finally {
    llmProviderLoading.value = false
  }
}

// 获取LLM模型列表
const fetchLLMModels = async () => {
  llmModelLoading.value = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmModels.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        page: 1,
        limit: -1,
        sort_by: ['name'],
        sort_order: ['desc']
      })
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const data = await response.json()
    llmModelsData.value = data?.data?.items ?? []
    reportCount()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.loadModelsFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('load llm models failed:', error)
  } finally {
    llmModelLoading.value = false
  }
}

// 打开添加LLM模型对话框（可传入服务商 ID 自动带入）
const openAddLLMModelDialog = (providerId) => {
  currentLLMModel.value = null
  llmModelSettings.value = {
    name: '',
    contextWindow: 4096,
    baseParams: {},
    providerId: providerId || '',
    gatewayId: '',
    gatewayModel: '',
    isDefault: false,
    extraParamsText: '',
    capabilitiesText: ''
  }
  fillGenParamsFromBase({})
  llmModelDialogVisible.value = true
}

// 打开编辑LLM模型对话框
const openEditLLMModelDialog = (model) => {
  currentLLMModel.value = model
  llmModelSettings.value = {
    name: model.name,
    contextWindow: model.context_window || 4096,
    baseParams: model.base_params || {},
    providerId: model.provider_id,
    gatewayId: model.gateway_id || '',
    gatewayModel: model.gateway_model || '',
    isDefault: model.is_default || false,
    extraParamsText: model.extra_params ? JSON.stringify(model.extra_params, null, 2) : '',
    capabilitiesText: model.capabilities ? JSON.stringify(model.capabilities, null, 2) : ''
  }
  fillGenParamsFromBase(model.base_params)
  llmModelDialogVisible.value = true
}

// 保存LLM模型
const saveLLMModelSettings = async () => {
  if (!llmModelSettings.value.name || !llmModelSettings.value.providerId) {
    ElMessage.warning(t('settings.llm.needAllFields'))
    return
  }

  let extraParams = {}
  if (llmModelSettings.value.extraParamsText) {
    try {
      extraParams = JSON.parse(llmModelSettings.value.extraParamsText)
    } catch (e) {
      ElMessage.warning(t('settings.llm.extraParamsInvalid'))
      return
    }
  }

  let capabilities = {}
  if (llmModelSettings.value.capabilitiesText) {
    try {
      capabilities = JSON.parse(llmModelSettings.value.capabilitiesText)
    } catch (e) {
      ElMessage.warning(t('settings.llm.capabilitiesInvalid'))
      return
    }
  }

  const modelData = {
    name: llmModelSettings.value.name,
    context_window: llmModelSettings.value.contextWindow,
    base_params: buildBaseParams(),
    provider_id: llmModelSettings.value.providerId,
    gateway_model: llmModelSettings.value.gatewayModel,
    is_default: llmModelSettings.value.isDefault,
    extra_params: extraParams,
    capabilities: capabilities
  }

  llmModelLoading.value = true
  try {
    const token = getToken()
    let response
    if (currentLLMModel.value) {
      // 编辑现有LLM模型
      response = await fetch(`${apiConfig.admin.llmModels.base}${currentLLMModel.value.id}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(modelData)
      })
    } else {
      // 添加新LLM模型
      response = await fetch(`${apiConfig.admin.llmModels.base}create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(modelData)
      })
    }

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    await checkBizOk(response)
    ElMessage.success({
      message: currentLLMModel.value ? t('settings.llm.modelUpdated') : t('settings.llm.modelAdded'),
      duration: 700
    })
    llmModelDialogVisible.value = false
    // 重新获取LLM模型列表
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.saveModelFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('save llm model failed:', error)
  } finally {
    llmModelLoading.value = false
  }
}

// 删除LLM模型
const deleteLLMModel = async (model) => {
  try {
    await ElMessageBox.confirm(t('settings.llm.deleteModelConfirm'), t('settings.llm.tipTitle'), {
      confirmButtonText: t('settings.llm.delete'),
      cancelButtonText: t('settings.llm.cancel'),
      type: 'warning'
    })
  } catch {
    return
  }
  llmModelLoading.value = true
  try {
    const token = getToken()
    const response = await fetch(`${apiConfig.admin.llmModels.base}${model.id}`, {
      method: 'DELETE',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    ElMessage.success({
      message: t('settings.llm.modelDeleted'),
      duration: 700
    })
    // 重新获取LLM模型列表
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.deleteModelFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('delete llm model failed:', error)
  } finally {
    llmModelLoading.value = false
  }
}

// 启用 LLM 模型（Provision：向 LiteLLM 网关注册并回写 gateway_id）
const provisionLLMModel = async (model) => {
  model._provisioning = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmModels.provision(model.id), {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const data = await checkBizOk(response)
    ElMessage.success({
      message: data?.data?.gateway_id
        ? t('settings.llm.provisionSucceededWithGateway', { name: model.name, id: data.data.gateway_id })
        : t('settings.llm.provisionSucceeded', { name: model.name }),
      duration: 1500
    })
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.provisionModelFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('provision llm model failed:', error)
  } finally {
    model._provisioning = false
  }
}

// 禁用 LLM 模型（Deprovision：从 LiteLLM 网关删除并清空 gateway_id）
const deprovisionLLMModel = async (model) => {
  try {
    await ElMessageBox.confirm(t('settings.llm.deprovisionConfirm', { name: model.name }), t('settings.llm.tipTitle'), {
      confirmButtonText: t('settings.llm.disable'),
      cancelButtonText: t('settings.llm.cancel'),
      type: 'warning'
    })
  } catch {
    return
  }
  model._deprovisioning = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmModels.deprovision(model.id), {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    await checkBizOk(response)
    ElMessage.success({
      message: t('settings.llm.deprovisionSucceeded', { name: model.name }),
      duration: 1500
    })
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.deprovisionModelFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('deprovision llm model failed:', error)
  } finally {
    model._deprovisioning = false
  }
}

// 全部注销（DeprovisionAll：从 LiteLLM 网关批量删除本租户所有模型并清空 gateway_id）
const deprovisionAllLLMModels = async () => {
  try {
    await ElMessageBox.confirm(
      t('settings.llm.deprovisionAllConfirm'),
      t('settings.llm.deprovisionAll'),
      {
        confirmButtonText: t('settings.llm.deprovisionAll'),
        cancelButtonText: t('settings.llm.cancel'),
        type: 'warning'
      }
    )
  } catch {
    return
  }
  deprovisionAllLoading.value = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmModels.deprovisionAll, {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const data = await checkBizOk(response)
    const count = data?.data?.count ?? 0
    ElMessage.success({
      message: t('settings.llm.deprovisionAllSucceeded', { n: count }),
      duration: 1500
    })
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.deprovisionAllFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('deprovision all llm models failed:', error)
  } finally {
    deprovisionAllLoading.value = false
  }
}

// 同步：逐个检查已启用模型，把本地配置对齐到 LiteLLM，并清理网关多余记录
const syncLLMModels = async () => {
  try {
    await ElMessageBox.confirm(
      t('settings.llm.syncConfirm'),
      t('settings.llm.syncTitle'),
      {
        confirmButtonText: t('settings.llm.sync'),
        cancelButtonText: t('settings.llm.cancel'),
        type: 'warning'
      }
    )
  } catch {
    return
  }
  syncLLMLoading.value = true
  try {
    const token = getToken()
    const response = await fetch(apiConfig.admin.llmModels.sync, {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    const data = await checkBizOk(response)
    const r = data?.data || {}
    const skipped = r.skipped || []
    const errors = r.errors || []
    const summary = t('settings.llm.syncSummary', {
      created: r.created || 0,
      updated: r.updated || 0,
      unchanged: r.unchanged || 0,
      deleted: r.deleted || 0
    })

    if (errors.length) {
      // 有失败项时用弹窗展示明细；动态内容先转义，避免模型名里的尖括号被当 HTML
      const esc = (s) => String(s).replace(/[&<>"']/g, (ch) => (
        { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]
      ))
      const parts = [`<b>${esc(summary)}</b>`]
      if (skipped.length) {
        parts.push(t('settings.llm.syncSkippedDetail', { n: skipped.length }))
        skipped.forEach((s) => parts.push(t('settings.llm.syncSkippedItem', {
          model: esc(s.model), reason: esc(s.reason)
        })))
      }
      parts.push(t('settings.llm.syncErrorDetail', { n: errors.length }))
      errors.forEach((e) => parts.push(t('settings.llm.syncErrorItem', {
        model: esc(e.model), error: esc(e.error)
      })))
      ElMessageBox.alert(parts.join(''), t('settings.llm.syncDoneWithErrors'), {
        confirmButtonText: t('settings.llm.gotIt'),
        type: 'warning',
        dangerouslyUseHTMLString: true
      }).catch(() => {})
    } else if (skipped.length) {
      ElMessage.success({
        message: t('settings.llm.syncSummaryWithSkipped', { summary, n: skipped.length }),
        duration: 3000
      })
    } else {
      ElMessage.success({
        message: summary,
        duration: 2500
      })
    }
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: t('settings.llm.syncFailed', { msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('sync llm models failed:', error)
  } finally {
    syncLLMLoading.value = false
  }
}

// 根据服务商ID获取服务商名称
const getProviderName = (providerId) => {
  if (!providerId) return ''
  const provider = llmProvidersData.value.find(p => p.id === providerId)
  return provider ? provider.name : providerId
}

// 服务商面板打开时：重置预设选择并按需加载预设
watch(llmProviderDialogVisible, (visible) => {
  if (visible) onLLMProviderDialogOpen()
})

// 切到本 tab 时才拉列表（放在所有 fetch 声明之后：immediate 会在 setup 里同步调用它们，
// 提前到函数定义之前会撞 const 的 TDZ —— 报「Cannot access 'fetchLLMProviders' before initialization」）
watch(() => props.active, (active) => {
  if (!active) return
  fetchLLMProviders()
  fetchLLMModels()
}, { immediate: true })
</script>

<style scoped>
/* 外壳（左侧导航 / 搜索框）的样式留在 Settings.vue；这里只管 LLM 页自己的内容 */
.module-section {
  background: #fff;
  border: 1px solid #e0e0e0;
  border-radius: 12px;
  padding: 20px 24px 24px;
}

.module-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 18px;
}

.module-head h3 {
  font-size: 16px;
  font-weight: 500;
  color: #202124;
  margin: 0;
}

/* 头部操作区：flex 居中对齐（避免 radio-group 与按钮按行内基线错位），
   间距由 gap 控制，清掉 EP 相邻按钮自带的 12px 左间距。 */
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.head-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}


.text-ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* LLM 服务商卡片右上角的操作区（连接的卡片已搬到 ConnectionTab，那里有一份同名的） */
.item-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}

.meta-value {
  min-width: 0;
}

/* 服务商卡片 */
.provider-list {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.provider-card {
  border: 1px solid #ebeef2;
  border-radius: 10px;
  padding: 16px 18px;
  transition: box-shadow 0.2s ease;
}

.provider-card:hover {
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.05);
}

.provider-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}

.provider-title {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.provider-logo {
  width: 32px;
  height: 32px;
  border-radius: 6px;
  overflow: hidden;
  flex-shrink: 0;
  background: #f0f2f5;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}

.provider-logo img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.provider-logo .el-icon {
  font-size: 20px;
  color: #409eff;
}

.provider-name {
  font-size: 15px;
  font-weight: 600;
  color: #1f2329;
}

.provider-meta {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 10px;
  padding-left: 42px;
  font-size: 13px;
  color: #5a6068;
  flex-wrap: wrap;
}

.provider-meta .meta-value {
  max-width: 360px;
}

/* 模型列表 */
.model-list {
  margin-top: 14px;
  border-top: 1px dashed #eceef1;
  padding-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.model-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  background: #fafbfc;
  border: 1px solid #f0f2f5;
  border-radius: 8px;
  padding: 10px 12px;
}

.model-info {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.model-name {
  font-size: 14px;
  font-weight: 600;
  color: #2b2f36;
}

.model-params {
  flex: 1;
  font-size: 12px;
  color: #9aa0a8;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty-inline {
  font-size: 13px;
  color: #b0b5bc;
  padding: 8px 4px;
}

.empty-block {
  grid-column: 1 / -1;
  padding: 20px 0;
}

/* LLM 服务商预设卡片 */
.preset-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
  width: 100%;
  max-height: 280px;
  overflow-y: auto;
  padding: 2px;
}
.preset-card {
  display: flex;
  align-items: center;
  gap: 10px;
  border: 1px solid #e8eaed;
  border-radius: 8px;
  padding: 10px 12px;
  cursor: pointer;
  transition: all 0.2s ease;
  background: #fff;
}
.preset-card:hover {
  border-color: #409eff;
  box-shadow: 0 2px 8px rgba(64, 158, 255, 0.1);
}
.preset-card.active {
  border-color: #409eff;
  background: #ecf5ff;
  box-shadow: 0 2px 8px rgba(64, 158, 255, 0.18);
}
.preset-logo {
  width: 36px;
  height: 36px;
  border-radius: 6px;
  overflow: hidden;
  flex-shrink: 0;
  background: #f0f2f5;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.preset-logo img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.preset-logo .el-icon {
  font-size: 20px;
  color: #409eff;
}
.preset-info {
  min-width: 0;
  flex: 1;
}
.preset-name {
  font-size: 14px;
  font-weight: 600;
  color: #1f2329;
  display: flex;
  align-items: center;
  gap: 6px;
}
.preset-desc {
  font-size: 12px;
  color: #9aa0a8;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.preset-tags {
  margin-top: 4px;
  display: flex;
  gap: 4px;
}
.preset-empty {
  grid-column: 1 / -1;
  text-align: center;
  font-size: 13px;
  color: #b0b5bc;
  padding: 20px 0;
}

/* 添加 LLM 服务商：左（预设卡片列表）+ 右（表单）两栏。
   height: 100% 让两栏撑满 panel-body —— 左栏上下充满，
   列表超出时在左栏内部滚动，右栏表单过高时右栏自己滚（不牵动左栏）。 */
.provider-form-layout {
  display: flex;
  align-items: stretch;
  gap: 16px;
  height: 100%;
}
.provider-preset-aside {
  width: 250px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
}
.provider-preset-title {
  font-size: 13px;
  font-weight: 600;
  color: #606266;
  line-height: 1.4;
  flex-shrink: 0;
}
/* 左栏内预设卡片竖排单列，占满左栏剩余高度并自身滚动
   （覆盖默认 auto-fill 多列网格；align-content: start 避免卡片被拉伸变形） */
.provider-preset-aside .preset-grid {
  grid-template-columns: 1fr;
  flex: 1;
  max-height: none;
  align-content: start;
}
.provider-form-main {
  flex: 1;
  min-width: 0;
}
/* 左右两栏共用：预留垂直滚动条位置、不显示横向滚动条；滑块默认透明，
   鼠标进入面板时才显示（压到滑块上再加深） */
.provider-preset-aside .preset-grid,
.provider-form-main {
  min-height: 0;
  overflow-y: auto;
  overflow-x: hidden;
  scrollbar-gutter: stable;
  scrollbar-width: thin;                    /* Firefox */
  scrollbar-color: transparent transparent; /* Firefox：默认透明 */
}
.provider-preset-aside .preset-grid::-webkit-scrollbar,
.provider-form-main::-webkit-scrollbar {
  width: 8px;
}
.provider-preset-aside .preset-grid::-webkit-scrollbar-track,
.provider-form-main::-webkit-scrollbar-track {
  background: transparent;
}
.provider-preset-aside .preset-grid::-webkit-scrollbar-thumb,
.provider-form-main::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.settings-panel:hover .provider-preset-aside .preset-grid::-webkit-scrollbar-thumb,
.settings-panel:hover .provider-form-main::-webkit-scrollbar-thumb {
  background: rgba(0, 0, 0, 0.2);
}
.settings-panel:hover .provider-preset-aside .preset-grid::-webkit-scrollbar-thumb:hover,
.settings-panel:hover .provider-form-main::-webkit-scrollbar-thumb:hover {
  background: rgba(0, 0, 0, 0.35);
}
.settings-panel:hover .provider-preset-aside .preset-grid,
.settings-panel:hover .provider-form-main {
  scrollbar-color: rgba(0, 0, 0, 0.2) transparent; /* Firefox */
}

/* 表单项横排 */
.form-row-2 {
  display: flex;
  gap: 16px;
}
.form-row-2 > .el-form-item {
  flex: 1;
  min-width: 0;
}

/* 生成参数：开关 + 标签 + 值 横排 */
.gen-param-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 18px;
}
.gen-param-row .gen-param-label {
  font-size: 14px;
  color: #606266;
  min-width: 64px;
}
.gen-param-row .el-input-number {
  flex: 1;
}

</style>
