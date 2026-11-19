<template>
  <!-- 跨服务器市场：左 = 我配的对端站点（我去拉），右 = 该站的资产目录（逐条导入）。
       本页只管「我去拉别人」；「允许谁拉我」（匿名访问开关 + 出口令牌）
       在「本地市场 → 对外接入」里，两个方向分开放，免得混在一起误读。 -->
  <div class="section">
    <div class="split">
      <div class="list-pane">
        <div class="lp-bar">
          <span class="lp-count">{{ t('platform.market.peerCount', { count: peers.length }) }}</span>
          <div class="lp-bar-right">
            <el-button size="small" type="primary" @click="openPeerForm()">
              <el-icon><Plus /></el-icon>
              {{ t('platform.market.newPeer') }}
            </el-button>
          </div>
        </div>

        <!-- 卡片而不是表格：左栏被右侧 560px 目录压着，四列里「状态」这列最需要空间，
             压成三行之后错误原因不用再靠 tooltip 才能看全 -->
        <div v-loading="loadingPeers" class="cards">
          <div
            v-for="p in peers"
            :key="p.id"
            class="peer-card"
            :class="{ 'is-active': activePeer && activePeer.id === p.id }"
            @click="selectPeer(p)"
          >
            <div class="pc-top">
              <span class="pc-name" :title="p.name">{{ p.name }}</span>
              <el-tag size="small" :type="p.install_policy === 'auto' ? 'warning' : 'info'" effect="plain">
                {{ p.install_policy === 'auto' ? t('platform.market.autoApprove') : t('platform.market.needReview') }}
              </el-tag>
            </div>
            <div class="pc-url mono" :title="p.base_url">{{ p.base_url }}</div>
            <div class="pc-state">
              <span v-if="p.last_error" class="err" :title="p.last_error">{{ p.last_error }}</span>
              <span v-else class="muted">
                {{ p.last_sync_at ? t('platform.market.lastSync', { time: fmtTime(p.last_sync_at) }) : t('platform.market.neverPulled') }}
              </span>
              <span v-if="!p.enabled" class="muted">{{ t('platform.market.disabled') }}</span>
              <span v-if="p.auth_mode === 'none'" class="muted">{{ t('platform.market.publicMarket') }}</span>
              <span v-else-if="!p.has_token" class="err">{{ t('platform.market.noToken') }}</span>
            </div>
            <div class="pc-actions">
              <el-button link type="primary" size="small" @click.stop="openPeerForm(p)">{{ t('common.edit') }}</el-button>
              <el-button link type="danger" size="small" @click.stop="removePeer(p)">{{ t('common.delete') }}</el-button>
            </div>
          </div>
          <el-empty v-if="!peers.length && !loadingPeers" :description="t('platform.market.emptyPeers')" />
        </div>

        <div class="lp-foot">
          {{ t('platform.market.footHint') }}
        </div>
      </div>

      <section class="detail-pane">
        <el-empty v-if="!activePeer" :description="t('platform.market.selectPeer')" />

        <template v-else>
          <div class="d-head">
            <div class="d-title">
              <span class="d-name">{{ activePeer.name }}</span>
              <span class="mono d-schema">{{ activePeer.base_url }}</span>
            </div>
            <el-button size="small" :loading="loadingCatalog" @click="loadCatalog">{{ t('platform.market.pullCatalog') }}</el-button>
          </div>

          <div v-if="activePeer.last_error" class="warn">{{ activePeer.last_error }}</div>

          <div class="hint" style="margin: 10px 0">
            {{ t('platform.market.hintPrefix') }}<strong>{{ t('platform.market.hintApproved') }}</strong>{{ t('platform.market.hintMid') }}
            <strong>{{ activePeer.install_policy === 'auto' ? t('platform.market.hintAutoStatus') : t('platform.market.hintReviewStatus') }}</strong>
            {{ activePeer.install_policy === 'auto' ? t('platform.market.hintAutoTail') : t('platform.market.hintReviewTail') }}
          </div>

          <!-- 目录筛选：全在本地过滤（见脚本里 catFilters 上面的说明） -->
          <div class="cat-bar">
            <button
              v-for="k in KIND_FILTERS"
              :key="'ck-' + k.value"
              class="cat-chip"
              :class="{ active: catFilters.kind === k.value }"
              @click="catFilters.kind = k.value"
            >{{ k.label }}</button>
            <span class="cat-sep"></span>
            <button
              v-for="s in IMPORT_FILTERS"
              :key="'ci-' + s.value"
              class="cat-chip"
              :class="{ active: catFilters.imported === s.value }"
              :title="catFilters.imported === s.value ? t('platform.market.cancelFilterTip') : ''"
              @click="toggleImport(s.value)"
            >{{ s.label }}</button>
            <el-input
              v-model="catFilters.q"
              class="cat-search"
              size="small"
              clearable
              :placeholder="t('platform.market.searchPlaceholder')"
            >
              <template #prefix><el-icon><Search /></el-icon></template>
            </el-input>
            <span class="cat-total">{{ filteredCatalog.length }} / {{ catalog.length }}</span>
          </div>

          <!-- 目录也用卡片：一列「资产名 / name / 本机状态 + 导入」压成三行，
               比五列表格更省字，导入按钮跟状态在同一行不用来回扫 -->
          <div v-loading="loadingCatalog" class="cat-cards">
            <div v-for="row in filteredCatalog" :key="row.id" class="cat-card">
              <div class="cc-top">
                <span class="kind" :class="'kind--' + row.kind">{{ kindLabel(row.kind) }}</span>
                <span class="cc-name" :title="row.display_name || row.name">
                  {{ row.display_name || row.name }}
                </span>
                <span class="muted cc-ver">v{{ row.build }}</span>
              </div>
              <div class="cc-sub mono" :title="row.name">{{ row.name }}</div>
              <div class="cc-desc">{{ row.description || t('platform.market.noDescription') }}</div>
              <div class="cc-foot">
                <!-- 对方版本更高时标成橙色并写出目标版本：不然「已导入 v1」看着一切正常 -->
                <el-tag
                  v-if="row.imported"
                  size="small"
                  :type="hasUpdate(row) ? 'warning' : 'success'"
                  effect="plain"
                >
                  {{ t('platform.market.importedVersion', { version: row.local?.build }) }}<template v-if="hasUpdate(row)"> → v{{ row.build }}</template>
                </el-tag>
                <span v-else class="muted">{{ t('platform.market.notImported') }}</span>
                <el-button
                  link
                  type="primary"
                  size="small"
                  :loading="importingId === row.id"
                  @click="importAsset(row)"
                >{{ row.imported ? t('platform.market.overwriteImport') : t('assetImport.import') }}</el-button>
              </div>
            </div>
            <el-empty
              v-if="!filteredCatalog.length && !loadingCatalog"
              :description="catalog.length ? t('platform.market.emptyFiltered', { count: catalog.length }) : t('platform.market.emptyCatalog')"
            />
          </div>

          <div v-if="importResult" class="result">
            <el-alert
              type="success"
              :closable="false"
              show-icon
              :title="t('platform.market.importResult', {
                name: importResult.asset?.name,
                build: importResult.asset?.build,
                status: importResult.asset?.status === 'listed' ? t('platform.market.statusPassed') : t('platform.market.statusReview')
              })"
            />
            <ul v-if="importResult.warnings?.length" class="warn-list">
              <li v-for="(w, i) in importResult.warnings" :key="i">{{ w }}</li>
            </ul>
          </div>
        </template>
      </section>
    </div>

    <!-- 新建 / 编辑对端 -->
    <el-dialog v-model="peerFormVisible" :title="peerForm.id ? t('platform.market.dialogEdit') : t('platform.market.dialogCreate')" width="520px">
      <div class="form">
        <div class="form-row">
          <label>{{ t('platform.market.labelName') }}</label>
          <div class="form-control">
            <el-input v-model="peerForm.name" size="small" :placeholder="t('platform.market.phName')" />
          </div>
        </div>
        <div class="form-row">
          <label>{{ t('platform.market.labelAddress') }}</label>
          <div class="form-control">
            <el-input v-model="peerForm.base_url" size="small" :placeholder="t('platform.market.phAddress')" />
            <div class="hint">{{ t('platform.market.addressHint1') }}<code>/platform/market/…</code>{{ t('platform.market.addressHint2') }}</div>
          </div>
        </div>
        <div class="form-row">
          <label>{{ t('platform.market.labelAuth') }}</label>
          <div class="form-control">
            <el-select v-model="peerForm.auth_mode" size="small" style="width: 100%">
              <el-option :label="t('platform.market.authToken')" value="token" />
              <el-option :label="t('platform.market.authNone')" value="none" />
            </el-select>
            <div class="hint">
              {{ t('platform.market.authHint1') }} <code>market.open: true</code>{{ t('platform.market.authHint2') }}
            </div>
          </div>
        </div>
        <div v-if="peerForm.auth_mode === 'token'" class="form-row">
          <label>{{ t('platform.market.labelToken') }}</label>
          <div class="form-control">
            <el-input
              v-model="peerForm.token"
              size="small"
              :placeholder="peerForm.id ? t('platform.market.phTokenKeep') : t('platform.market.phTokenNew')"
            />
            <div v-if="peerForm.id" class="hint">{{ t('platform.market.tokenHintEdit') }}</div>
          </div>
        </div>
        <div class="form-row">
          <label>{{ t('platform.market.labelPolicy') }}</label>
          <div class="form-control">
            <el-checkbox v-model="peerForm.auto_approve" size="small">
              {{ t('platform.market.autoApproveLabel') }}
            </el-checkbox>
            <el-checkbox v-model="peerForm.enabled" size="small">{{ t('platform.market.enablePeer') }}</el-checkbox>
            <div class="hint">{{ t('platform.market.policyHint') }}</div>
          </div>
        </div>
      </div>
      <template #footer>
        <el-button @click="peerFormVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="savingPeer" @click="savePeer">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <!-- 市场出口令牌已移到「本地市场 → 对外接入」：签发的是「别人拉我」的令牌，
         与本页「我去拉别人」方向相反，放在一起容易误读 -->
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search } from '@element-plus/icons-vue'
import { api } from '../api'

const { t } = useI18n()

const emit = defineEmits(['count', 'session-expired'])

// 资产类型标签复用导入对话框里那套（同一个概念不该有两种叫法）
const kindLabel = (k) => ({
  skill: t('assetImport.kindSkill'),
  custom_tool: t('assetImport.kindCustomTool'),
  agent: t('assetImport.kindAgent'),
  workflow: t('assetImport.kindWorkflow')
}[k] || k || '—')
const fmtTime = (t) => (t ? String(t).replace('T', ' ').slice(0, 19) : '—')

const peers = ref([])
const loadingPeers = ref(false)
const activePeer = ref(null)
const catalog = ref([])
const loadingCatalog = ref(false)
const importingId = ref('')
const importResult = ref(null)

// ===== 目录筛选 =====
// 全在本地算：目录本来就是一次全量拉回来的。走后端 ?kind= 会真打一次对端
// （PeerCatalog 直连 peerGet，不走 admin/share 那套 per-peer 缓存），
// 切个类型就要冒 30s 超时的风险，纯亏。
const KIND_FILTERS = computed(() => [
  { value: '', label: t('platform.market.filterAll') },
  { value: 'skill', label: t('assetImport.kindSkill') },
  { value: 'custom_tool', label: t('assetImport.kindCustomTool') },
  { value: 'agent', label: t('assetImport.kindAgent') },
  { value: 'workflow', label: t('assetImport.kindWorkflow') }
])
// 是否已导入做成两个可切换的 chip（点亮的再点一次 = 不限），
// 比再摆一个「全部」按钮少一个控件、也不会和左边的「全部」读串
const IMPORT_FILTERS = computed(() => [
  { value: 'no', label: t('platform.market.filterNotImported') },
  { value: 'yes', label: t('platform.market.filterImported') }
])
const catFilters = reactive({ kind: '', imported: '', q: '' })

// 「对方版本更高」：卡片上直接标出来，不用点覆盖导入才发现
const hasUpdate = (row) => !!row?.imported && Number(row.build) > Number(row.local?.build || 0)

const filteredCatalog = computed(() => {
  const kw = catFilters.q.trim().toLowerCase()
  return catalog.value.filter((r) => {
    if (catFilters.kind && r.kind !== catFilters.kind) return false
    if (catFilters.imported === 'yes' && !r.imported) return false
    if (catFilters.imported === 'no' && r.imported) return false
    if (!kw) return true
    // 名称 / 键名 / 说明一起搜：本机只认「对面怎么叫它」
    return [r.display_name, r.name, r.description].some((s) => (s || '').toLowerCase().includes(kw))
  })
})

const toggleImport = (v) => {
  catFilters.imported = catFilters.imported === v ? '' : v
}

function handleError(err) {
  if (err?.status === 401) {
    emit('session-expired')
    return
  }
  ElMessage.error(err?.message || t('platform.market.requestFailed'))
}

const loadPeers = async () => {
  loadingPeers.value = true
  try {
    const data = await api.listPeers()
    peers.value = data?.items || []
    emit('count', peers.value.length)
    // 选中项跟着刷新后的行数据走
    if (activePeer.value) {
      const hit = peers.value.find((p) => p.id === activePeer.value.id)
      if (hit) activePeer.value = hit
    }
  } catch (err) {
    handleError(err)
  } finally {
    loadingPeers.value = false
  }
}

const selectPeer = (row) => {
  if (!row) return
  activePeer.value = row
  catalog.value = []
  importResult.value = null
  // 换对端必须清筛选：带着上一个站的类型/搜索词看新目录，空了会以为「这个站没东西」
  catFilters.kind = ''
  catFilters.imported = ''
  catFilters.q = ''
  loadCatalog()
}

const loadCatalog = async () => {
  if (!activePeer.value) return
  loadingCatalog.value = true
  importResult.value = null
  try {
    const data = await api.peerCatalog(activePeer.value.id)
    catalog.value = data?.items || []
    // 同步把 last_error / last_sync_at 带回来
    await loadPeers()
  } catch (err) {
    handleError(err)
    catalog.value = []
    // 失败原因后端已写进 last_error，刷新列表让它显形
    loadPeers()
  } finally {
    loadingCatalog.value = false
  }
}

const importAsset = async (row) => {
  if (!activePeer.value) return
  const overwrite = !!row.imported
  if (overwrite) {
    try {
      await ElMessageBox.confirm(
        t('platform.market.overwriteConfirm', {
          peer: activePeer.value.name,
          name: row.name,
          build: row.local?.build,
          reviewTail: activePeer.value.auto_approve ? '' : t('platform.market.overwriteReviewTail')
        }),
        t('platform.market.overwriteConfirmTitle'),
        { type: 'warning', confirmButtonText: t('platform.market.overwriteImport'), cancelButtonText: t('common.cancel') }
      )
    } catch {
      return
    }
  }
  importingId.value = row.id
  try {
    const data = await api.importFromPeer(activePeer.value.id, { asset_id: row.id, force: overwrite })
    importResult.value = data
    ElMessage.success(
      data?.asset?.status === 'listed' ? t('platform.market.importedPassed') : t('platform.market.importedPending')
    )
    await loadCatalog()
  } catch (err) {
    handleError(err)
  } finally {
    importingId.value = ''
  }
}

// ===== 对端表单 =====
const peerFormVisible = ref(false)
const savingPeer = ref(false)
const peerForm = reactive({
  id: '',
  name: '',
  base_url: '',
  token: '',
  auth_mode: 'token', // token = 带市场令牌；none = 对方市场公开，不带令牌
  auto_approve: false,
  enabled: true
})

const openPeerForm = (row) => {
  peerForm.id = row?.id || ''
  peerForm.name = row?.name || ''
  peerForm.base_url = row?.base_url || ''
  peerForm.token = ''
  // 回填认证方式：新建默认「市场令牌」（与需要令牌的对端占多数相符）
  peerForm.auth_mode = row?.auth_mode || (row ? (row.has_token ? 'token' : 'none') : 'token')
  peerForm.auto_approve = row?.install_policy === 'auto'
  peerForm.enabled = row ? !!row.enabled : true
  peerFormVisible.value = true
}

const savePeer = async () => {
  savingPeer.value = true
  try {
    const body = {
      name: peerForm.name.trim(),
      base_url: peerForm.base_url.trim(),
      auth_mode: peerForm.auth_mode,
      // 公开市场不传令牌（后端 auth_mode=none 也会清空旧值）
      token: peerForm.auth_mode === 'none' ? '' : peerForm.token.trim(),
      install_policy: peerForm.auto_approve ? 'auto' : 'review',
      enabled: peerForm.enabled
    }
    if (peerForm.id) await api.updatePeer(peerForm.id, body)
    else await api.createPeer(body)
    ElMessage.success(t('platform.market.saved'))
    peerFormVisible.value = false
    await loadPeers()
  } catch (err) {
    handleError(err)
  } finally {
    savingPeer.value = false
  }
}

const removePeer = async (row) => {
  try {
    await ElMessageBox.confirm(
      t('platform.market.deleteConfirm', { name: row.name }),
      t('platform.market.deleteTitle'),
      { type: 'warning', confirmButtonText: t('common.delete'), cancelButtonText: t('common.cancel') }
    )
  } catch {
    return
  }
  try {
    await api.deletePeer(row.id)
    if (activePeer.value?.id === row.id) {
      activePeer.value = null
      catalog.value = []
    }
    ElMessage.success(t('platform.market.deleted'))
    await loadPeers()
  } catch (err) {
    handleError(err)
  }
}

// 市场出口令牌 / 匿名访问开关已移到「本地市场 → 对外接入」：
// 那是「别人拉我」，和本页「我去拉别人」方向相反。

onMounted(loadPeers)
</script>

<style scoped>
/* ===== 三层灰阶（与本地市场 / 租户管理 / 平台用户同款）=====
   大背景：#f0f2f5（与平台外壳同色，整页一块浅灰）
   两张灰卡：.list-pane / .detail-pane（#fafafa）
   内容卡：.peer-card / .cat-card 白底 + 边框 + 投影 */
.section {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  box-sizing: border-box;
  overflow: hidden;
  background: #f0f2f5;
  border-radius: 12px;
}

.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* 左：对端站点灰卡。
   固定 360px：原来 flex:1 会把右侧目录挤到 720 之后、左栏被拉得又宽又空 */
.list-pane {
  flex: 0 0 360px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 8px 8px 4px;
  box-sizing: border-box;
  overflow: hidden;
  background: #fafafa;
  border-radius: 12px;
}

.lp-bar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-bottom: 10px;
}

.lp-count {
  font-size: 13px;
  color: #909399;
}

.lp-bar-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 卡片队列：撑满剩余高度自己滚；间距与本地市场一致（12px）。
   底色交给灰卡，滚动条位置常驻、拇指透明 → 悬浮才上色 */
.cards {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
  align-content: start;
  gap: 12px;
  padding: 0;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.cards::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.cards::-webkit-scrollbar-track {
  background: transparent;
}

.cards::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.cards:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.cards:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.cards :deep(.el-empty) {
  grid-column: 1 / -1;
}

/* 一张卡四行：站点名 + 策略 / 地址 / 状态 / 操作。
   盒模型对齐本地市场的资产卡：白底、灰边、12px 圆角、常驻投影 */
.peer-card {
  position: relative;
  padding: 12px 14px;
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  cursor: pointer;
  transition: all 0.3s ease;
}

.peer-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

.peer-card.is-active {
  border-color: #409eff;
  background: #ecf5ff;
  box-shadow: 0 0 0 1px rgba(64, 158, 255, 0.18);
}

.pc-top {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.pc-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pc-url {
  margin-top: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pc-state {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 6px;
  font-size: 12px;
  min-width: 0;
}

.pc-state > span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 编辑 / 删除：默认不占位（绝对定位贴在右下），鼠标进卡片才淡入；
   底色取卡片自身的（inherit），浮在上面也看不出补丁 */
.pc-actions {
  position: absolute;
  right: 6px;
  bottom: 3px;
  display: flex;
  gap: 2px;
  padding: 0 3px;
  border-radius: 6px;
  background: inherit;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.15s ease;
}

.peer-card:hover .pc-actions {
  opacity: 1;
  pointer-events: auto;
}

.lp-foot {
  flex-shrink: 0;
  padding-top: 8px;
  font-size: 12px;
  color: #c0c4cc;
}

/* 右：资产目录灰卡（白卡浮在它上面）。左栏定宽后，这里吃掉剩余宽度 */
.detail-pane {
  flex: 1;
  min-width: 0;
  overflow: auto;
  padding: 10px 12px;
  box-sizing: border-box;
  background: #fafafa;
  border-radius: 12px;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.detail-pane::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.detail-pane::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.detail-pane:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.detail-pane:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.d-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.d-title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
}

.d-name {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
}

.d-schema {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.warn {
  margin-top: 10px;
  padding: 8px 10px;
  font-size: 12px;
  line-height: 1.6;
  color: #b26a00;
  background: #fdf6ec;
  border: 1px solid #faecd8;
  border-radius: 8px;
  word-break: break-all;
}

.result {
  margin-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.warn-list {
  margin: 0;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.7;
  color: #7a6a3d;
  max-height: 180px;
  overflow: auto;
}

.form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}

.form-row > label {
  width: 76px;
  flex-shrink: 0;
  padding-top: 4px;
  font-size: 13px;
  color: #606266;
}

.form-control {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.form-control code {
  padding: 0 4px;
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  background: #f4f5f7;
  border-radius: 4px;
}

/* .token-bar / .open-bar / .plain-token 已随「市场出口令牌」移到本地市场页 */

.kind {
  display: inline-block;
  padding: 1px 8px;
  font-size: 12px;
  line-height: 18px;
  border-radius: 9px;
  background: #f0f2f5;
  color: #606266;
  white-space: nowrap;
}

.kind--skill {
  background: #eef6ff;
  color: #1874d0;
}

.kind--custom_tool {
  background: #f4f0ff;
  color: #6b47c9;
}

.kind--agent {
  background: #effaf3;
  color: #2c9b62;
}

.kind--workflow {
  background: #fff7e6;
  color: #c47b00;
}

/* ===== 目录筛选条 =====
   与本地市场同一套药丸 chip；两组之间靠竖线分开，否则「全部」与「未导入」会读串 */
.cat-bar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin: 10px 0 8px;
}

.cat-chip {
  height: 24px;
  padding: 0 10px;
  font-size: 12px;
  color: #666;
  background: #fff;
  border: 1px solid #e4e7ed;
  border-radius: 12px;
  cursor: pointer;
  transition: all 0.2s;
}

.cat-chip:hover {
  border-color: #1890ff;
  color: #1890ff;
}

.cat-chip.active {
  background: #e6f0f8;
  border-color: #b3d4f0;
  color: #1890ff;
  font-weight: 500;
}

.cat-sep {
  width: 1px;
  height: 14px;
  margin: 0 2px;
  background: #e4e7ed;
}

/* 搜索靠右，筛掉多少贴在最末尾：窄屏换行时这一组一起掉到下一行 */
.cat-search {
  width: 150px;
  margin-left: auto;
}

.cat-total {
  flex-shrink: 0;
  font-size: 12px;
  color: #c0c4cc;
}

/* 对端目录卡片：一行多张，三行内容（标题 / name / 状态+导入） */
.cat-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 8px;
  align-content: start;
}

.cat-cards :deep(.el-empty) {
  grid-column: 1 / -1;
}

.cat-card {
  padding: 8px 10px 6px;
  /* 详情区已是 #fafafa 灰卡，这里要白底才显得出卡片边界 */
  background: #fff;
  border: 1px solid #eef0f3;
  border-radius: 10px;
  transition: border-color 0.15s, background-color 0.15s;
}

.cat-card:hover {
  border-color: #d6e6fb;
  background: #f7fbff;
}

.cc-top {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.cc-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cc-ver {
  flex-shrink: 0;
  font-size: 12px;
}

.cc-sub {
  margin-top: 3px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 描述最多两行（固定两行高：同排卡片的「状态 + 导入」才会对齐） */
.cc-desc {
  margin-top: 4px;
  min-height: 36px;
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.cc-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  margin-top: 4px;
}

.mono {
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  color: #909399;
}

.muted {
  color: #c0c4cc;
}

.err {
  font-size: 12px;
  color: #f56c6c;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.hint {
  font-size: 12px;
  color: #a8abb2;
  line-height: 1.6;
}
</style>
