<template>
  <div class="module-section">
    <div class="module-head">
      <h3>{{ title }}</h3>
      <div class="head-actions">
        <el-radio-group v-model="authType" size="small" @change="loadConfig">
          <el-radio-button value="apikey">API Key</el-radio-button>
          <el-radio-button value="oauth">OAuth 2.0</el-radio-button>
        </el-radio-group>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="onRefresh">刷新</el-button>
      </div>
    </div>

    <div v-loading="loading" class="config-form">
      <el-alert type="info" :closable="false" show-icon class="config-tip">
        <template #title>入站鉴权配置</template>
        此配置用于校验外部 {{ kind.toUpperCase() }} 客户端调用本租户
        <code>POST /service/{{ kind }}/{schema}</code> 时的身份（路径段即租户标识）。修改后保存即生效。
      </el-alert>

      <el-form label-position="top">
        <div class="mcp-enabled-row">
          <el-form-item label="启用该鉴权方式" class="mcp-enabled-item">
            <el-switch v-model="config.is_active" active-text="启用" inactive-text="停用" @change="onActiveChange" />
          </el-form-item>
        </div>

        <!-- API Key 模式：绑定在「服务凭证」中统一管理 -->
        <template v-if="authType === 'apikey'">
          <el-alert type="info" :closable="false" show-icon style="margin-top: 8px">
            <span>API Key 的用户绑定在「服务凭证」统一管理，此处仅控制该鉴权方式是否启用。</span>
          </el-alert>
        </template>

        <!-- OAuth 模式 -->
        <template v-if="authType === 'oauth'">
          <el-form-item label="Issuer (令牌颁发者)" required>
            <el-input v-model="config.auth_config.issuer" placeholder="https://idp.example.com" />
          </el-form-item>
          <el-form-item label="授权端点 (authorize_endpoint)">
            <el-input v-model="config.auth_config.authorize_endpoint" placeholder="https://idp.example.com/authorize" />
          </el-form-item>
          <el-form-item label="JWKS 地址 (jwks_url)" required>
            <el-input v-model="config.auth_config.jwks_url" placeholder="https://idp.example.com/.well-known/jwks.json" />
          </el-form-item>
          <el-form-item label="Audience">
            <el-input v-model="config.auth_config.audience" placeholder="可选，校验 token 受众" />
          </el-form-item>
          <el-form-item label="Required Scope">
            <el-input v-model="config.auth_config.required_scope" :placeholder="scopePlaceholder" />
          </el-form-item>
        </template>

        <div v-if="authType !== 'apikey'" class="form-actions">
          <el-button type="primary" :loading="saving" @click="saveConfig">保存配置</el-button>
          <el-button @click="loadConfig">重置</el-button>
        </div>
      </el-form>
    </div>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { getToken } from '../../../utils/auth.js'

/**
 * 入站鉴权配置（MCP / RPC 各一张表，按 auth_type 分行）。
 * 两块界面与逻辑完全一致，只有接口地址、路径文案和 scope 示例不同，因此一个组件两处复用。
 */
const props = defineProps({
  active: { type: Boolean, default: false },
  // 'mcp' | 'rpc'：只用于文案（/service/mcp/{schema}）
  kind: { type: String, required: true },
  title: { type: String, required: true },
  // { get(authType), update }
  api: { type: Object, required: true },
  scopePlaceholder: { type: String, default: '' }
})

// 保存后让父级刷新左侧导航上的「已启用方式」计数
const emit = defineEmits(['refresh-count'])

const authType = ref('apikey')
const loading = ref(false)
const saving = ref(false)
const config = ref({ auth_type: 'apikey', is_active: false, auth_config: {} })

const loadConfig = async () => {
  loading.value = true
  try {
    const token = getToken()
    const response = await fetch(props.api.get(authType.value), {
      headers: { Authorization: `Bearer ${token}` }
    })
    if (!response.ok) throw new Error('获取配置失败')
    const data = await response.json()
    const cfg = data?.data || {}
    config.value = {
      auth_type: cfg.auth_type || authType.value,
      is_active: !!cfg.is_active,
      auth_config: cfg.auth_config || (authType.value === 'apikey'
        ? {}
        : { issuer: '', authorize_endpoint: '', jwks_url: '', audience: '', required_scope: '' })
    }
    // apikey 的用户绑定在「服务凭证」统一管理，此处无需加载
  } catch (error) {
    ElMessage.error({
      message: `获取 ${props.title} 配置失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`获取 ${props.title} 配置失败:`, error)
  } finally {
    loading.value = false
  }
}

watch(() => props.active, (active) => {
  if (active) loadConfig()
}, { immediate: true })

const onRefresh = async () => {
  await loadConfig()
  emit('refresh-count')
}

// apikey 模式下切换启用/停用：直接写库（无需点保存）
const onActiveChange = async (val) => {
  if (authType.value !== 'apikey') return
  try {
    const token = getToken()
    const resp = await fetch(props.api.update, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify({ auth_type: 'apikey', is_active: val, auth_config: {} })
    })
    if (!resp.ok) throw new Error('更新失败')
    ElMessage.success({ message: val ? '已启用' : '已停用', offset: 20 })
    emit('refresh-count')
  } catch (e) {
    ElMessage.error({ message: `更新失败: ${e.message}`, offset: 20 })
    config.value.is_active = !val
  }
}

const saveConfig = async () => {
  saving.value = true
  try {
    const token = getToken()
    const payload = {
      auth_type: authType.value,
      is_active: config.value.is_active,
      // apikey：auth_config 固定为空对象（密钥与用户绑定在 svc_auth_bindings 管理）
      auth_config: authType.value === 'apikey' ? {} : config.value.auth_config
    }
    const response = await fetch(props.api.update, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(payload)
    })
    if (!response.ok) throw new Error('保存失败')
    ElMessage.success({ message: '配置已保存', offset: 20 })
    await loadConfig()
    emit('refresh-count')
  } catch (error) {
    ElMessage.error({
      message: `保存 ${props.title} 配置失败: ${error.message}`,
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`保存 ${props.title} 配置失败:`, error)
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
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

.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.head-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.config-form {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.config-tip {
  margin-bottom: 12px;
}

.mcp-enabled-row {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 6px;
}

.mcp-enabled-item {
  margin-bottom: 0;
}

.form-actions {
  display: flex;
  gap: 10px;
  margin-top: 8px;
}
</style>
