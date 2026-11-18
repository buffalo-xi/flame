<template>
  <div class="module-section">
          <div class="module-head">
            <h3>LLM 配置</h3>
            <div class="head-actions">
              <el-button size="small" :icon="Refresh" :loading="llmProviderLoading || llmModelLoading" @click="fetchLLMProviders(); fetchLLMModels()">刷新</el-button>
              <el-button type="success" size="small" :loading="syncLLMLoading" @click="syncLLMModels">
                <el-icon><Refresh /></el-icon> 同步
              </el-button>
              <el-button type="warning" size="small" :loading="deprovisionAllLoading" @click="deprovisionAllLLMModels">
                <el-icon><Remove /></el-icon> 全部注销
              </el-button>
              <el-button type="primary" size="small" @click="openAddLLMProviderDialog">
                <el-icon><Plus /></el-icon> 添加服务商
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
                  <el-tag v-if="p.enabled === false" size="small" type="danger" effect="light">已停用</el-tag>
                </div>
                <div class="item-actions">
                  <el-button link type="success" :icon="Plus" title="添加模型" @click="openAddLLMModelDialog(p.id)" />
                  <el-button link type="primary" :icon="Edit" @click="openEditLLMProviderDialog(p)" />
                  <el-button link type="danger" :icon="Delete" @click="deleteLLMProvider(p)" />
                </div>
              </div>
              <div class="provider-meta">
                <span class="meta-value text-ellipsis">{{ p.url }}</span>
                <span class="secret-text">{{ maskSecret(p.api_key) }}</span>
                <el-button link size="small" :icon="CopyDocument" title="复制" @click="copyText(p.api_key)" />
              </div>

              <div class="model-list">
                <div v-if="modelsOf(p.id).length === 0" class="empty-inline">
                  该服务商下暂无模型，点击「添加模型」创建
                </div>
                <div v-for="m in modelsOf(p.id)" :key="m.id" class="model-item">
                  <div class="model-info">
                    <span class="model-name">{{ m.name }}</span>
                    <el-tag v-if="m.is_default" size="small" type="success" effect="light">默认</el-tag>
                    <el-tag v-if="m.gateway_id" size="small" type="success" effect="light">已启用</el-tag>
                    <el-tag v-else size="small" type="info" effect="light">未启用</el-tag>
                  </div>
                  <div class="model-params">
                    上下文 {{ m.context_window || '-' }}
                    <template v-if="m.base_params && Object.keys(m.base_params).length">
                      <span v-if="Object.prototype.hasOwnProperty.call(m.base_params, 'max_tokens')"> · Max Tokens {{ m.base_params.max_tokens }}</span>
                      <span v-if="Object.prototype.hasOwnProperty.call(m.base_params, 'temperature')"> · 温度 {{ m.base_params.temperature }}</span>
                      <span v-if="Object.prototype.hasOwnProperty.call(m.base_params, 'top_p')"> · TopP {{ m.base_params.top_p }}</span>
                    </template>
                    <span v-else> · 生成参数未设置</span>
                    <span v-if="m.gateway_id" class="model-gateway-id"> · 网关ID {{ m.gateway_id }} · 网关模型 {{ m.gateway_model }}</span>
                  </div>
                  <div class="item-actions">
                    <el-button
                      v-if="!m.gateway_id"
                      link type="success" size="small"
                      :loading="m._provisioning"
                      @click="provisionLLMModel(m)"
                    >启用</el-button>
                    <el-button
                      v-else
                      link type="warning" size="small"
                      :loading="m._deprovisioning"
                      @click="deprovisionLLMModel(m)"
                    >禁用</el-button>
                    <el-button link type="primary" :icon="Edit" @click="openEditLLMModelDialog(m)" />
                    <el-button link type="danger" :icon="Delete" @click="deleteLLMModel(m)" />
                  </div>
                </div>
              </div>
            </div>

            <div v-if="!llmProviderLoading && llmProvidersData.length === 0" class="empty-block">
              <el-empty description="暂无 LLM 服务商">
                <el-button type="primary" @click="openAddLLMProviderDialog">添加服务商</el-button>
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
          <div class="provider-preset-title">选择预设服务商（可选，点击后自动填充）</div>
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
                  <el-tag v-if="preset.is_popular" size="small" type="warning" effect="light">热门</el-tag>
                </div>
                <div v-if="preset.description" class="preset-desc">{{ preset.description }}</div>
                <div class="preset-tags">
                  <el-tag size="small" effect="plain" type="info">{{ preset.protocol }}</el-tag>
                </div>
              </div>
            </div>
            <div v-if="!llmProviderPresetLoading && llmProviderPresetsData.length === 0" class="preset-empty">
              暂无预设服务商
            </div>
          </div>
        </aside>
        <!-- 右侧：服务商表单 -->
        <div class="provider-form-main">
          <el-form label-position="top">
            <el-form-item label="服务商名称" required>
              <el-input v-model="llmProviderSettings.sourceName" placeholder="输入服务商名称" />
            </el-form-item>
            <el-form-item label="LLM API 地址" required>
              <el-input v-model="llmProviderSettings.url" placeholder="输入 LLM API 地址" />
            </el-form-item>
            <el-form-item label="API Key" required>
              <el-input v-model="llmProviderSettings.apiKey" placeholder="输入 API Key" />
            </el-form-item>
            <el-form-item label="协议类型" required>
              <el-input v-model="llmProviderSettings.protocol" placeholder="协议类型" disabled />
            </el-form-item>
            <el-form-item label="图标">
              <el-input v-model="llmProviderSettings.icon" placeholder="输入图标 URL" />
            </el-form-item>
            <el-form-item label="启用">
              <el-switch v-model="llmProviderSettings.enabled" />
            </el-form-item>
            <el-form-item label="额外配置 (JSON)">
              <el-input type="textarea" v-model="llmProviderSettings.extraConfigText" placeholder='例如：{"region":"us-east-1"}' :rows="4" />
            </el-form-item>
          </el-form>
        </div>
      </div>
      <template #footer>
        <el-button @click="llmProviderDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveLLMProviderSettings">保存</el-button>
      </template>
    </SidePanel>

    <!-- LLM 模型 -->
    <SidePanel :visible="llmModelDialogVisible" :title="llmModelDialogTitle"
               :panel-style="sidePanel.styleFor(500)"
               @close="llmModelDialogVisible = false" @resize="sidePanel.startResize">
      <el-form label-position="top">
        <el-form-item label="模型名称" required>
          <el-input v-model="llmModelSettings.name" placeholder="输入模型名称" />
        </el-form-item>
        <el-form-item label="上下文窗口">
          <el-input-number v-model="llmModelSettings.contextWindow" placeholder="输入上下文窗口大小" />
        </el-form-item>
        <!-- 基础生成参数：开关 + 值，仅开启的参数会写入 base_params（键存在=使用，键不存在=未使用） -->
        <div class="gen-param-row">
          <el-switch v-model="llmGenParams.maxTokens.enabled" />
          <span class="gen-param-label">最大 tokens</span>
          <el-input-number v-model="llmGenParams.maxTokens.value" :disabled="!llmGenParams.maxTokens.enabled" placeholder="输入最大 tokens 数" :min="0" />
        </div>
        <div class="gen-param-row">
          <el-switch v-model="llmGenParams.temperature.enabled" />
          <span class="gen-param-label">温度</span>
          <el-input-number v-model="llmGenParams.temperature.value" :disabled="!llmGenParams.temperature.enabled" :min="0" :max="1" :step="0.1" placeholder="输入温度" />
        </div>
        <div class="gen-param-row">
          <el-switch v-model="llmGenParams.topP.enabled" />
          <span class="gen-param-label">Top P</span>
          <el-input-number v-model="llmGenParams.topP.value" :disabled="!llmGenParams.topP.enabled" :min="0" :max="1" :step="0.1" placeholder="输入 Top P" />
        </div>
        <el-form-item label="服务商" required>
          <el-input :model-value="getProviderName(llmModelSettings.providerId)" placeholder="选择 LLM 服务商" disabled />
        </el-form-item>
        <el-form-item v-if="llmModelSettings.gatewayId" label="网关模型ID">
          <el-input :model-value="llmModelSettings.gatewayId" disabled />
        </el-form-item>
        <el-form-item label="网关模型名" :disabled="!!llmModelSettings.gatewayId">
          <el-input
            v-model="llmModelSettings.gatewayModel"
            placeholder="在网关中注册的模型名，启用后不可修改"
            :disabled="!!llmModelSettings.gatewayId"
          />
          <template v-if="llmModelSettings.gatewayId" #help>
            模型已启用，网关模型名不可修改，请先禁用再修改
          </template>
        </el-form-item>
        <div class="form-row-2">
          <el-form-item label="设为默认">
            <el-switch v-model="llmModelSettings.isDefault" />
          </el-form-item>
        </div>
        <el-form-item label="额外参数 (JSON)">
          <el-input type="textarea" v-model="llmModelSettings.extraParamsText" placeholder='例如 千问启用思考：{"enable_thinking": true}' :rows="4" />
        </el-form-item>
        <el-form-item label="能力配置 (JSON)">
          <el-input type="textarea" v-model="llmModelSettings.capabilitiesText" placeholder='例如：{"vision":true}' :rows="4" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="llmModelDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveLLMModelSettings">保存</el-button>
      </template>
    </SidePanel>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Edit, Delete, CopyDocument, Plus, Refresh, Remove, ChatDotRound } from '@element-plus/icons-vue'
import apiConfig from '../../../config/api.js'
import { getToken } from '../../../utils/auth.js'
import SidePanel from '../SidePanel.vue'
import { useSidePanelContext } from '../useSidePanel.js'
import { maskSecret, copyText } from '../format.js'

const props = defineProps({ active: { type: Boolean, default: false } })

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
  sourceName: '默认LLM服务商',
  apiKey: '',
  protocol: 'openai-compatible',
  icon: '',
  enabled: true,
  extraConfigText: ''
})

// LLM服务商对话框相关
const llmProviderDialogVisible = ref(false)
const llmProviderDialogTitle = ref('添加LLM服务商')
const currentLLMProvider = ref(null)

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
const llmModelDialogTitle = ref('添加LLM模型')
const currentLLMModel = ref(null)

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
// 后端 xhttp.ErrJSON 虽然 HTTP 状态码为 200，但 body.code 为 400
const checkBizOk = async (response) => {
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`)
  }
  const data = await response.json()
  if (data && typeof data.code !== 'undefined' && data.code !== 200) {
    throw new Error(data.msg || '操作失败')
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
      throw new Error('获取LLM服务商失败')
    }
    const data = await response.json()
    llmProvidersData.value = data?.data?.items ?? []
    reportCount()
  } catch (error) {
    ElMessage.error({
      message: `获取LLM服务商失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('获取LLM服务商失败:', error)
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
      throw new Error('获取LLM服务商预设失败')
    }
    const data = await response.json()
    llmProviderPresetsData.value = data?.data?.items ?? []
    llmProviderPresetsLoaded = true
  } catch (error) {
    ElMessage.error({
      message: `获取LLM服务商预设失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('获取LLM服务商预设失败:', error)
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
  llmProviderDialogTitle.value = '添加LLM服务商'
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
  llmProviderDialogTitle.value = '编辑LLM服务商'
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
    ElMessage.warning('请填写所有必填字段')
    return
  }

  let extraConfig = {}
  if (llmProviderSettings.value.extraConfigText) {
    try {
      extraConfig = JSON.parse(llmProviderSettings.value.extraConfigText)
    } catch (e) {
      ElMessage.warning('额外配置 JSON 格式错误')
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
      throw new Error('保存LLM服务商失败')
    }

    await checkBizOk(response)
    ElMessage.success({
      message: currentLLMProvider.value ? 'LLM服务商更新成功' : 'LLM服务商添加成功',
      duration: 700
    })
    llmProviderDialogVisible.value = false
    // 重新获取LLM服务商列表
    await fetchLLMProviders()
  } catch (error) {
    ElMessage.error({
      message: `保存LLM服务商失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('保存LLM服务商失败:', error)
  } finally {
    llmProviderLoading.value = false
  }
}

// 删除LLM服务商
const deleteLLMProvider = async (source) => {
  try {
    await ElMessageBox.confirm(
      '确定要删除这个 LLM 服务商吗？这将会删除该服务商下的所有 LLM 模型，并更新相关的智能体配置。',
      '提示',
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
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
      throw new Error('删除LLM服务商失败')
    }

    ElMessage.success({
      message: 'LLM服务商删除成功',
      duration: 700
    })
    // 重新获取LLM服务商列表
    await fetchLLMProviders()
    // 重新获取LLM模型列表
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: `删除LLM服务商失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('删除LLM服务商失败:', error)
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
      throw new Error('获取LLM模型失败')
    }
    const data = await response.json()
    llmModelsData.value = data?.data?.items ?? []
    reportCount()
  } catch (error) {
    ElMessage.error({
      message: `获取LLM模型失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('获取LLM模型失败:', error)
  } finally {
    llmModelLoading.value = false
  }
}

// 打开添加LLM模型对话框（可传入服务商 ID 自动带入）
const openAddLLMModelDialog = (providerId) => {
  currentLLMModel.value = null
  llmModelDialogTitle.value = '添加LLM模型'
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
  llmModelDialogTitle.value = '编辑LLM模型'
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
    ElMessage.warning('请填写所有必填字段')
    return
  }

  let extraParams = {}
  if (llmModelSettings.value.extraParamsText) {
    try {
      extraParams = JSON.parse(llmModelSettings.value.extraParamsText)
    } catch (e) {
      ElMessage.warning('额外参数 JSON 格式错误')
      return
    }
  }

  let capabilities = {}
  if (llmModelSettings.value.capabilitiesText) {
    try {
      capabilities = JSON.parse(llmModelSettings.value.capabilitiesText)
    } catch (e) {
      ElMessage.warning('能力配置 JSON 格式错误')
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
      throw new Error('保存LLM模型失败')
    }

    await checkBizOk(response)
    ElMessage.success({
      message: currentLLMModel.value ? 'LLM模型更新成功' : 'LLM模型添加成功',
      duration: 700
    })
    llmModelDialogVisible.value = false
    // 重新获取LLM模型列表
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: `保存LLM模型失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('保存LLM模型失败:', error)
  } finally {
    llmModelLoading.value = false
  }
}

// 删除LLM模型
const deleteLLMModel = async (model) => {
  try {
    await ElMessageBox.confirm('确定要删除这个 LLM 模型吗？', '提示', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
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
      throw new Error('删除LLM模型失败')
    }

    ElMessage.success({
      message: 'LLM模型删除成功',
      duration: 700
    })
    // 重新获取LLM模型列表
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: `删除LLM模型失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('删除LLM模型失败:', error)
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
      throw new Error('启用失败')
    }
    const data = await checkBizOk(response)
    ElMessage.success({
      message: `模型「${model.name}」已启用` + (data?.data?.gateway_id ? `（网关ID: ${data.data.gateway_id}）` : ''),
      duration: 1500
    })
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: `启用模型失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('启用模型失败:', error)
  } finally {
    model._provisioning = false
  }
}

// 禁用 LLM 模型（Deprovision：从 LiteLLM 网关删除并清空 gateway_id）
const deprovisionLLMModel = async (model) => {
  try {
    await ElMessageBox.confirm(`确定要禁用模型「${model.name}」吗？禁用后该模型将无法用于推理调用。`, '提示', {
      confirmButtonText: '禁用',
      cancelButtonText: '取消',
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
      throw new Error('禁用失败')
    }
    await checkBizOk(response)
    ElMessage.success({
      message: `模型「${model.name}」已禁用`,
      duration: 1500
    })
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: `禁用模型失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('禁用模型失败:', error)
  } finally {
    model._deprovisioning = false
  }
}

// 全部注销（DeprovisionAll：从 LiteLLM 网关批量删除本租户所有模型并清空 gateway_id）
const deprovisionAllLLMModels = async () => {
  try {
    await ElMessageBox.confirm(
      '确定要注销当前租户下的全部 LLM 模型吗？注销后这些模型将无法用于推理调用，且需重新启用才能恢复。',
      '全部注销',
      {
        confirmButtonText: '全部注销',
        cancelButtonText: '取消',
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
      throw new Error('全部注销失败')
    }
    const data = await checkBizOk(response)
    const count = data?.data?.count ?? 0
    ElMessage.success({
      message: `已注销 ${count} 个 LLM 模型`,
      duration: 1500
    })
    await fetchLLMModels()
  } catch (error) {
    ElMessage.error({
      message: `全部注销失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('全部注销失败:', error)
  } finally {
    deprovisionAllLoading.value = false
  }
}

// 同步：逐个检查已启用模型，把本地配置对齐到 LiteLLM，并清理网关多余记录
const syncLLMModels = async () => {
  try {
    await ElMessageBox.confirm(
      '将逐个检查已启用模型：网关上缺失的注册过去，配置有变化则覆盖；并删除网关上本租户多余（本地已无引用）的记录。未启用的模型只列出、不注册。确定继续？',
      '同步到 LiteLLM',
      {
        confirmButtonText: '同步',
        cancelButtonText: '取消',
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
      throw new Error('同步失败')
    }
    const data = await checkBizOk(response)
    const r = data?.data || {}
    const skipped = r.skipped || []
    const errors = r.errors || []
    const summary = `新建 ${r.created || 0} 个 · 更新 ${r.updated || 0} 个 · 一致 ${r.unchanged || 0} 个 · 删除多余 ${r.deleted || 0} 个`

    if (errors.length) {
      // 有失败项时用弹窗展示明细；动态内容先转义，避免模型名里的尖括号被当 HTML
      const esc = (s) => String(s).replace(/[&<>"']/g, (ch) => (
        { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]
      ))
      const parts = [`<b>${esc(summary)}</b>`]
      if (skipped.length) {
        parts.push(`<br>已跳过 ${skipped.length} 个未启用模型：`)
        skipped.forEach((s) => parts.push(`<br>· ${esc(s.model)}（${esc(s.reason)}）`))
      }
      parts.push(`<br><br>失败 ${errors.length} 个：`)
      errors.forEach((e) => parts.push(`<br>· ${esc(e.model)}：${esc(e.error)}`))
      ElMessageBox.alert(parts.join(''), '同步完成（有失败项）', {
        confirmButtonText: '知道了',
        type: 'warning',
        dangerouslyUseHTMLString: true
      }).catch(() => {})
    } else if (skipped.length) {
      ElMessage.success({
        message: `${summary}；跳过 ${skipped.length} 个未启用模型`,
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
      message: `同步失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error('同步失败:', error)
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
