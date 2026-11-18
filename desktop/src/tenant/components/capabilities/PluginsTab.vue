<template>
  <div class="plug-wrap">
  <div class="pv">
    <!-- 顶部：计数 + 两个动作 -->
    <div class="pv-bar">
      <div class="pv-summary">
        共 <strong>{{ items.length }}</strong> 个插件工具 · <strong>{{ activeCount }}</strong> 个已启用
        <span v-if="refreshedAt" class="pv-muted">清单刷新于 {{ formatTime(refreshedAt) }}</span>
      </div>
      <div class="pv-actions">
        <el-button size="small" :loading="loading" @click="load">刷新列表</el-button>
        <el-button size="small" type="primary" :loading="rescanning" @click="rescan">重扫磁盘</el-button>
      </div>
    </div>

    <div v-if="loadError" class="pv-error">{{ loadError }}</div>
    <div v-if="refreshError" class="pv-error">{{ refreshError }}</div>

    <div v-if="!items.length && !loading" class="pv-empty">
      还没有发现任何插件。把插件放进 <code>{root_dir}/plugins/tools/&lt;name&gt;/</code>
      （<code>tool.py</code> + 至少一个实现目录），再点「重扫磁盘」。
    </div>

    <div class="pv-grid">
      <div v-for="p in items" :key="p.name" class="pv-card" :class="{ on: p.is_active }">
        <div class="pv-card-head">
          <div class="pv-card-title">
            {{ p.display_name || p.name }}
            <span class="ref-tag">{{ p.name }}</span>
            <span class="ref-tag" :class="p.allowed ? (p.is_active ? 'ok' : '') : 'off'">
              {{ p.blocked ? '平台全局停用' : !p.allowed ? '平台已停用' : p.is_active ? '已启用' : '未启用' }}
            </span>
          </div>
          <el-switch
            :model-value="p.is_active"
            :disabled="!p.allowed"
            :loading="busy === p.name"
            :title="p.allowed ? '' : p.blocked ? '平台已全局停用该插件' : '平台已对本租户停用该插件'"
            @change="(v) => toggle(p, v)"
          />
        </div>

        <div class="pv-card-desc" :title="p.description">{{ p.description || '无描述' }}</div>

        <div class="pv-card-meta">
          <span>{{ (p.implementations || []).length }} 个实现</span>
          <span v-if="p.impl">主实现：{{ implLabel(p, p.impl) }}</span>
          <span v-else class="pv-warn">未选择实现</span>
          <span v-if="(p.fallbacks || []).length">
            兜底：{{ p.fallbacks.map((n) => implLabel(p, n)).join(' → ') }}
          </span>
        </div>

        <!-- 平台策略：只读。缺省开放，只有平台显式停用才需要提示（本站改不了）。 -->
        <div v-if="p.blocked || !p.allowed" class="pv-card-meta">
          <span v-if="p.blocked" class="pv-warn">平台已全局停用该插件，需平台管理员解除</span>
          <span v-else class="pv-warn">平台已对本租户停用该插件，需平台管理员恢复</span>
        </div>

        <div v-if="scanErrorOf(p.name)" class="pv-card-error">{{ scanErrorOf(p.name) }}</div>

        <div class="pv-card-actions">
          <el-button size="small" text @click="openEditor(p)">配置</el-button>
        </div>
      </div>
    </div>

    <!-- 配置面板：与技能编辑页的设置面板同款 —— 右侧滑出、无遮罩、左缘可拖宽 -->
    <div v-if="editing" ref="panelRef" class="settings-panel" :style="{ width: panelWidth + 'px' }">
      <div class="panel-resizer" @mousedown="startResize"></div>
      <div class="panel-header">
        <div class="panel-title">
          配置插件 · {{ editing.display_name || editing.name }}
          <span class="ref-tag">{{ editing.name }}</span>
        </div>
        <span v-if="formDirty" class="dirty-tip">未保存</span>
        <el-button size="small" type="primary" :loading="saving" @click="save">保存</el-button>
        <span class="close-icon" @click="closeEditor">&#xd7;</span>
      </div>

      <div class="panel-body">
        <!-- 标题在 header 上，这里只放状态与描述，避免同一个名字出现两次 -->
        <div class="panel-head-line">
          <span class="ref-tag" :class="editing.allowed ? (editing.is_active ? 'ok' : '') : 'off'">
            {{ editing.blocked ? '平台全局停用' : !editing.allowed ? '平台已停用' : editing.is_active ? '已启用' : '未启用' }}
          </span>
          <span class="panel-desc">{{ editing.description || '无描述' }}</span>
        </div>
        <div class="panel-meta">
          {{ (editing.implementations || []).length }} 个实现
        </div>
        <div v-if="editing.blocked" class="panel-warn">平台已全局停用该插件，需平台管理员解除后才能启用。</div>
        <div v-else-if="!editing.allowed" class="panel-warn">平台已对本租户停用该插件，需平台管理员恢复后才能启用。</div>
        <div v-if="scanErrorOf(editing.name)" class="panel-warn">{{ scanErrorOf(editing.name) }}</div>

        <!-- 主实现 -->
        <div class="pv-field">
          <div class="pv-label">主实现</div>
          <el-select v-model="form.impl" size="small" style="width: 100%" @change="ensureDefaults">
            <el-option
              v-for="im in editing.implementations || []"
              :key="im.name"
              :label="im.display_name || im.name"
              :value="im.name"
            />
          </el-select>
          <div class="pv-hint">模型只看得到 <code>{{ editing.name }}</code> 这一个工具，换实现不影响工具名与参数。</div>
        </div>

        <!-- 该实现的配置 -->
        <div class="pv-field">
          <div class="pv-label">实现配置</div>
          <div v-if="!fields.length" class="pv-hint">该实现不需要配置。</div>
          <div v-for="f in fields" :key="f.key" class="pv-cfg-row">
            <div class="pv-cfg-label">
              {{ f.label || f.key }}
              <span v-if="f.required" class="pv-req">*</span>
            </div>
            <el-switch
              v-if="f.type === 'bool'"
              :model-value="!!cfgValue(f.key)"
              @update:model-value="(v) => setCfg(f.key, v)"
            />
            <el-input-number
              v-else-if="f.type === 'int'"
              :model-value="Number(cfgValue(f.key) ?? f.default ?? 0)"
              :min="0"
              size="small"
              style="width: 160px"
              @update:model-value="(v) => setCfg(f.key, v)"
            />
            <el-input
              v-else
              :model-value="cfgValue(f.key) ?? ''"
              :type="f.secret ? 'password' : 'text'"
              :show-password="!!f.secret"
              size="small"
              :placeholder="f.secret ? '已保存（留空/打码表示不改）' : (f.default ?? '')"
              @update:model-value="(v) => setCfg(f.key, v)"
            />
            <div v-if="f.help" class="pv-hint">{{ f.help }}</div>
          </div>
        </div>

        <!-- 兜底链 -->
        <div class="pv-field">
          <div class="pv-label">兜底链</div>
          <div class="pv-hint">主实现失败（鉴权 / 限流 / 网络）时按顺序再试；缺 key 这类配置错误不会降级。</div>
          <div v-for="(name, i) in form.fallbacks" :key="name" class="pv-chain-row">
            <span class="pv-chain-name">{{ implLabel(editing, name) }}</span>
            <el-button text size="small" :disabled="i === 0" @click="moveFallback(i, -1)">上移</el-button>
            <el-button text size="small" :disabled="i === form.fallbacks.length - 1" @click="moveFallback(i, 1)">下移</el-button>
            <el-button text size="small" @click="form.fallbacks.splice(i, 1)">移除</el-button>
          </div>
          <el-select
            v-if="addableImpls.length"
            :model-value="''"
            placeholder="添加兜底实现"
            size="small"
            style="width: 220px"
            @change="(v) => { if (v) form.fallbacks.push(v) }"
          >
            <el-option v-for="im in addableImpls" :key="im.name" :label="im.display_name || im.name" :value="im.name" />
          </el-select>
          <div v-if="!addableImpls.length" class="pv-hint">没有可再添加的实现。</div>
        </div>

        <!-- 试一次 -->
        <div class="pv-field">
          <div class="pv-label">测试连接</div>
          <div class="pv-test-row">
            <el-input v-model="testQuery" size="small" placeholder="检索词" style="flex: 1" />
            <el-button size="small" :loading="testing" @click="runTest">试一次</el-button>
          </div>
          <div v-if="testError" class="pv-test-error">{{ testError }}</div>
          <div v-if="testResult" class="pv-test-result">
            <div class="pv-test-meta">
              实现 {{ testResult.provider || form.impl }} · 耗时 {{ testResult.elapsed_ms }}ms
            </div>
            <div v-for="(r, i) in testResult.results || []" :key="i" class="pv-test-item">
              <div class="pv-test-title">{{ r.title || '(无标题)' }}</div>
              <div class="pv-test-url">{{ r.url }}</div>
              <div class="pv-test-snippet">{{ r.snippet }}</div>
            </div>
            <div v-if="testResult.answer" class="pv-test-answer">{{ testResult.answer }}</div>
            <div v-if="(testResult.notes || []).length" class="pv-hint">{{ testResult.notes.join('；') }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { useEscClose } from '../../composables/useEscClose'

const emit = defineEmits(['count'])

const items = ref([])
const scanErrors = ref([])
const refreshedAt = ref('')
const refreshError = ref('')
const loading = ref(false)
const rescanning = ref(false)
const busy = ref('')
const loadError = ref('')

// 配置面板（与技能编辑页的设置面板同款）：editing 非空即打开
const editing = ref(null)
const saving = ref(false)
const form = reactive({ impl: '', fallbacks: [], configs: {}, is_active: false })

// 面板：宽度（可拖，300–720）/ 元素引用（判断「点在外面」）
const panelRef = ref(null)
const panelWidth = ref(420)

// 有没有未保存的改动：拿打开面板那一刻的快照做基线。
// 不能直接和列表里的原始行比 —— ensureDefaults 会把默认值补进表单，
// 那样"刚打开"就被判成脏，用户会以为改过东西。
const baseline = ref('')
const formSnapshot = () => JSON.stringify({ impl: form.impl, fallbacks: form.fallbacks, configs: form.configs })
const formDirty = computed(() => !!editing.value && baseline.value !== formSnapshot())

const testQuery = ref('flame 插件')
const testing = ref(false)
const testResult = ref(null)
const testError = ref('')

const authHeaders = () => ({ Authorization: `Bearer ${getToken()}`, 'Content-Type': 'application/json' })

const activeCount = computed(() => items.value.filter((p) => p.is_active).length)
const formatTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')

const implLabel = (p, name) => {
  const im = (p.implementations || []).find((x) => x.name === name)
  return im ? (im.display_name || im.name) : name
}

// 扫描错误是按路径记的（tools/web_search/tavily），卡片上只显示与当前工具相关的
const scanErrorOf = (toolName) => {
  const hit = scanErrors.value.filter((e) => String(e.path || '').endsWith('/' + toolName) || String(e.path || '').includes('/' + toolName + '/'))
  return hit.length ? hit.map((e) => `${e.path}: ${e.error}`).join('；') : ''
}

const load = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const res = await fetch(adminApi.plugins.list, { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '读取插件清单失败')
    items.value = body.data?.plugins || []
    scanErrors.value = body.data?.errors || []
    refreshedAt.value = body.data?.refreshed_at || ''
    refreshError.value = body.data?.refresh_error || ''
    // 平台策略表读不到时后端按「已停用」处理并回带原因，这里照实提示
    const policyErr = body.data?.policy_error
    if (policyErr) loadError.value = `平台插件策略读取失败（当前按「已停用」展示）：${policyErr}`
    // 面板开着时刷新列表：把 editing 换成新对象，状态徽标与额度才不会过期
    // （表单是独立副本，用户已填的内容不受影响）
    if (editing.value) {
      const fresh = items.value.find((x) => x.name === editing.value.name)
      if (fresh) editing.value = fresh
    }
  } catch (e) {
    loadError.value = e.message || '读取插件清单失败'
  } finally {
    loading.value = false
    emit('count', items.value.length)
  }
}

const rescan = async () => {
  rescanning.value = true
  try {
    const res = await fetch(adminApi.plugins.refresh, { method: 'POST', headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '重扫失败')
    ElMessage.success(`已重扫：${body.data?.tools ?? 0} 个工具`)
    await load()
  } catch (e) {
    ElMessage.error(e.message || '重扫失败')
  } finally {
    rescanning.value = false
  }
}

// 开关只改启用状态：configs 传空对象 = 不动任何配置
const toggle = async (p, isActive) => {
  // 开关在平台停用时是 disabled 的，这里再挡一道：执行层本来也会拒，
  // 但没必要让用户看到「保存成功却调不动」。
  if (!p.allowed) {
    ElMessage.warning(p.blocked ? '该插件已被平台全局停用，请联系平台管理员' : '该插件已被平台对本租户停用，请联系平台管理员')
    return
  }
  busy.value = p.name
  try {
    const res = await fetch(adminApi.plugins.update(p.name), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ impl: p.impl || '', fallbacks: p.fallbacks || [], configs: {}, is_active: isActive })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '保存失败')
    ElMessage.success(isActive ? '已启用' : '已停用')
    await load()
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    busy.value = ''
  }
}

const openEditor = (p) => {
  editing.value = p
  form.impl = p.impl || p.implementations?.[0]?.name || ''
  form.fallbacks = [...(p.fallbacks || [])]
  // 深拷一份：configs 按实现分键，编辑时不要直接改列表里的对象
  form.configs = JSON.parse(JSON.stringify(p.configs || {}))
  form.is_active = p.is_active
  testResult.value = null
  testError.value = ''
  ensureDefaults()
  // 快照必须在 ensureDefaults 之后取（默认值是表单的一部分，不算改动）
  baseline.value = formSnapshot()
}

const closeEditor = async () => {
  if (formDirty.value) {
    try {
      await ElMessageBox.confirm('配置有未保存的修改，确定关闭？', '关闭配置', { type: 'warning' })
    } catch {
      return
    }
  }
  editing.value = null
}

// ESC 关面板：走 closeEditor，未保存的改动照样先问一句
useEscClose(() => !!editing.value, () => closeEditor())

// 左缘拖拽调宽：与技能设置面板同款手感（越往左拖越宽）
const startResize = (e) => {
  const startX = e.clientX
  const startW = panelWidth.value
  const onMove = (ev) => {
    panelWidth.value = Math.max(300, Math.min(startW + (startX - ev.clientX), 720))
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}

// 点面板外部关闭（与智能体 / 技能设置面板同款口径）：
// 忽略 Element Plus 浮层，也忽略列表卡片上的「配置」按钮 —— 那是切换到另一个插件，
// 不该先弹一次「有未保存修改」的确认。
const onDocMouseDown = (e) => {
  if (!editing.value) return
  const t = e.target
  if (t.closest && (t.closest('.el-popper') || t.closest('.el-select-dropdown') || t.closest('.el-overlay'))) return
  if (t.closest && t.closest('.pv-card-actions')) return
  if (panelRef.value && panelRef.value.contains(t)) return
  closeEditor()
}

const fields = computed(() => {
  const im = (editing.value?.implementations || []).find((x) => x.name === form.impl)
  return im?.config_fields || []
})

const cfgValue = (key) => (form.configs[form.impl] || {})[key]

const setCfg = (key, value) => {
  if (!form.configs[form.impl]) form.configs[form.impl] = {}
  form.configs[form.impl][key] = value
}

// 切换实现时把该实现的默认值补进本地表单（secret 不预填，留空 = 用已保存的）
const ensureDefaults = () => {
  if (!form.impl) return
  if (!form.configs[form.impl]) form.configs[form.impl] = {}
  const box = form.configs[form.impl]
  for (const f of fields.value) {
    if (f.default === undefined || f.default === null) continue
    if (box[f.key] === undefined || box[f.key] === '') box[f.key] = f.default
  }
}

const addableImpls = computed(() => {
  const used = new Set([form.impl, ...form.fallbacks])
  return (editing.value?.implementations || []).filter((im) => !used.has(im.name))
})

const moveFallback = (i, delta) => {
  const j = i + delta
  if (j < 0 || j >= form.fallbacks.length) return
  const arr = form.fallbacks
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

const runTest = async () => {
  if (!editing.value) return
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    const res = await fetch(adminApi.plugins.test(editing.value.name), {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({
        impl: form.impl,
        query: testQuery.value,
        configs: form.configs[form.impl] || {}
      })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '测试失败')
    if (!body.data?.ok) throw new Error(`${body.data?.error || '测试失败'}${body.data?.error_code ? `（${body.data.error_code}）` : ''}`)
    testResult.value = body.data
  } catch (e) {
    testError.value = e.message || '测试失败'
  } finally {
    testing.value = false
  }
}

const save = async () => {
  if (!editing.value) return
  saving.value = true
  try {
    const res = await fetch(adminApi.plugins.update(editing.value.name), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({
        impl: form.impl,
        fallbacks: form.fallbacks,
        configs: form.configs,
        is_active: form.is_active
      })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(body.msg || '保存失败')
    ElMessage.success('已保存')
    // 保存后直接收起面板回到列表：插件的配置是一次性表单，不像技能设置那样边改边生效
    editing.value = null
    await load()
  } catch (e) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  load()
  document.addEventListener('mousedown', onDocMouseDown)
})

onUnmounted(() => {
  document.removeEventListener('mousedown', onDocMouseDown)
})
</script>

<style scoped>
/* 面板以本容器为定位基准（absolute inset 的上下边界都取这里），
   缺了 position 会落到 .capabilities 上，把顶部 tab 条一起盖住 */
.plug-wrap {
  position: relative;
  height: 100%;
}

.pv {
  padding: 0 20px 20px;
  overflow-y: auto;
  height: 100%;
  box-sizing: border-box;
  /* 与面板同口径：滚动条常驻占位、平时透明，鼠标进来才上色 */
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.pv::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.pv::-webkit-scrollbar-track {
  background: transparent;
}

.pv::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.plug-wrap:hover .pv {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.plug-wrap:hover .pv::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.pv-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.pv-summary { color: #666; font-size: 13px; }
.pv-muted { color: #9aa0a6; margin-left: 8px; }
.pv-actions { display: flex; gap: 8px; }

.pv-error {
  margin-bottom: 12px;
  padding: 8px 12px;
  background: #fef0f0;
  border: 1px solid #fde2e2;
  border-radius: 6px;
  color: #c45656;
  font-size: 13px;
}

.pv-empty {
  padding: 40px 12px;
  text-align: center;
  color: #9aa0a6;
  font-size: 13px;
  line-height: 1.9;
}

/* 卡片刻意做小一号（与自定义工具卡同款）：一屏多看几个 */
.pv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(272px, 1fr));
  gap: 8px;
  /* 每张卡按自身内容取高，不被同一行最高的那张拉齐 —— 描述长短不一时空白更少 */
  align-items: start;
}

.pv-card {
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 8px;
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.pv-card.on { border-color: #a7d8c2; box-shadow: 0 1px 6px rgba(0, 112, 74, 0.08); }

.pv-card-head { display: flex; align-items: center; justify-content: space-between; gap: 6px; }
.pv-card-title { font-weight: 600; font-size: 13px; color: #222; display: flex; align-items: center; gap: 5px; flex-wrap: wrap; }
/* 描述最多两行，超出裁掉；全文靠 title 悬停看 —— 卡片是速览，不是详情页 */
.pv-card-desc {
  color: #666;
  font-size: 12px;
  line-height: 1.45;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
}
.pv-card-meta { display: flex; flex-wrap: wrap; gap: 8px; color: #888; font-size: 11px; }
.pv-warn { color: #e6a23c; }
.pv-card-error { color: #c45656; font-size: 12px; line-height: 1.5; }
.pv-card-actions { display: flex; justify-content: flex-end; gap: 2px; }
/* 操作按钮跟着卡片缩：默认 small 高 32px，会把卡片撑回原来的高度 */
.pv-card-actions :deep(.el-button) { height: 24px; padding: 0 8px; font-size: 12px; }

.ref-tag {
  font-size: 11px;
  color: #666;
  background: #f2f3f5;
  border-radius: 4px;
  padding: 1px 6px;
  font-weight: 400;
}
.ref-tag.ok { color: #00704a; background: #e6f2ec; }
.ref-tag.off { color: #c45656; background: #fdecec; }

/* 配置面板里的表单块 */
.pv-field { display: flex; flex-direction: column; gap: 6px; }
.pv-label { font-weight: 600; font-size: 13px; color: #333; }
.pv-hint { color: #9aa0a6; font-size: 12px; line-height: 1.6; }
.pv-cfg-row { display: flex; flex-direction: column; gap: 4px; margin-bottom: 4px; }
.pv-cfg-label { font-size: 13px; color: #555; }
.pv-req { color: #f56c6c; }
.pv-chain-row {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  background: #fafafa;
  border-radius: 6px;
  margin-bottom: 6px;
}
.pv-chain-name { flex: 1; font-size: 13px; }
.pv-test-row { display: flex; gap: 8px; align-items: center; }
.pv-test-error { color: #c45656; font-size: 12px; margin-top: 6px; white-space: pre-wrap; }
.pv-test-result { margin-top: 10px; display: flex; flex-direction: column; gap: 8px; }
.pv-test-meta { color: #00704a; font-size: 12px; }
.pv-test-item { border-left: 2px solid #e6f2ec; padding-left: 8px; }
.pv-test-title { font-size: 13px; font-weight: 500; color: #222; }
.pv-test-url { font-size: 11px; color: #1890ff; word-break: break-all; }
.pv-test-snippet { font-size: 12px; color: #666; line-height: 1.6; }
.pv-test-answer {
  font-size: 12px;
  color: #333;
  background: #f7f7f7;
  border-radius: 6px;
  padding: 8px;
  line-height: 1.6;
}

/* ============ 配置面板：对齐技能编辑页的设置面板 ============
   右侧滑出、无遮罩（可以直接看到背后的卡片列表）、左缘可拖宽。 */
.settings-panel {
  position: absolute;
  z-index: 100;
  right: 0;
  top: 0;
  bottom: 0;
  background: #fff;
  border-radius: 6px 0 0 6px;
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
  gap: 12px;
  border-bottom: 1px solid #f1f1f1;
}

/* 面板标题：单页形态不需要 tab，标题直接说清在配哪个插件 */
.panel-title {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  font-size: 14px;
  font-weight: 600;
  color: #333;
}

.dirty-tip {
  font-size: 12px;
  color: #e6a23c;
  white-space: nowrap;
}

.close-icon {
  cursor: pointer;
  padding: 4px;
  border-radius: 4px;
  font-size: 18px;
  line-height: 1;
}

.close-icon:hover {
  background: rgba(0, 0, 0, 0.1);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  min-height: 0;
  /* 常驻占位：滚动条位置一直留着（不会被内容撑出来/缩回去导致横向跳动），
     只是平时透明，鼠标进面板才上色 —— 与右侧详情卡那套一致 */
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.panel-body::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.panel-body::-webkit-scrollbar-track {
  background: transparent;
}

.panel-body::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.settings-panel:hover .panel-body {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.settings-panel:hover .panel-body::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.panel-body .pv-field {
  margin-bottom: 18px;
}

.panel-head-line {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

/* 描述单行省略：面板可能被拖窄，长描述不该把状态徽标挤下去 */
.panel-desc {
  flex: 1;
  min-width: 0;
  color: #666;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.panel-meta {
  color: #888;
  font-size: 12px;
  margin-top: 6px;
  margin-bottom: 14px;
}

.panel-warn {
  color: #e6a23c;
  font-size: 12px;
  line-height: 1.6;
  margin-top: 6px;
}
</style>
