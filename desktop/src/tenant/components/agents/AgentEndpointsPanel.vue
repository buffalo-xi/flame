<template>
  <!-- 端点面板：右侧抽屉式，管理该智能体的 MCP / RPC 端点（同一目标、同一协议只能有一个） -->
  <div v-if="visible" ref="panelRef" class="expose-panel" :style="{ width: panelWidth + 'px' }">
    <div class="panel-resizer" @mousedown="startResize"></div>
    <div class="panel-header">
      <span class="panel-title">端点: {{ agent?.name }}</span>
      <span class="close-icon" @click="$emit('close')">&#xd7;</span>
    </div>
    <div class="panel-body">
      <!-- 新增编辑表单（无端点或有端点时点击 + 进入） -->
      <template v-if="showForm">
        <div class="form-section">
          <div class="form-head">
            <div class="form-title">新建端点</div>
            <div class="form-sub">端点 = 对外调用入口，名字跟实体走；同一协议只能建一个。</div>
          </div>

          <el-form :model="formData" label-width="64px" :rules="rules" ref="formRef">
            <el-form-item label="端点名">
              <el-input :model-value="agent?.name || ''" disabled />
              <div class="field-hint">与实体名一致，改名请回实体本身</div>
            </el-form-item>
            <el-form-item label="协议" prop="protocol">
              <el-radio-group v-model="formData.protocol" class="protocol-group">
                <el-radio-button v-for="p in availableProtocols" :key="p.value" :value="p.value">
                  {{ p.label }}
                </el-radio-button>
              </el-radio-group>
              <div class="field-hint">{{ protocolHint }}</div>
            </el-form-item>
            <el-form-item label="描述">
              <el-input
                v-model="formData.description"
                type="textarea"
                :rows="3"
                placeholder="外部调用方看到的就是这段说明"
              />
            </el-form-item>
            <!-- 智能体没有「入参声明」这一层：端点对外是开放 JSON，由智能体自己理解 -->
            <el-form-item label="参数">
              <div class="arg-preview">
                <div class="arg-empty">智能体不声明入参：外部调用按自由 JSON 传入，由智能体自行理解</div>
              </div>
            </el-form-item>
          </el-form>

          <div class="form-actions">
            <el-button @click="cancelForm">取消</el-button>
            <el-button type="primary" :loading="submitting" @click="submitForm">创建</el-button>
          </div>
        </div>
      </template>
      <!-- 端点列表 -->
      <template v-else>
        <div class="endpoints-section">
          <div class="section-header">
            <span class="section-title">已配置端点</span>
            <div class="section-header-actions">
              <button
                v-if="!allProtocolsExposed"
                class="add-endpoint-btn"
                @click="openAddForm"
                title="新增端点"
              >
                <el-icon><Plus /></el-icon>
              </button>
              <button
                v-if="endpoints.some(ep => !ep.exposed)"
                class="expose-all-btn"
                @click="exposeAll"
              >
                全部对外开放
              </button>
              <button
                v-if="endpoints.some(ep => ep.exposed)"
                class="retract-all-btn"
                @click="retractAll"
              >
                全部收回
              </button>
            </div>
          </div>
          <div v-if="loading" class="endpoints-loading">加载中...</div>
          <div v-else-if="endpoints.length === 0" class="endpoints-empty">
            暂无端点
          </div>
          <div v-else class="endpoints-list">
            <div class="endpoint-item" v-for="ep in endpoints" :key="ep.id">
              <div class="endpoint-item-header">
                <span class="protocol-tag" :style="{ backgroundColor: getProtocolColor(ep.protocol) }">
                  {{ (ep.protocol || '').toUpperCase() }}
                </span>
                <span class="endpoint-name">{{ ep.name }}</span>
                <!-- 文案对齐端点域：对外开放 / 仅内部。「发 / 下」是资产域的说法，别借 -->
                <el-switch
                  :model-value="ep.exposed"
                  @change="(val) => toggleEndpoint(ep, val)"
                  size="small"
                  inline-prompt
                  active-text="外"
                  inactive-text="内"
                  :title="ep.exposed ? '对外开放：租户外可用凭证调用' : '仅内部：只有本租户内可调用'"
                />
                <button
                  class="endpoint-delete-btn"
                  :disabled="ep.exposed"
                  :title="ep.exposed ? '仍在对外开放，先「收回对外」再删除' : '删除端点'"
                  @click="deleteEndpoint(ep)"
                >
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
              <div v-if="ep.description" class="endpoint-desc">{{ ep.description }}</div>
              <div class="endpoint-meta">
                <span class="endpoint-time">创建: {{ formatTime(ep.created_at) }}</span>
                <span class="endpoint-time">更新: {{ formatTime(ep.updated_at) }}</span>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, nextTick, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Delete } from '@element-plus/icons-vue'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { useEscClose } from '../../composables/useEscClose'

const props = defineProps({
  visible: { type: Boolean, default: false },
  // 要管理的智能体：只用到 id / name / description
  agent: { type: Object, default: null }
})
// changed：端点状态变了，父组件据此更新卡片色带 / 端点角标
const emit = defineEmits(['close', 'changed'])

useEscClose(() => props.visible, () => emit('close'))

const panelRef = ref(null)
const panelWidth = ref(400)
const loading = ref(false)
const endpoints = ref([])
const formRef = ref(null)
// true = 新增表单视图，false = 端点列表视图
const showForm = ref(false)
const submitting = ref(false)
const formData = ref({
  name: '',
  description: '',
  protocol: 'mcp'
})
const rules = {
  protocol: [
    { required: true, message: '请选择协议', trigger: 'change' }
  ]
}

// 协议颜色映射
const getProtocolColor = (protocol) => {
  const map = { mcp: '#52c41a', rpc: '#1890ff', llm: '#fa8c16' }
  return map[protocol] || '#909399'
}

// 格式化时间
const formatTime = (time) => {
  if (!time) return '-'
  const date = new Date(time)
  if (isNaN(date.getTime())) return '-'
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const hours = String(date.getHours()).padStart(2, '0')
  const minutes = String(date.getMinutes()).padStart(2, '0')
  return `${year}/${month}/${day} ${hours}:${minutes}`
}

// 是否所有协议都已建端点
const allProtocolsExposed = computed(() => {
  const protocols = new Set(endpoints.value.map(ep => ep.protocol))
  return protocols.has('mcp') && protocols.has('rpc')
})

// 可选协议（同一协议只能有一个端点，已建过的过滤掉）
const availableProtocols = computed(() => {
  const exposed = new Set(endpoints.value.map(ep => ep.protocol))
  return [
    { label: 'MCP', value: 'mcp' },
    { label: 'RPC', value: 'rpc' }
  ].filter(p => !exposed.has(p.value))
})

// 两种协议的外部表现不同，选的时候就给一句说明
const protocolHint = computed(() => (
  formData.value.protocol === 'rpc'
    ? 'RPC：服务端直调；智能体端点不会列进其它智能体的工具目录（只有工作流的 RPC 端点会）'
    : 'MCP：给外部 MCP 客户端注册成工具，入参为自由 JSON'
))

// 端点状态变化后通知父组件：卡片色带 + 端点角标（必要时再拉一次详情纠正状态）
const notifyChanged = (reloadStatus = false) => {
  emit('changed', {
    agentId: props.agent?.id,
    endpoints: endpoints.value.map(ep => ({ protocol: ep.protocol, exposed: !!ep.exposed })),
    reloadStatus
  })
}

const resetForm = () => {
  formData.value = {
    name: props.agent?.name || '',
    description: props.agent?.description || '',
    protocol: 'mcp'
  }
}

// 获取该智能体下的端点列表
const fetchEndpoints = async () => {
  const targetId = props.agent?.id
  if (!targetId) return
  loading.value = true
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
        target_type: 'agent'
      })
    })
    if (!response.ok) return
    const data = await response.json()
    if (data.code === 200) {
      endpoints.value = data.data.items || []
      notifyChanged()
    }
  } catch (error) {
    console.error('获取端点列表失败:', error)
  } finally {
    loading.value = false
  }
}

// 点击 + 新建端点：协议默认取第一个还没建过的，切到表单视图
const openAddForm = () => {
  if (availableProtocols.value.length > 0) {
    formData.value.protocol = availableProtocols.value[0].value
  }
  showForm.value = true
}

// 取消表单：无端点时直接关面板，否则回到列表
const cancelForm = () => {
  if (endpoints.value.length === 0) {
    emit('close')
  } else {
    showForm.value = false
  }
}

// 提交新建端点
const submitForm = async () => {
  if (!formRef.value || submitting.value) return
  try {
    await formRef.value.validate()
  } catch {
    return // 校验失败由表单项自己标红，不再叠一条全局错误
  }
  const token = getToken()
  if (!token) {
    ElMessage.error({ message: '请先登录', duration: 5000, offset: 20, showClose: true })
    return
  }

  submitting.value = true
  try {
    // 不再提交 config：端点运行时从不读这一列，入参走 input_schema
    const response = await fetch(adminApi.svcEndpoints.create, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        name: formData.value.name,
        description: formData.value.description,
        target_id: props.agent?.id,
        target_type: 'agent',
        protocol: formData.value.protocol
      })
    })

    if (!response.ok) throw new Error('创建端点失败')
    const data = await response.json()
    if (data.code === 200) {
      ElMessage.success({ message: '端点已创建', duration: 700 })
      resetForm()
      await fetchEndpoints()
      showForm.value = false
    } else {
      ElMessage.error({ message: `创建端点失败: ${data.msg}`, duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error('创建端点失败:', error)
    ElMessage.error({ message: '创建端点失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
  } finally {
    submitting.value = false
  }
}

// 切换单个端点对外开放 / 收回
const toggleEndpoint = async (ep, exposed) => {
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: '请先登录', duration: 5000, offset: 20, showClose: true })
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
    if (!response.ok) throw new Error('更新失败')
    const data = await response.json()
    if (data.code === 200) {
      ep.exposed = exposed
      ElMessage.success({
        message: exposed ? '端点已对外开放' : '端点已收回对外',
        duration: 1200
      })
      notifyChanged(exposed)
    } else {
      ElMessage.error({ message: `更新失败: ${data.msg}`, duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error('切换端点状态失败:', error)
    ElMessage.error({ message: '更新失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
  }
}

// 删除单个端点
const deleteEndpoint = (ep) => {
  ElMessageBox.confirm(
    `确定要删除端点 "${ep.name}"（${(ep.protocol || '').toUpperCase()}）吗？`,
    '删除确认',
    {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    }
  ).then(async () => {
    try {
      const token = getToken()
      if (!token) {
        ElMessage.error({ message: '请先登录', duration: 5000, offset: 20, showClose: true })
        return
      }
      const response = await fetch(`${adminApi.svcEndpoints.delete}${ep.id}`, {
        method: 'DELETE',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        }
      })
      if (!response.ok) throw new Error('删除失败')
      const data = await response.json()
      if (data.code === 200) {
        ElMessage.success({ message: '端点删除成功', duration: 700 })
        await fetchEndpoints()
      } else {
        ElMessage.error({ message: `删除失败: ${data.msg}`, duration: 5000, offset: 20, showClose: true })
      }
    } catch (error) {
      console.error('删除端点失败:', error)
      ElMessage.error({ message: '删除失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
    }
  }).catch(() => {})
}

// 全部对外开放
const exposeAll = async () => {
  const retracted = endpoints.value.filter(ep => !ep.exposed)
  if (retracted.length === 0) {
    ElMessage.info({ message: '所有端点都已对外开放', duration: 700 })
    return
  }
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: '请先登录', duration: 5000, offset: 20, showClose: true })
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
      if (!response.ok) throw new Error('对外开放失败')
      const data = await response.json()
      if (data.code !== 200) throw new Error(data.msg || '对外开放失败')
    }
    ElMessage.success({ message: '已全部对外开放', duration: 1200 })
    await fetchEndpoints()
    notifyChanged(true)
  } catch (error) {
    console.error('全部对外开放失败:', error)
    ElMessage.error({ message: '对外开放失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
  }
}

// 全部收回
const retractAll = async () => {
  try {
    const token = getToken()
    if (!token) {
      ElMessage.error({ message: '请先登录', duration: 5000, offset: 20, showClose: true })
      return
    }
    const response = await fetch(adminApi.svcEndpoints.retract, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        target_type: 'agent',
        target_id: props.agent?.id
      })
    })
    if (!response.ok) throw new Error('收回失败')
    const data = await response.json()
    if (data.code === 200) {
      ElMessage.success({ message: '已全部收回', duration: 700 })
      await fetchEndpoints()
    } else {
      ElMessage.error({ message: `收回失败: ${data.msg}`, duration: 5000, offset: 20, showClose: true })
    }
  } catch (error) {
    console.error('全部收回失败:', error)
    ElMessage.error({ message: '收回失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
  }
}

// 面板拖拽调整宽度
const startResize = (e) => {
  const startX = e.clientX
  const startWidth = panelWidth.value

  const onMouseMove = (ev) => {
    panelWidth.value = Math.max(400, startWidth + (startX - ev.clientX))
  }
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }

  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// 点面板外部关闭
const onOutsideClick = (e) => {
  const target = e.target
  // 放过 Element Plus 浮层（确认框 / 下拉等 teleport 到 body 的元素）
  if (target.closest && (target.closest('.el-overlay') || target.closest('.el-select-dropdown') || target.closest('.el-popper'))) {
    return
  }
  if (panelRef.value && !panelRef.value.contains(target)) {
    emit('close')
  }
}

// 打开 / 关闭：打开时按 agent 重置视图并拉端点，关闭时摘掉外部点击监听。
// 放在所有声明之后：immediate 会在 setup 里同步调用 fetchEndpoints，提前会撞 TDZ。
watch(() => props.visible, async (visible) => {
  if (visible) {
    resetForm()
    endpoints.value = []
    showForm.value = false
    await fetchEndpoints()
    // 无端点时直接显示新增表单，有端点时显示列表
    showForm.value = endpoints.value.length === 0
    // nextTick 延迟绑定，避免打开按钮的 mousedown 立即触发关闭
    nextTick(() => {
      document.addEventListener('mousedown', onOutsideClick)
    })
  } else {
    document.removeEventListener('mousedown', onOutsideClick)
  }
}, { immediate: true })

onUnmounted(() => {
  document.removeEventListener('mousedown', onOutsideClick)
})
</script>

<style scoped>
/* ========== 端点面板 ========== */
/* ========== 端点面板 ========== */
.expose-panel {
  position: absolute;
  z-index: 3200; /* 与设置面板同级（编辑器容器内最高层） */
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
  /* 预留滚动条位置：内容高度变化（切区/搜索/收起展开）时不会因滚动条出现或消失而左右晃动 */
  scrollbar-gutter: stable;
}

.panel-tail {
  border-top: 0px solid #e0e0e0;
  display: flex;
  flex-direction: column;
  max-height: 45%;
  overflow-y: auto;
  background-color: #f9f9f9;
  border-radius: 10px;
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

/* 参数（只读）：智能体没有入参声明，只有一个说明 */
.arg-preview {
  width: 100%;
  padding: 8px 10px;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  background: #fafbfc;
}

.arg-empty {
  font-size: 12px;
  line-height: 1.5;
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

/* 协议彩色标签 */
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
</style>
