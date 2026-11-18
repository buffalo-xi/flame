<template>
  <div class="module-section">
    <div class="module-head">
      <h3>全局变量</h3>
      <div class="head-actions">
        <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button type="primary" size="small" :icon="Plus" @click="openCreate">新建变量</el-button>
      </div>
    </div>

    <!-- 口径写死在这里：「全局」在多租户系统里最容易被读成「跨租户」。
         文案从 script 里取：模板里直接写 {{...}} 会被 Vue 当成插值解析。 -->
    <el-alert
      class="gv-tip"
      type="info"
      :closable="false"
      show-icon
      :title="SCOPE_TIP"
      :description="USAGE_TIP"
    />

    <div v-loading="loading" class="gv-grid">
      <div v-if="!loading && !items.length" class="gv-empty">
        还没有全局变量。先在右上角建一个，再到工作流的节点输入变量里把来源选为「全局变量」。
      </div>
      <div v-for="v in items" :key="v.name" class="gv-card">
        <div class="gv-card-top">
          <span class="gv-name" :title="v.name">{{ v.name }}</span>
          <div class="gv-card-actions">
            <el-button link type="primary" :icon="Edit" title="编辑" @click="openEdit(v)" />
            <el-button link type="danger" :icon="Delete" title="删除" @click="remove(v)" />
          </div>
        </div>
        <div class="gv-card-meta">
          <span class="gv-kind" :class="'gv-kind--' + v.kind">{{ kindLabel(v.kind) }}</span>
          <span v-if="v.kind === 'secret'" class="gv-state" :class="{ 'is-empty': !v.has_value }">
            {{ v.has_value ? '已设置' : '未设置' }}
          </span>
        </div>
        <div class="gv-value" :class="{ 'is-secret': v.kind === 'secret' }">{{ displayValue(v) }}</div>
        <div class="gv-desc" :title="v.description || ''">{{ v.description || '—' }}</div>
      </div>
    </div>

    <el-dialog
      v-model="dialogVisible"
      :title="editing ? '编辑全局变量' : '新建全局变量'"
      width="520px"
    >
      <el-form label-width="84px" @submit.prevent>
        <el-form-item label="变量名" required>
          <el-input
            v-model="draft.name"
            :disabled="editing"
            placeholder="如 DB_HOST（字母 / 数字 / 下划线，不能数字开头）"
          />
          <!-- 变量名就是引用名，直接把它在模板里的样子显示出来，免得用户猜 -->
          <div class="gv-hint">{{ templateHint }}</div>
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="draft.kind" style="width: 100%">
            <el-option label="文本（text）" value="text" />
            <el-option label="密钥（secret，接口不回显）" value="secret" />
            <el-option label="数字（number）" value="number" />
            <el-option label="布尔（bool）" value="bool" />
          </el-select>
        </el-form-item>
        <el-form-item label="值">
          <!-- 布尔只有真/假：给选择器而不是输入框 —— 手输 yes/on 后端虽然也认，
               但存进库里的值五花八门，排查时看不出到底是哪一种 -->
          <el-radio-group v-if="draft.kind === 'bool'" v-model="draft.value">
            <el-radio value="true">真（true）</el-radio>
            <el-radio value="false">假（false）</el-radio>
          </el-radio-group>
          <el-input
            v-else
            v-model="draft.value"
            :type="draft.kind === 'secret' ? 'password' : 'text'"
            :show-password="draft.kind === 'secret'"
            :placeholder="secretKeepPlaceholder"
          />
          <div v-if="draft.kind === 'secret'" class="gv-hint">
            密钥只在运行期读取，接口不会回显；也不要写进 AGENT.md / SKILL.md（会进模型上下文与运行记录）。
          </div>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="draft.description" placeholder="这个变量是干什么的（随资产包的需求清单带出）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Plus, Edit, Delete } from '@element-plus/icons-vue'
import adminApi from '../../../config/admin.js'
import { authHeaders, checkBizOk } from '../http.js'

const props = defineProps({
  // Settings.vue 用 v-show + active 懒加载：没切到这个页签就不发请求
  active: { type: Boolean, default: true }
})

const items = ref([])
const loading = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const editing = ref(false)

const draft = ref({ name: '', kind: 'text', value: '', description: '', has_value: false })

// 把库里的原始字符串收敛成 true/false（后端宽松解析，存量可能是 1 / yes / on）
const toBoolStr = (raw) =>
  ['', 'false', '0', 'no', 'off'].includes(String(raw).trim().toLowerCase()) ? 'false' : 'true'

// 切换类型时把值收敛成该类型的合法形态：从文本切到 bool 时旧值可能是 "hello"，
// 原样存下去后端 Resolve 会直接报错（布尔解析失败），等于让用户白填一遍。
watch(() => draft.value.kind, (kind) => {
  if (kind === 'bool') draft.value.value = toBoolStr(draft.value.value)
})

// 说明文案放这里而不是直接写进 template：里面有 {{ .input.x }} 与 ${global_vars.X}，
// 前者会被 Vue 当成插值解析（构建直接报 Unterminated string constant）。
const SCOPE_TIP = '全局 = 本租户内全局，不跨租户'
const USAGE_TIP =
  '工作流里：在节点的「输入变量」中新增一项，来源选「全局变量」，模板里用 {{ .input.变量名 }} 引用；' +
  '智能体 / 技能文档里写 ${global_vars.变量名}。' +
  '资产包只带引用名不带值，装到别的租户后在这里填一次即可。'

// 变量名 → 它被引用时的完整样子，让用户直接看到该写什么
const templateHint = computed(() => {
  const name = draft.value.name || '变量名'
  return `节点输入变量：source_type = global，source_key = ${name}；模板里写 {{ .input.${name} }}`
})

// secret 编辑时留空 = 不改原值（后端也是这个语义），占位文案要把它说清，
// 否则用户会以为「留空就是把密码清了」。
const secretKeepPlaceholder = computed(() =>
  editing.value && draft.value.kind === 'secret' && draft.value.has_value
    ? '留空表示不修改原值'
    : '变量值'
)

const KIND_LABELS = { text: '文本', secret: '密钥', number: '数字', bool: '布尔' }
const kindLabel = (k) => KIND_LABELS[k] || k

const displayValue = (v) => {
  if (v.kind === 'secret') return v.has_value ? '••••••' : '（未设置）'
  return v.value === '' ? '（空）' : v.value
}

const load = async () => {
  loading.value = true
  try {
    const res = await fetch(adminApi.globalVars.list, { headers: authHeaders() })
    const data = await checkBizOk(res)
    items.value = data.data?.items || []
  } catch (e) {
    ElMessage.error('读取全局变量失败：' + e.message)
  } finally {
    loading.value = false
  }
}

const openCreate = () => {
  editing.value = false
  draft.value = { name: '', kind: 'text', value: '', description: '', has_value: false }
  dialogVisible.value = true
}

const openEdit = (v) => {
  editing.value = true
  draft.value = {
    name: v.name,
    kind: v.kind,
    // 密钥不把掩码回填成值；bool 统一成 true/false，好让单选组能正确选中
    value: v.kind === 'secret' ? '' : (v.kind === 'bool' ? toBoolStr(v.value) : v.value),
    description: v.description || '',
    has_value: !!v.has_value
  }
  dialogVisible.value = true
}

const save = async () => {
  const name = draft.value.name.trim()
  if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name)) {
    ElMessage.warning('变量名只能用字母、数字、下划线，且不能以数字开头')
    return
  }
  // number 存的是字符串，后端解析失败会在运行期报错 —— 这里先挡一道
  if (draft.value.kind === 'number' && !/^-?\d+(\.\d+)?$/.test(String(draft.value.value).trim())) {
    ElMessage.warning('数字类型的值只能填数字')
    return
  }
  saving.value = true
  try {
    const res = await fetch(adminApi.globalVars.save, {
      method: 'POST',
      headers: authHeaders(true),
      body: JSON.stringify({
        name,
        kind: draft.value.kind,
        value: draft.value.value,
        description: draft.value.description
      })
    })
    await checkBizOk(res)
    // 时长由 utils/message.js 统一（成功 1s），这里不传 duration
    ElMessage.success(editing.value ? '已保存' : '已创建')
    dialogVisible.value = false
    load()
  } catch (e) {
    ElMessage.error('保存失败：' + e.message)
  } finally {
    saving.value = false
  }
}

// 后端 usage 接口的 kind → 中文（与 admin/globalvars/usage.go 的 UsageHit 对齐）
const HIT_KIND = { flow: '工作流', agent: '智能体', skill: '技能', agent_doc: '文档', skill_doc: '文档' }

// 先查引用面再问确认：变量被删后引用方要到运行期才失败，
// 「会坏掉谁」只有删除这一刻能说清楚。
const remove = async (v) => {
  let hits = []
  try {
    const res = await fetch(adminApi.globalVars.usage(v.name), { headers: authHeaders() })
    const data = await checkBizOk(res)
    hits = data.data?.items || []
  } catch (e) {
    // 查不到不阻断：后端删除时还会再拦一次，这里的提示只是提前告知
    ElMessage.warning('查询引用面失败：' + e.message + '（删除时后端还会再拦一次）')
  }

  const where = hits.slice(0, 5).map((h) => `${HIT_KIND[h.kind] || h.kind}「${h.name}」`).join('、')
  const more = hits.length > 5 ? ` 等 ${hits.length} 处` : ''
  const text = hits.length
    ? `「${v.name}」仍被引用：${where}${more}。删除后这些地方运行时会直接失败，仍要删除？`
    : `删除全局变量「${v.name}」？引用它的工作流 / 智能体在运行时会直接失败（不会静默用空值）。`

  try {
    await ElMessageBox.confirm(text, '删除全局变量', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  try {
    // force=1：用户已经在上面看过引用清单并确认
    const res = await fetch(adminApi.globalVars.remove(v.name, true), {
      method: 'DELETE',
      headers: authHeaders()
    })
    await checkBizOk(res)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    ElMessage.error('删除失败：' + e.message)
  }
}

onMounted(() => {
  if (props.active) load()
})

watch(() => props.active, (on) => {
  if (on && !items.value.length) load()
})
</script>

<style scoped>
/* 与其它设置页一致的外壳（用户管理 / 服务凭证 / 绑定… 各自 scoped 里都有一份）：
   白底圆角卡 + 头部两端对齐 + 按钮间距走 gap，并清掉 EP 相邻按钮自带的 12px 左间距。
   这套样式不在 Settings.vue 里 —— scoped 不会下发到子组件。 */
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

.gv-tip {
  margin-bottom: 12px;
}

/* 固定列宽 + 左对齐：容器再宽也不把卡片抻长（minmax(..., 1fr) 会拉满，
   两三个变量时会变成两三个大横条，反而更难扫） */
.gv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, 240px);
  justify-content: start;
  gap: 10px;
  min-height: 90px;
}

.gv-empty {
  grid-column: 1 / -1;
  padding: 28px;
  border: 1px dashed #dcdfe6;
  border-radius: 10px;
  text-align: center;
  font-size: 13px;
  color: #909399;
}

.gv-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  padding: 12px 14px;
  background: #fff;
  border: 1px solid #e8eaed;
  border-radius: 10px;
  transition: box-shadow 0.2s ease, border-color 0.2s ease;
}

.gv-card:hover {
  border-color: #c6e2ff;
  box-shadow: 0 2px 10px rgba(64, 158, 255, 0.12);
}

.gv-card-top {
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 变量名就是引用名：等宽 + 省略，名字再长也不会把操作按钮挤走 */
.gv-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

.gv-card-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
}

.gv-card-actions :deep(.el-button) {
  height: 20px;
  padding: 0 3px;
}

.gv-card-meta {
  display: flex;
  align-items: center;
  gap: 6px;
}

/* 类型用色块而不是 el-tag：卡片本身很轻，tag 的边框会显得碎 */
.gv-kind {
  padding: 0 6px;
  font-size: 11px;
  line-height: 16px;
  border-radius: 4px;
  color: #fff;
  background: #409eff;
}

.gv-kind--secret { background: #e6a23c; }
.gv-kind--number { background: #67c23a; }
.gv-kind--bool { background: #909399; }

.gv-state { font-size: 11px; color: #67c23a; }
.gv-state.is-empty { color: #c0c4cc; }

.gv-value {
  overflow: hidden;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  color: #606266;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.gv-value.is-secret {
  letter-spacing: 1px;
  color: #909399;
}

.gv-desc {
  display: -webkit-box;
  min-height: 18px;
  overflow: hidden;
  font-size: 12px;
  line-height: 1.5;
  color: #909399;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.gv-hint {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.5;
  color: #909399;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
