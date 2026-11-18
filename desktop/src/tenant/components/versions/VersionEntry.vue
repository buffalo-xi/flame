<template>
  <span class="version-entry">
    <!-- 私有技能没有独立版本号：写明归属，不给入口（后端对私有技能也确实没有版本表行） -->
    <span v-if="privateHint" class="version-static" :title="privateTip">版本随所属智能体</span>

    <el-tooltip v-else :content="badgeTip" placement="bottom">
      <el-button size="small" class="version-badge" :class="badgeClass" :disabled="!target"
                 @click="open">
        {{ badgeText }}
      </el-button>
    </el-tooltip>

    <el-drawer v-model="visible" :title="title" size="380px" append-to-body class="version-drawer">
      <!-- 三态与工作流编辑器同一套口径：未发布 / 已发布但有改动 / 与线上一致 -->
      <div class="version-status">{{ statusLine }}</div>

      <div class="version-release">
        <el-button size="small" type="primary" :loading="releasing" :disabled="!canRelease"
                   @click="release">
          发布当前草稿
        </el-button>
        <div v-if="!canRelease" class="version-hint">
          只读状态（已废弃）不可发布、切到线上、载入或删除
        </div>
        <div class="version-hint">
          「切到线上」只改线上指针，草稿不动；「载入草稿」只改草稿，线上不动。
        </div>
      </div>

      <el-empty v-if="!loading && !versions.length" description="还没有发布过版本" />
      <div v-else v-loading="loading" class="version-list">
        <div v-for="v in versions" :key="v.version" class="version-item">
          <div class="version-item-head">
            <span class="version-no">v{{ v.version }}</span>
            <el-tag v-if="v.is_current" size="small" type="success">当前版本</el-tag>
            <span class="version-time">{{ formatTime(v.created_at) }}</span>
          </div>
          <div v-if="v.note" class="version-note">{{ v.note }}</div>
          <div class="version-item-meta">
            <span v-if="v.files_count !== undefined">{{ v.files_count }} 个文件</span>
            <span v-if="v.private_skills !== undefined">私有技能 {{ v.private_skills }}</span>
            <span v-if="v.created_by_name">{{ v.created_by_name }}</span>
          </div>
          <div class="version-item-actions">
            <!-- 切到线上 = 只改 is_current：草稿不动，适合「刚发布就发现不对」 -->
            <el-button size="small" type="primary" plain
                       :disabled="!canRelease || v.is_current" @click="rollback(v)">切到线上</el-button>
            <!-- 载入 = 该版本写回草稿（线上不动）；要改线上还得再发布一次 -->
            <el-button size="small" :disabled="!canRelease" @click="apply(v)">载入草稿</el-button>
            <!-- 删除只清历史：当前版本是正式调用的定义来源，按钮不给，接口侧也会拦 -->
            <el-button size="small" text type="danger"
                       :disabled="!canRelease || v.is_current" @click="remove(v)">删除</el-button>
          </div>
        </div>
      </div>
    </el-drawer>
  </span>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'

// kind 决定用哪一组端点（三组接口形状一致，只有 key 是 id 还是 name 的差别）
const props = defineProps({
  kind: { type: String, required: true }, // agent | skill | tool
  target: { type: String, default: '' }, // agent/skill = id，tool = name
  currentVersion: { type: Number, default: 0 },
  dirty: { type: Boolean, default: false },
  canRelease: { type: Boolean, default: true },
  privateHint: { type: Boolean, default: false },
  title: { type: String, default: '版本记录' }
})
// applied = 草稿被整份替换（父级要重挂载文件树）；changed = 只是线上指针变了（父级刷徽标即可）
const emit = defineEmits(['applied', 'changed'])

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

const privateTip = '私有技能没有独立版本号：它的版本就是所属智能体的版本，发布智能体时一起冻结'

const badgeText = computed(() => {
  if (!props.currentVersion) return '未发布'
  return props.dirty ? `v${props.currentVersion} · 有改动` : `v${props.currentVersion}`
})
const badgeClass = computed(() => ({
  'is-unreleased': !props.currentVersion,
  'is-dirty': !!props.currentVersion && props.dirty
}))
const badgeTip = computed(() => {
  if (!props.currentVersion) return '还没有正式版本：正式调用会拒绝，只有编辑页调试能跑'
  if (props.dirty) return `线上跑的是 v${props.currentVersion}，当前草稿有未发布的修改`
  return `线上跑的就是 v${props.currentVersion}`
})
const statusLine = computed(() => {
  if (!props.currentVersion) return '尚未发布正式版本：正式入口（MCP / RPC / 端点 / 调用）会直接报错'
  if (props.dirty) return `当前版本 v${props.currentVersion}：草稿有未发布的改动，线上仍是 v${props.currentVersion}`
  return `当前版本 v${props.currentVersion}：与草稿一致`
})

const formatTime = (t) => {
  if (!t) return ''
  return String(t).replace('T', ' ').slice(0, 19)
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
    if (body.code !== 200) throw new Error(body.msg || '读取版本历史失败')
    versions.value = body.data?.versions || []
  } catch (e) {
    ElMessage.error(e.message || '读取版本历史失败')
  } finally {
    loading.value = false
  }
}

const release = async () => {
  let note = ''
  try {
    const r = await ElMessageBox.prompt('这次发布改了什么？（可留空）', '发布版本', {
      confirmButtonText: '发布',
      cancelButtonText: '取消',
      inputPlaceholder: '例如：修正技能描述'
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
    if (body.code !== 200) throw new Error(body.msg || '发布失败')
    ElMessage.success(`已发布 v${body.data?.version ?? ''}`)
    // 发布不动草稿：父级只需重读版本状态，别重挂载文件树
    emit('changed')
    await load()
  } catch (e) {
    ElMessage.error(e.message || '发布失败')
  } finally {
    releasing.value = false
  }
}

// 切到线上：只翻 is_current，不新增版本、不动草稿 —— 这是「回退」的正确形态。
// 少了它，回退只能「载入草稿 → 再发布」：既覆盖正在改的草稿，又凭空多一个版本号。
const rollback = async (v) => {
  try {
    await ElMessageBox.confirm(
      `线上立刻切到 v${v.version}：正式调用（MCP / RPC / 端点 / 智能体）马上按这一版执行。草稿不动。` +
      `\n\n调用授权与技能绑定不随版本回滚（它们始终是当前配置）——回退后「谁能调用它」仍是回退前那一套。`,
      '切到线上',
      { type: 'warning', confirmButtonText: '切换', cancelButtonText: '取消' }
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
    if (body.code !== 200) throw new Error(body.msg || '切换失败')
    ElMessage.success(`线上已切到 v${v.version}`)
    // 草稿没动：父级只需重读版本状态（当前版本号变了，「有改动」可能跟着变）
    emit('changed')
    await load()
  } catch (e) {
    ElMessage.error(e.message || '切换失败')
  }
}

const apply = async (v) => {
  try {
    await ElMessageBox.confirm(
      `把 v${v.version} 的内容写回草稿？线上仍然跑当前版本，直到你再次发布。` +
      `\n\n调用授权与技能绑定不随版本回写；智能体的「不限制工具 / 技能发现」两个开关属于定义，会按这一版写回。`,
      '载入版本',
      { type: 'warning', confirmButtonText: '载入', cancelButtonText: '取消' }
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
    if (body.code !== 200) throw new Error(body.msg || '载入失败')
    ElMessage.success(`已把 v${v.version} 载入草稿`)
    // 草稿内容变了：父级要重读实体（文件树 / 正文），否则界面还停在旧内容上
    emit('applied')
    await load()
  } catch (e) {
    ElMessage.error(e.message || '载入失败')
  }
}

const remove = async (v) => {
  try {
    await ElMessageBox.confirm(
      `v${v.version} 的历史记录会被永久删除，无法恢复。线上运行的版本与草稿都不受影响。`,
      '删除版本',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
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
    if (body.code !== 200) throw new Error(body.msg || '删除失败')
    ElMessage.success(`已删除 v${v.version}`)
    await load()
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
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
