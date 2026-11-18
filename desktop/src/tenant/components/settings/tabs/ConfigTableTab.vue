<template>
  <div class="module-section">
    <div class="module-head">
      <h3>{{ title }}</h3>
      <div class="head-actions">
        <el-button size="small" :icon="Refresh" :loading="loading" @click="fetchConfigs">刷新</el-button>
        <el-button type="primary" size="small" :loading="saving" @click="saveConfigs">
          <el-icon><Check /></el-icon> 保存租户配置
        </el-button>
      </div>
    </div>
    <div v-loading="loading" class="flow-config-body">
      <div class="flow-config-table">
        <div class="flow-config-table-head">
          <span class="col-name">配置项</span>
          <span class="col-system">系统级</span>
          <span class="col-tenant">租户配置</span>
        </div>
        <!-- data-field 供设置页顶部搜索定位到具体配置项（gotoSetting 里的 querySelector） -->
        <div v-for="f in fields" :key="f.key" :data-field="f.key" class="flow-config-row">
          <span class="col-name">
            <span class="col-name-label">{{ f.label }}</span>
            <span v-if="f.desc" class="col-name-desc">{{ f.desc }}</span>
          </span>
          <span class="col-system">{{ formatValue(systemConfig[f.key]) }}</span>
          <el-input v-model="tenantConfig[f.key]" placeholder="留空回退系统级" size="small" />
        </div>
      </div>
      <div class="flow-config-hint">
        <slot name="hint" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Check, Refresh } from '@element-plus/icons-vue'
import { getToken } from '../../../utils/auth.js'

/**
 * 「系统级值 + 租户覆盖值」的配置表：工作流运行参数与智能体运行参数共用这一份。
 * 两张表交互完全一样，差别只有字段、接口、以及是否允许 -1（不限制）——
 * 抄成两份时最容易出的问题是校验规则慢慢走样（一边允许 -1，另一边忘了拦）。
 */
const props = defineProps({
  active: { type: Boolean, default: false },
  title: { type: String, required: true },
  fields: { type: Array, required: true },
  // { get, update } 两个地址
  api: { type: Object, required: true },
  // 允许 -1 表示"不限制"；不允许时校验 >= 1
  allowUnlimited: { type: Boolean, default: false }
})

const loading = ref(false)
const saving = ref(false)
const systemConfig = ref({})
const tenantConfig = ref({})

const formatValue = (v) => {
  if (v === null || v === undefined || v === '') return '未设置'
  return String(v)
}

const fetchConfigs = async () => {
  loading.value = true
  try {
    const token = getToken()
    if (!token) return
    const response = await fetch(props.api.get, { headers: { Authorization: `Bearer ${token}` } })
    if (!response.ok) return
    const data = await response.json()
    if (data.code === 200) {
      systemConfig.value = data.data.system || {}
      const t = data.data.tenant || {}
      const obj = {}
      for (const f of props.fields) {
        const v = t[f.key]
        obj[f.key] = (v === null || v === undefined) ? '' : String(v)
      }
      tenantConfig.value = obj
    }
  } catch (e) {
    console.error(`获取${props.title}失败:`, e)
  } finally {
    loading.value = false
  }
}

watch(() => props.active, (active) => {
  if (active) fetchConfigs()
}, { immediate: true })

const saveConfigs = async () => {
  const sys = systemConfig.value || {}
  for (const f of props.fields) {
    const raw = tenantConfig.value[f.key]
    if (raw === '' || raw === null || raw === undefined) continue
    const tVal = Number(raw)
    if (isNaN(tVal)) {
      // 工作流运行参数历史行为：非数字直接忽略（保存时置空），不打断其它项
      if (props.allowUnlimited) continue
      ElMessage.error({ message: `${f.label}必须填数字`, duration: 5000, offset: 20, showClose: true })
      return
    }
    if (!props.allowUnlimited && tVal < 1) {
      ElMessage.error({ message: `${f.label}必须 >= 1（本组配置不支持 -1 不限制）`, duration: 5000, offset: 20, showClose: true })
      return
    }
    // -1 = 不限制，跳过上限比较
    if (props.allowUnlimited && tVal === -1) continue
    const sysVal = sys[f.key]
    if (sysVal === null || sysVal === undefined) continue
    if (tVal > sysVal) {
      ElMessage.error({ message: `${f.label}不能超过系统级值 ${sysVal}`, duration: 5000, offset: 20, showClose: true })
      return
    }
  }

  const body = {}
  for (const f of props.fields) {
    const raw = tenantConfig.value[f.key]
    if (raw === '' || raw === null || raw === undefined) {
      body[f.key] = null
    } else {
      const n = Number(raw)
      body[f.key] = isNaN(n) ? null : n
    }
  }

  saving.value = true
  try {
    const token = getToken()
    if (!token) return
    const response = await fetch(props.api.update, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
      body: JSON.stringify(body)
    })
    if (!response.ok) throw new Error('保存失败')
    const data = await response.json()
    if (data.code === 200) {
      ElMessage.success({ message: `${props.title}保存成功`, duration: 700 })
      await fetchConfigs()
    } else {
      ElMessage.error({ message: `保存失败: ${data.msg}`, duration: 5000, offset: 20, showClose: true })
    }
  } catch (e) {
    console.error(`保存${props.title}失败:`, e)
    ElMessage.error({ message: '保存失败，请稍后重试', duration: 5000, offset: 20, showClose: true })
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

.flow-config-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* ===== 表格卡片化（与用户管理表格同一套观感）=====
   一行 = 一张卡片：容器是浅灰底，行之间靠 margin 露出底色当缝隙；
   表头是独立的白色卡片（白底 + 轻阴影），整块再浮一层外阴影。 */
.flow-config-table {
  background: #f5f7fa;
  border-radius: 8px;
  overflow: hidden;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06);
}

.flow-config-table-head,
.flow-config-row {
  display: grid;
  grid-template-columns: 1.3fr 1fr 1.5fr;
  align-items: center;
  gap: 12px;
  padding: 10px 16px;
}

.flow-config-table-head {
  background: #fff;
  /* 字号比正文略小一档，字重/字色对齐 el-table 表头：13px + 600 + #303133 */
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  box-shadow: 0 1px 3px rgba(15, 23, 42, 0.05);
}

/* 卡片本体：白底；上外边距露出容器底色，形成 4px 行间缝隙 */
.flow-config-row {
  background: #fff;
  margin-top: 4px;
}

/* 从顶部搜索跳过来时闪一下，否则用户看不出落在哪一行 */
.flow-config-row.field-flash {
  background-color: #fff8e6;
  transition: background-color 0.3s;
}

/* 悬停整行提亮，方便对行读数；正在闪烁提示的那行保持黄色 */
.flow-config-row:hover:not(.field-flash) {
  background-color: #f0f6ff;
}

.flow-config-row .col-name {
  font-size: 13px;
  color: #1f2329;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

/* 配置项说明：一行灰字，讲「这个数是干什么的」，与底部 hint（怎么填）分工 */
.flow-config-row .col-name-desc {
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
}

.flow-config-row .col-system {
  font-size: 13px;
  color: #606266;
}

/* 输入框内文字与表体同步，否则一行里会出现两种字号 */
.flow-config-row :deep(.el-input__inner) {
  font-size: 13px;
}

.flow-config-hint {
  font-size: 12px;
  color: #909399;
}
</style>
