<template>
  <span class="version-entry">
    <!-- 私有技能没有独立版本号：写明归属，不给入口（后端对私有技能也确实没有版本表行） -->
    <span v-if="privateHint" class="version-static" :title="privateTip">{{ $t('version.badge') }}</span>

    <el-tooltip v-else :content="badgeTip" placement="bottom">
      <el-button size="small" class="version-badge" :class="badgeClass" :disabled="!target"
                 @click="open">
        {{ badgeText }}
      </el-button>
    </el-tooltip>

    <el-drawer v-model="visible" :title="drawerTitle" size="380px" append-to-body class="version-drawer">
      <!-- 三态与工作流编辑器同一套口径：未发布 / 已发布但有改动 / 与线上一致 -->
      <div class="version-status">{{ statusLine }}</div>

      <div class="version-release">
        <el-button size="small" type="primary" :loading="releasing" :disabled="!canRelease"
                   @click="release">
          {{ $t('version.releaseCurrent') }}
        </el-button>
        <div v-if="!canRelease" class="version-hint">
          {{ $t('version.readonlyNotice') }}
        </div>
        <div class="version-hint">
          {{ $t('version.pointerNotice') }}
        </div>
      </div>

      <el-empty v-if="!loading && !versions.length" :description="$t('version.empty')" />
      <div v-else v-loading="loading" class="version-list">
        <div v-for="v in versions" :key="v.version" class="version-item">
          <div class="version-item-head">
            <span class="version-no">v{{ v.version }}</span>
            <el-tag v-if="v.is_current" size="small" type="success">{{ $t('version.currentTag') }}</el-tag>
            <span class="version-time">{{ formatTime(v.created_at) }}</span>
          </div>
          <div v-if="v.note" class="version-note">{{ v.note }}</div>
          <div class="version-item-meta">
            <span v-if="v.files_count !== undefined">{{ $t('version.filesCount', { n: v.files_count }) }}</span>
            <span v-if="v.private_skills !== undefined">{{ $t('version.privateSkills', { n: v.private_skills }) }}</span>
            <span v-if="v.created_by_name">{{ v.created_by_name }}</span>
          </div>
          <div class="version-item-actions">
            <!-- 切到线上 = 只改 is_current：草稿不动，适合「刚发布就发现不对」 -->
            <el-button size="small" type="primary" plain
                       :disabled="!canRelease || v.is_current" @click="rollback(v)">{{ $t('version.rollback') }}</el-button>
            <!-- 载入 = 该版本写回草稿（线上不动）；要改线上还得再发布一次 -->
            <el-button size="small" :disabled="!canRelease" @click="apply(v)">{{ $t('version.apply') }}</el-button>
            <!-- 删除只清历史：当前版本是正式调用的定义来源，按钮不给，接口侧也会拦 -->
            <el-button size="small" text type="danger"
                       :disabled="!canRelease || v.is_current" @click="remove(v)">{{ $t('version.delete') }}</el-button>
          </div>
        </div>
      </div>
    </el-drawer>
  </span>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { formatError } from '../../../locales'

// kind 决定用哪一组端点（三组接口形状一致，只有 key 是 id 还是 name 的差别）
const props = defineProps({
  kind: { type: String, required: true }, // agent | skill | tool
  target: { type: String, default: '' }, // agent/skill = id，tool = name
  currentVersion: { type: Number, default: 0 },
  dirty: { type: Boolean, default: false },
  canRelease: { type: Boolean, default: true },
  privateHint: { type: Boolean, default: false },
  // 留空用默认标题（默认值不能写死文案：它不随语言变）
  title: { type: String, default: '' }
})
// applied = 草稿被整份替换（父级要重挂载文件树）；changed = 只是线上指针变了（父级刷徽标即可）
const emit = defineEmits(['applied', 'changed'])

const { t } = useI18n()
const drawerTitle = computed(() => props.title || t('version.defaultTitle'))

const api = computed(() => {
  if (props.kind === 'tool') return adminApi.customTools
  if (props.kind === 'skill') return adminApi.skills
  return adminApi.agents
})

const headers = () => ({
  'Content-Type': 'application/json',
  Authorization: `Bearer ${getToken()}`
})

const visible = ref(false)
const loading = ref(false)
const releasing = ref(false)
const versions = ref([])

const privateTip = computed(() => t('version.privateTip'))

const badgeText = computed(() => {
  if (!props.currentVersion) return t('version.labelUnreleased')
  return props.dirty
    ? t('version.labelDirty', { v: props.currentVersion })
    : `v${props.currentVersion}`
})
const badgeClass = computed(() => ({
  'is-unreleased': !props.currentVersion,
  'is-dirty': !!props.currentVersion && props.dirty
}))
const badgeTip = computed(() => {
  if (!props.currentVersion) return t('version.badgeTipUnreleased')
  if (props.dirty) return t('version.badgeTipDirty', { v: props.currentVersion })
  return t('version.badgeTipClean', { v: props.currentVersion })
})
const statusLine = computed(() => {
  if (!props.currentVersion) return t('version.statusUnreleased')
  if (props.dirty) return t('version.statusDirty', { v: props.currentVersion })
  return t('version.statusClean', { v: props.currentVersion })
})

const formatTime = (ts) => {
  if (!ts) return ''
  return String(ts).replace('T', ' ').slice(0, 19)
}

const open = async () => {
  if (!props.target) return
  visible.value = true
  await load()
}

const load = async () => {
  loading.value = true
  try {
    const res = await fetch(api.value.versions(props.target), { headers: headers() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('version.loadFailed'))
    versions.value = body.data?.versions || []
  } catch (e) {
    ElMessage.error(e.message || t('version.loadFailed'))
  } finally {
    loading.value = false
  }
}

const release = async () => {
  let note = ''
  try {
    const r = await ElMessageBox.prompt(t('version.releasePrompt'), t('version.releaseTitle'), {
      confirmButtonText: t('version.release'),
      cancelButtonText: t('common.cancel'),
      inputPlaceholder: t('version.releasePlaceholder')
    })
    note = r.value || ''
  } catch {
    return // 取消
  }
  releasing.value = true
  try {
    const res = await fetch(api.value.versions(props.target), {
      method: 'POST',
      headers: headers(),
      body: JSON.stringify({ note })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('version.releaseFailed'))
    ElMessage.success(t('version.released', { v: body.data?.version ?? '' }))
    // 发布不动草稿：父级只需重读版本状态，别重挂载文件树
    emit('changed')
    await load()
  } catch (e) {
    ElMessage.error(e.message || t('version.releaseFailed'))
  } finally {
    releasing.value = false
  }
}

// 切到线上：只翻 is_current，不新增版本、不动草稿 —— 这是「回退」的正确形态。
// 少了它，回退只能「载入草稿 → 再发布」：既覆盖正在改的草稿，又凭空多一个版本号。
const rollback = async (v) => {
  try {
    await ElMessageBox.confirm(
      t('version.rollbackConfirm', { v: v.version }),
      t('version.rollbackTitle'),
      {
        type: 'warning',
        confirmButtonText: t('version.confirmSwitch'),
        cancelButtonText: t('common.cancel')
      }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(api.value.rollbackVersion(props.target, v.version), {
      method: 'POST',
      headers: headers()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('version.rollbackFailed'))
    ElMessage.success(t('version.rolledBack', { v: v.version }))
    // 草稿没动：父级只需重读版本状态（当前版本号变了，「有改动」可能跟着变）
    emit('changed')
    await load()
  } catch (e) {
    ElMessage.error(e.message || t('version.rollbackFailed'))
  }
}

const apply = async (v) => {
  try {
    await ElMessageBox.confirm(
      t('version.applyConfirm', { v: v.version }),
      t('version.applyTitle'),
      {
        type: 'warning',
        confirmButtonText: t('version.confirmApply'),
        cancelButtonText: t('common.cancel')
      }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(api.value.applyVersion(props.target, v.version), {
      method: 'POST',
      headers: headers()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('version.applyFailed'))
    ElMessage.success(t('version.applied', { v: v.version }))
    // 草稿内容变了：父级要重读实体（文件树 / 正文），否则界面还停在旧内容上
    emit('applied')
    await load()
  } catch (e) {
    ElMessage.error(e.message || t('version.applyFailed'))
  }
}

const remove = async (v) => {
  try {
    await ElMessageBox.confirm(
      t('version.deleteConfirm', { v: v.version }),
      t('version.deleteTitle'),
      {
        type: 'warning',
        confirmButtonText: t('version.confirmDelete'),
        cancelButtonText: t('common.cancel')
      }
    )
  } catch {
    return
  }
  try {
    const res = await fetch(api.value.deleteVersion(props.target, v.version), {
      method: 'DELETE',
      headers: headers()
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('version.deleteFailed'))
    ElMessage.success(t('version.deleted', { v: v.version }))
    await load()
  } catch (e) {
    ElMessage.error(e.message || t('version.deleteFailed'))
  }
}
</script>

<style scoped>
.version-entry {
  display: inline-flex;
  align-items: center;
}

.version-badge {
  font-size: 12px;
  padding: 0 8px;
  height: 22px;
  border-radius: 11px;
}

/* 未发布：暖色描边提醒「线上还跑不了」 */
.version-badge.is-unreleased {
  color: #b88230;
  border-color: #e6c07b;
  background: #fdf6ec;
}

/* 已发布但有未发布的改动：与「未发布」区分 —— 那是有版本只是落后 */
.version-badge.is-dirty {
  color: #b88230;
  border-color: #f0d3a3;
  background: #fdfaf3;
}

.version-static {
  font-size: 12px;
  color: #909399;
  cursor: default;
}

.version-status {
  font-size: 12px;
  color: #606266;
  background: #f5f7fa;
  border-radius: 4px;
  padding: 8px 10px;
  line-height: 1.5;
}

.version-release {
  margin: 12px 0;
}

.version-hint {
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
}

.version-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-height: 60px;
}

.version-item {
  border: 1px solid #ebeef5;
  border-radius: 6px;
  padding: 10px 12px;
}

.version-item-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.version-no {
  font-weight: 600;
  font-size: 13px;
}

.version-time {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
}

.version-note {
  margin-top: 6px;
  font-size: 12px;
  color: #606266;
  white-space: pre-wrap;
}

.version-item-meta {
  margin-top: 6px;
  display: flex;
  gap: 10px;
  font-size: 12px;
  color: #909399;
}

.version-item-actions {
  margin-top: 8px;
  display: flex;
  gap: 8px;
}
</style>
