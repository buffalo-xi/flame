<template>
  <!-- 端点面板：把工作流暴露成 MCP / RPC 入口（同一目标、同一协议只能有一个端点） -->
  <div ref="panelRootRef" class="expose-panel" :style="{ width: exposePanelWidth + 'px' }">
    <div class="panel-resizer" @mousedown="startExposeResize"></div>
    <div class="panel-header">
      <span class="panel-title">{{ t('flow.endpoints.panelTitle', { name: wf?.name }) }}</span>
      <span class="close-icon" @click="emit('close')">&#xd7;</span>
    </div>
    <div class="panel-body">
      <!-- 新建端点表单（无端点时直接进入，有端点时点 + 进入） -->
      <template v-if="showExposeForm">
        <div class="form-section">
          <div class="form-head">
            <div class="form-title">{{ t('flow.endpoints.formTitle') }}</div>
            <div class="form-sub">
              {{ t('flow.endpoints.formHint') }}
            </div>
          </div>

          <el-form :model="exposeFormData" label-width="64px" :rules="exposeRules" ref="exposeFormRef">
            <el-form-item :label="t('flow.endpoints.nameLabel')">
              <el-input :model-value="wf?.name || ''" disabled />
              <div class="field-hint">{{ t('flow.endpoints.nameHint') }}</div>
            </el-form-item>
            <el-form-item :label="t('flow.endpoints.protocolLabel')" prop="protocol">
              <el-radio-group v-model="exposeFormData.protocol" class="protocol-group">
                <el-radio-button v-for="p in availableProtocols" :key="p.value" :value="p.value">
                  {{ p.label }}
                </el-radio-button>
              </el-radio-group>
              <div class="field-hint">{{ protocolHint }}</div>
            </el-form-item>
            <el-form-item :label="t('flow.endpoints.descLabel')">
              <el-input
                v-model="exposeFormData.description"
                type="textarea"
                :rows="3"
                :placeholder="t('flow.endpoints.descPlaceholder')"
              />
            </el-form-item>
            <!-- 只读参数：端点建好后 input_schema 恒等于工作流开始节点声明的入参，
                 所以这里只做预览，不让用户手写（旧「配置」框运行时没人读，已下线） -->
            <el-form-item :label="t('flow.endpoints.argsLabel')">
              <div class="arg-preview">
                <template v-if="argRows.length">
                  <div v-for="a in argRows" :key="a.name" class="arg-row">
                    <span class="arg-name">{{ a.name }}</span>
                    <span class="arg-type">{{ a.type }}</span>
                    <span v-if="a.required" class="arg-req">{{ t('flow.common.required') }}</span>
                    <span v-if="a.desc" class="arg-desc" :title="a.desc">{{ a.desc }}</span>
                  </div>
                  <div class="arg-hint">{{ t('flow.endpoints.argsHint') }}</div>
                </template>
                <div v-else class="arg-empty">{{ t('flow.endpoints.argsEmpty') }}</div>
              </div>
            </el-form-item>
          </el-form>

          <div class="form-actions">
            <el-button @click="handleCancelExposeForm">{{ t('common.cancel') }}</el-button>
            <el-button type="primary" :loading="submitting" @click="submitExposeForm">{{ t('flow.endpoints.create') }}</el-button>
          </div>
        </div>
      </template>
      <!-- 端点列表 -->
      <template v-else>
        <div class="endpoints-section">
          <div class="section-header">
            <span class="section-title">{{ t('flow.endpoints.sectionTitle') }}</span>
            <div class="section-header-actions">
              <button
                type="button"
                v-if="!allProtocolsExposed"
                class="add-endpoint-btn"
                @click="openAddEndpointForm"
                :title="t('flow.endpoints.addEndpoint')"
              >
                <el-icon><Plus /></el-icon>
              </button>
              <button
                type="button"
                v-if="exposedEndpoints.some(ep => !ep.exposed)"
                class="expose-all-btn"
                @click="handleExposeAll"
              >
                {{ t('flow.endpoints.exposeAll') }}
              </button>
              <button
                type="button"
                v-if="exposedEndpoints.some(ep => ep.exposed)"
                class="retract-all-btn"
                @click="handleRetractAll"
              >
                {{ t('flow.endpoints.retractAll') }}
              </button>
            </div>
          </div>
          <div v-if="exposeLoading" class="endpoints-loading">{{ t('flow.runPanel.loading') }}</div>
          <div v-else-if="exposedEndpoints.length === 0" class="endpoints-empty">
            {{ t('flow.endpoints.empty') }}
          </div>
          <div v-else class="endpoints-list">
            <div class="endpoint-item" v-for="ep in exposedEndpoints" :key="ep.id">
              <div class="endpoint-item-header">
                <span class="protocol-tag" :style="{ backgroundColor: getProtocolColor(ep.protocol) }">
                  {{ (ep.protocol || '').toUpperCase() }}
                </span>
                <span class="endpoint-name">{{ ep.name }}</span>
                <el-switch
                  :model-value="ep.exposed"
                  @change="(val) => handleToggleEndpointEnabled(ep, val)"
                  size="small"
                  inline-prompt
                  :active-text="t('flow.endpoints.switchOn')"
                  :inactive-text="t('flow.endpoints.switchOff')"
                  :title="ep.exposed ? t('flow.endpoints.exposedTip') : t('flow.endpoints.internalTip')"
                />
                <!-- RPC 端点由流程托管（建流程时自动补），删掉会断掉 local:rpc:<name> 的引用 -->
                <span
                  v-if="ep.protocol === 'rpc'"
                  class="endpoint-managed"
                  :title="t('flow.endpoints.managedTip')"
                >
                  {{ t('flow.endpoints.managed') }}
                </span>
                <button
                  v-else
                  type="button"
                  class="endpoint-delete-btn"
                  :disabled="ep.exposed"
                  :title="ep.exposed ? t('flow.endpoints.deleteBlockedTip') : t('flow.endpoints.deleteTip')"
                  @click="handleDeleteEndpoint(ep)"
                >
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
              <div v-if="ep.description" class="endpoint-desc">{{ ep.description }}</div>
              <div class="endpoint-meta">
                <span class="endpoint-time">{{ t('flow.endpoints.createdAt', { time: formatTime(ep.created_at) }) }}</span>
                <span class="endpoint-time">{{ t('flow.endpoints.updatedAt', { time: formatTime(ep.updated_at) }) }}</span>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Delete } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { getProtocolColor, formatTime } from '../../utils/flowFormat.js'
import { useEscClose } from '../../composables/useEscClose'
import { useI18n } from 'vue-i18n'
import { formatError } from '../../../locales'

const { t } = useI18n()

const props = defineProps({
  // 要管理端点的工作流（{ id, name, description }）
  wf: { type: Object, required: true },
  // 刷新实例列表：端点增删改后卡片上的端点标记才会准
  refreshList: { type: Function, required: true },
  // 同步单张卡片的端点标记（父级持有 flowsList）
  syncCard: { type: Function, required: true }
})
const emit = defineEmits(['close'])

// 父级用 v-if 挂载：挂上即「窗口开着」，卸载时 useEscClose 自己出栈
useEscClose(true, () => emit('close'))

// 面板根节点：用于「点击面板外关闭」
const panelRootRef = ref(null)

const exposeLoading = ref(false)
const exposedEndpoints = ref([])
const exposeFormData = ref({
  name: '',
  description: '',
  protocol: 'mcp'
})
const exposeRules = ref({
  protocol: [
    { required: true, message: t('flow.endpoints.selectProtocol'), trigger: 'change' }
  ]
})
const exposeFormRef = ref(null)
// 面板当前显示新建表单（true）还是端点列表（false）
const showExposeForm = ref(false)
const submitting = ref(false)

// 工作流声明的入参（flows.arguments）：端点建好后 input_schema 就是它，这里只读预览
const argSchema = ref(null)

// 面板宽度
const exposePanelWidth = ref(400)

// 面板拖拽调整宽度
const startExposeResize = (e) => {
  const startX = e.clientX
  const startWidth = exposePanelWidth.value

  const onMouseMove = (e) => {
    const deltaX = startX - e.clientX
    const newWidth = Math.max(400, startWidth + deltaX)
    exposePanelWidth.value = newWidth
  }

  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }

  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// 是否所有协议都已建端点
const allProtocolsExposed = computed(() => {
  const protocols = new Set(exposedEndpoints.value.map(ep => ep.protocol))
  return protocols.has('mcp') && protocols.has('rpc')
})

// 可选协议（过滤掉已建端点的协议：同一协议只能有一个端点）
const availableProtocols = computed(() => {
  const exposed = new Set(exposedEndpoints.value.map(ep => ep.protocol))
  return [
    { label: 'MCP', value: 'mcp' },
    { label: 'RPC', value: 'rpc' }
  ].filter(p => !exposed.has(p.value))
})

// 两种协议的外部表现不同，选错的代价是调用方式对不上 —— 选的时候就给一句说明
const protocolHint = computed(() => (
  exposeFormData.value.protocol === 'rpc'
    ? t('flow.endpoints.protocolRpcHint')
    : t('flow.endpoints.protocolMcpHint')
))

// 「参数」预览：把开始节点声明的 JSON Schema 摊平成一行一个参数
const formatArgType = (cfg) => {
  const type = cfg?.type
  if (Array.isArray(type)) return type.filter(t => t !== 'null').join(' | ') || '—'
  if (type === 'array' && cfg?.items?.type) return `array<${cfg.items.type}>`
  return type || '—'
}

const argRows = computed(() => {
  const schema = argSchema.value
  const properties = schema?.properties
  if (!properties || typeof properties !== 'object') return []
  const required = new Set(Array.isArray(schema?.required) ? schema.required : [])
  return Object.entries(properties).map(([name, cfg]) => ({
    name,
    type: formatArgType(cfg),
    required: required.has(name),
    desc: cfg?.description || ''
  }))
})

// 点击 + 新建端点：协议默认取第一个还没建过的，并切到表单
const openAddEndpointForm = () => {
  if (availableProtocols.value.length > 0) {
    exposeFormData.value.protocol = availableProtocols.value[0].value
  }
  showExposeForm.value = true
}

// 获取该目标下的端点列表
const fetchExposedEndpoints = async (targetId, targetType) => {
  exposeLoading.value = true
  try {
    const token = getToken()
    if (!token) return
    const response = await fetch(adminApi.svcEndpoints.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        target_id: targetId,
        target_type: targetType
      })
    })
    if (!response.ok) return
    const data = await response.json()
    if (data.code === 200) {
      exposedEndpoints.value = data.data.items || []
    }
  } catch (error) {
    console.error(t('flow.endpoints.loadFailed'), error)
  } finally {
    exposeLoading.value = false
  }
}

// 读工作流声明的入参做预览。端点建好后 input_schema 会被后端强制覆盖成 flows.arguments
// （见 svcendpoints 仓储 Create），所以这里展示的就是端点将来对外声明的入参。
const fetchArgSchema = async () => {
  if (!props.wf?.id) return
  try {
    const token = getToken()
    if (!token) return
    const response = await fetch(`${adminApi.flows.base}${props.wf.id}`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!response.ok) return
    const data = await response.json()
    if (data.code === 200) argSchema.value = data.data?.arguments || null
  } catch (error) {
    // 预览拿不到不影响创建：空着就显示「尚未声明入参」
    console.error(t('flow.endpoints.loadArgsFailed'), error)
  }
}

// 提交新建端点表单
const submitExposeForm = async () => {
  if (!exposeFormRef.value || submitting.value) return
  try {
    await exposeFormRef.value.validate()
  } catch {
    return // 校验失败由表单项自己标红，不再叠一条全局错误
  }
  const token = getToken()
  if (!token) {
    ElMessage.error({ message: t('common.loginRequired'), duration: 5000, offset: 20, showClose: true })
    return
  }

  submitting.value = true
  try {
    // 不再提交 config：端点运行时从不读这一列（入参走 input_schema），
    // 让用户手写一段没人消费的 JSON 只会制造误解。
    const response = await fetch(adminApi.svcEndpoints.create, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        name: exposeFormData.value.name,
        description: exposeFormData.value.description,
        target_id: props.wf.id,
        target_type: 'flow',
        protocol: exposeFormData.value.protocol
      })
    })

    if (!response.ok) {
      throw new Error(t('flow.endpoints.createFailed'))
    }

    const data = await response.json()
    if (data.code === 200) {
      ElMessage.success({ message: t('flow.endpoints.created'), duration: 700 })
      // 重置新增表单（保留工作流名称作为默认）
      exposeFormData.value = {
        name: props.wf.name || '',
        description: props.wf.description || '',
        protocol: 'mcp'
      }
      // 刷新端点列表
      await fetchExposedEndpoints(props.wf.id, 'flow')
      // 局部更新对应卡片的 is_exposed 状态
      props.syncCard(props.wf.id, exposedEndpoints.value.map(ep => ({ protocol: ep.protocol, exposed: ep.exposed })))
      // 端点状态变了，列表要重新拉一次（卡片上的端点与状态 tag 才准）
      await props.refreshList()
      // 创建成功后回到端点列表视图
      showExposeForm.value = false
    } else {
      ElMessage.error({ message: t('flow.endpoints.createFailedReason', { msg: formatError(data) }), duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error(t('flow.endpoints.createFailed'), error)
    ElMessage.error({ message: t('flow.endpoints.createFailedRetry'), duration: 5000, offset: 20, showClose: true })
  } finally {
    submitting.value = false
  }
}

// 取消新建表单，回到端点列表（无端点时关闭面板）
const handleCancelExposeForm = () => {
  if (exposedEndpoints.value.length === 0) {
    emit('close')
  } else {
    showExposeForm.value = false
  }
}

// 切换端点对外开放 / 收回
const handleToggleEndpointEnabled = async (ep, exposed) => {
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: t('common.loginRequired'), duration: 5000, offset: 20, showClose: true })
      return
    }
    const response = await fetch(`${adminApi.svcEndpoints.base}/${ep.id}`, {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ exposed })
    })
    if (!response.ok) throw new Error(t('flow.endpoints.updateFailed'))
    const data = await response.json()
    if (data.code === 200) {
      ep.exposed = exposed
      ElMessage.success({ message: exposed ? t('flow.endpoints.exposed') : t('flow.endpoints.retracted'), duration: 700 })
      props.syncCard(props.wf.id, exposedEndpoints.value.map(ep => ({ protocol: ep.protocol, exposed: ep.exposed })))
      // 端点对外状态变了：列表重新拉一次，卡片上的端点与状态 tag 才准
      await props.refreshList()
    } else {
      ElMessage.error({ message: t('flow.endpoints.updateFailedReason', { msg: formatError(data) }), duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error(t('flow.endpoints.toggleFailed'), error)
    ElMessage.error({ message: t('flow.endpoints.updateFailedRetry'), duration: 5000, offset: 20, showClose: true })
  }
}

// 删除单个端点
const handleDeleteEndpoint = (ep) => {
  ElMessageBox.confirm(
    t('flow.endpoints.deleteConfirm', { name: ep.name, protocol: (ep.protocol || '').toLowerCase() }),
    t('flow.endpoints.deleteConfirmTitle'),
    {
      confirmButtonText: t('common.confirm'),
      cancelButtonText: t('common.cancel'),
      type: 'warning'
    }
  ).then(async () => {
    try {
      const token = getToken()
      if (!token) {
        ElMessage.error({ message: t('common.loginRequired'), duration: 5000, offset: 20, showClose: true })
        return
      }
      const response = await fetch(`${adminApi.svcEndpoints.delete}${ep.id}`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        }
      })
      if (!response.ok) throw new Error(t('flow.endpoints.deleteFailed'))
      const data = await response.json()
      if (data.code === 200) {
        ElMessage.success({ message: t('flow.endpoints.deleted'), duration: 700 })
        await fetchExposedEndpoints(props.wf.id, 'flow')
        // 删除后若已无端点，则同步卡片上的端点标记
        props.syncCard(props.wf.id, exposedEndpoints.value.map(ep => ({ protocol: ep.protocol, exposed: ep.exposed })))
      } else {
        ElMessage.error({ message: t('flow.endpoints.deleteFailedReason', { msg: formatError(data) }), duration: 5000, offset: 20, showClose: true })
      }
    } catch (error) {
      console.error(t('flow.endpoints.deleteFailedLog'), error)
      ElMessage.error({ message: t('flow.endpoints.deleteFailedRetry'), duration: 5000, offset: 20, showClose: true })
    }
  }).catch(() => {})
}

// 全部对外开放
const handleExposeAll = async () => {
  const retracted = exposedEndpoints.value.filter(ep => !ep.exposed)
  if (retracted.length === 0) {
    ElMessage.info({ message: t('flow.endpoints.allExposed'), duration: 700 })
    return
  }
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: t('common.loginRequired'), duration: 5000, offset: 20, showClose: true })
      return
    }
    for (const ep of retracted) {
      const response = await fetch(`${adminApi.svcEndpoints.base}/${ep.id}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({ exposed: true })
      })
      if (!response.ok) throw new Error(t('flow.endpoints.exposeAllFailed'))
      const data = await response.json()
      if (data.code !== 200) throw new Error(formatError(data) || t('flow.endpoints.exposeAllFailed'))
    }
    ElMessage.success({ message: t('flow.endpoints.allExposedDone'), duration: 700 })
    await fetchExposedEndpoints(props.wf.id, 'flow')
    props.syncCard(props.wf.id, exposedEndpoints.value.map(ep => ({ protocol: ep.protocol, exposed: ep.exposed })))
    // 端点对外开放不改定义状态（status 与「对外」无关），只刷新卡片上的端点标记
    await props.refreshList()
  } catch (error) {
    console.error(t('flow.endpoints.exposeAllFailedLog'), error)
    ElMessage.error({ message: t('flow.endpoints.exposeAllFailedRetry'), duration: 5000, offset: 20, showClose: true })
  }
}

// 全部收回
const handleRetractAll = async () => {
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: t('common.loginRequired'), duration: 5000, offset: 20, showClose: true })
      return
    }
    const response = await fetch(adminApi.svcEndpoints.retract, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        target_type: 'flow',
        target_id: props.wf.id
      })
    })
    if (!response.ok) throw new Error(t('flow.endpoints.retractFailed'))
    const data = await response.json()
    if (data.code === 200) {
      ElMessage.success({ message: t('flow.endpoints.allRetractedDone'), duration: 700 })
      await fetchExposedEndpoints(props.wf.id, 'flow')
      // 全部收回后更新卡片为「仅内部」
      props.syncCard(props.wf.id, exposedEndpoints.value.map(ep => ({ protocol: ep.protocol, exposed: ep.exposed })))
    } else {
      ElMessage.error({ message: t('flow.endpoints.retractFailedReason', { msg: formatError(data) }), duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error(t('flow.endpoints.retractAllFailedLog'), error)
    ElMessage.error({ message: t('flow.endpoints.retractFailedRetry'), duration: 5000, offset: 20, showClose: true })
  }
}


// 打开面板时初始化：表单默认值取工作流名称/描述，随后按端点数量决定显示表单还是列表
onMounted(async () => {
  exposeFormData.value = {
    name: props.wf?.name || '',
    description: props.wf?.description || '',
    protocol: 'mcp'
  }
  // 参数预览与端点列表互不依赖，并行拉（预览失败不影响创建）
  fetchArgSchema()
  await fetchExposedEndpoints(props.wf.id, 'flow')
  // 无端点时直接显示编辑表单，有端点时显示列表
  showExposeForm.value = exposedEndpoints.value.length === 0
})

// 点击面板外部时自动关闭
const handlePanelOutsideClick = (e) => {
  const target = e.target
  // 忽略 Element Plus 浮层（确认框/下拉等 teleport 到 body 的元素）的点击
  if (target.closest && (target.closest('.el-overlay') || target.closest('.el-select-dropdown') || target.closest('.el-popper'))) {
    return
  }
  if (panelRootRef.value && !panelRootRef.value.contains(target)) {
    emit('close')
  }
}
// nextTick 延迟绑定，避免打开按钮的 mousedown 立即触发关闭；
// 用捕获阶段，避免被工作流编辑器画布等子组件的 stopPropagation 拦截
onMounted(() => {
  nextTick(() => document.addEventListener('mousedown', handlePanelOutsideClick, true))
})
onUnmounted(() => {
  document.removeEventListener('mousedown', handlePanelOutsideClick, true)
})
</script>

<style scoped>
/* ========== 端点面板 ========== */
.expose-panel {
  position: absolute;
  z-index: 100;
  right: 0;
  top: 2%;
  bottom: 2%;
  background: #ffffff;
  border-radius: 8px 0 0 8px;
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
  background: #e0e0e0;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 16px;
  color: black;
  gap: 12px;
}

.panel-title {
  font-weight: 600;
  font-size: 14px;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
}

/* 表单区：不再套灰底标题栏 —— 面板自身已有标题，双层标题在 400px 里显脏 */
.form-head {
  margin-bottom: 12px;
}

.form-title {
  font-size: 14px;
  font-weight: 600;
  color: #1f2329;
}

.form-sub {
  margin-top: 2px;
  font-size: 12px;
  line-height: 1.5;
  color: #8a9099;
}

/* 表单项的辅助说明：el-form-item 的 content 是 flex，必须占满整行才会换行 */
.field-hint {
  width: 100%;
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.5;
  color: #8a9099;
}

/* 参数预览（只读）：端点建好后 input_schema 就是这份声明 */
.arg-preview {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  background: #fafbfc;
}

.arg-row {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 6px;
  padding: 3px 0;
  font-size: 12px;
}

.arg-name {
  font-family: 'SFMono-Regular', Consolas, monospace;
  color: #1f2329;
}

.arg-type {
  color: #8a9099;
}

.arg-req {
  padding: 0 4px;
  border-radius: 3px;
  background: #fdf6ec;
  color: #e6a23c;
  font-size: 11px;
}

.arg-desc {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  color: #6b7280;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.arg-hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.5;
  color: #8a9099;
}

.arg-empty {
  font-size: 12px;
  color: #8a9099;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
  padding-top: 14px;
  border-top: 1px solid #f0f0f0;
}

.protocol-group :deep(.el-radio-button__inner) {
  padding: 6px 14px;
  font-size: 13px;
}

.protocol-tag {
  display: inline-block;
  padding: 2px 10px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
  color: #fff;
  white-space: nowrap;
}

/* 端点列表 */
.endpoints-section {
  margin-bottom: 8px;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: #333;
}

.expose-all-btn {
  background: #67c23a;
  color: #fff;
  border: none;
  padding: 4px 12px;
  border-radius: 4px;
  font-size: 12px;
  cursor: pointer;
  transition: background 0.2s;
}

.expose-all-btn:hover {
  background: #5daf34;
}

.retract-all-btn {
  background: #f56c6c;
  color: #fff;
  border: none;
  padding: 4px 12px;
  border-radius: 4px;
  font-size: 12px;
  cursor: pointer;
  transition: background 0.2s;
}

.retract-all-btn:hover {
  background: #e04848;
}

.section-header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.add-endpoint-btn {
  background: #f0f0f0;
  color: #333;
  border: 1px solid #d9d9d9;
  padding: 4px 8px;
  border-radius: 4px;
  font-size: 14px;
  cursor: pointer;
  display: flex;
  align-items: center;
  transition: background 0.2s;
}

.add-endpoint-btn:hover {
  background: #e6e6e6;
}

.form-section {
  height: 100%;
  display: flex;
  flex-direction: column;
}

.endpoints-loading,
.endpoints-empty {
  text-align: center;
  padding: 24px;
  color: #999;
  font-size: 13px;
  background-color: #fafafa;
  border-radius: 8px;
}

.endpoints-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.endpoint-item {
  border: 1px solid #e8e8e8;
  border-radius: 8px;
  padding: 12px;
  background-color: #fff;
  transition: box-shadow 0.2s;
}

.endpoint-item:hover {
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
}

.endpoint-item-header {
  display: flex;
  align-items: center;
  gap: 10px;
}

.endpoint-name {
  flex: 1;
  font-size: 14px;
  font-weight: 600;
  color: #333;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.endpoint-delete-btn {
  flex-shrink: 0;
  background: none;
  border: none;
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  color: #f56c6c;
  transition: background 0.2s;
  display: flex;
  align-items: center;
  justify-content: center;
}

.endpoint-delete-btn:hover {
  background: #fff2f0;
}

.endpoint-delete-btn:disabled {
  cursor: not-allowed;
  opacity: 0.4;
}

.endpoint-delete-btn:disabled:hover {
  background: none;
}

/* 托管端点（RPC）：不给删除按钮，用一个静态标记说明它从哪来 */
.endpoint-managed {
  flex-shrink: 0;
  padding: 0 4px;
  border-radius: 3px;
  background: #f2f3f5;
  color: #8a9099;
  font-size: 11px;
  line-height: 16px;
  cursor: default;
}

.endpoint-desc {
  margin-top: 8px;
  font-size: 12px;
  color: #666;
  line-height: 1.5;
}

.endpoint-meta {
  margin-top: 6px;
  display: flex;
  gap: 16px;
}

.endpoint-time {
  font-size: 12px;
  color: #999;
}

/* 表单项：label 只是辅助，别抢输入框的视觉重量 */
:deep(.el-form-item__label) {
  color: #4b5563;
  font-weight: 400;
}

@media (max-width: 768px) {
  .wf-instances-list {
    grid-template-columns: 1fr;
  }
}

</style>
