<template>
  <div class="module-section">
    <div class="module-head">
      <h3>{{ title }}</h3>
      <div class="head-actions">
        <el-radio-group v-model="authType" size="small" @change="loadConfig">
          <el-radio-button value="apikey">API Key</el-radio-button>
          <el-radio-button value="oauth">OAuth 2.0</el-radio-button>
        </el-radio-group>
        <el-button size="small" :icon="Refresh" :loading="loading" @click="onRefresh">{{ t('settings.inboundAuth.refresh') }}</el-button>
      </div>
    </div>

    <div v-loading="loading" class="config-form">
      <el-alert type="info" :closable="false" show-icon class="config-tip">
        <template #title>{{ t('settings.inboundAuth.tipTitle') }}</template>
        {{ t('settings.inboundAuth.tipBody1', { kind: kind.toUpperCase() }) }}
        <code>POST /service/{{ kind }}/{schema}</code>
        {{ t('settings.inboundAuth.tipBody2') }}
      </el-alert>

      <el-form label-position="top">
        <div class="mcp-enabled-row">
          <el-form-item :label="t('settings.inboundAuth.enabledLabel')" class="mcp-enabled-item">
            <el-switch v-model="config.is_active" :active-text="t('settings.inboundAuth.switchOn')"
                       :inactive-text="t('settings.inboundAuth.switchOff')" @change="onActiveChange" />
          </el-form-item>
        </div>

        <!-- API Key 模式：绑定在「服务凭证」中统一管理 -->
        <template v-if="authType === 'apikey'">
          <el-alert type="info" :closable="false" show-icon style="margin-top: 8px">
            <span>{{ t('settings.inboundAuth.apikeyTip') }}</span>
          </el-alert>
        </template>

        <!-- OAuth 模式 -->
        <template v-if="authType === 'oauth'">
          <el-form-item :label="t('settings.inboundAuth.issuer')" required>
            <el-input v-model="config.auth_config.issuer" placeholder="https://idp.example.com" />
          </el-form-item>
          <el-form-item :label="t('settings.inboundAuth.authorizeEndpoint')">
            <el-input v-model="config.auth_config.authorize_endpoint" placeholder="https://idp.example.com/authorize" />
          </el-form-item>
          <el-form-item :label="t('settings.inboundAuth.jwksUrl')" required>
            <el-input v-model="config.auth_config.jwks_url" placeholder="https://idp.example.com/.well-known/jwks.json" />
          </el-form-item>
          <el-form-item :label="t('settings.inboundAuth.audience')">
            <el-input v-model="config.auth_config.audience" :placeholder="t('settings.inboundAuth.audiencePlaceholder')" />
          </el-form-item>
          <el-form-item :label="t('settings.inboundAuth.requiredScope')">
            <el-input v-model="config.auth_config.required_scope" :placeholder="scopePlaceholder" />
          </el-form-item>
        </template>

        <div v-if="authType !== 'apikey'" class="form-actions">
          <el-button type="primary" :loading="saving" @click="saveConfig">{{ t('settings.inboundAuth.save') }}</el-button>
          <el-button @click="loadConfig">{{ t('settings.inboundAuth.reset') }}</el-button>
        </div>
      </el-form>
    </div>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
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

const { t } = useI18n()

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
    if (!response.ok) throw new Error(t('settings.inboundAuth.loadFailedShort'))
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
      message: t('settings.inboundAuth.loadFailed', { title: props.title, msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`load ${props.title} config failed:`, error)
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
    if (!resp.ok) throw new Error(t('settings.inboundAuth.updateFailed'))
    ElMessage.success({
      message: val ? t('settings.inboundAuth.enabled') : t('settings.inboundAuth.disabled'),
      offset: 20
    })
    emit('refresh-count')
  } catch (e) {
    ElMessage.error({
      message: `${t('settings.inboundAuth.updateFailed')}: ${e.message}`,
      offset: 20
    })
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
    if (!response.ok) throw new Error(t('settings.inboundAuth.saveFailedShort'))
    ElMessage.success({ message: t('settings.inboundAuth.saved'), offset: 20 })
    await loadConfig()
    emit('refresh-count')
  } catch (error) {
    ElMessage.error({
      message: t('settings.inboundAuth.saveFailed', { title: props.title, msg: error.message }),
      duration: 5000,
      offset: 20,
      showClose: true
    })
    console.error(`save ${props.title} config failed:`, error)
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
