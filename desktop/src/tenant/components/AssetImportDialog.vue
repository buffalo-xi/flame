<template>
  <!-- 四类资产列表共用同一个导入对话框：选一个 .zip、可选改名、装成本租户实体。
       与「资产市场」的区别：包是用户自己带上来的文件，不落平台资产、不进审核。
       表单结构与市场安装对话框刻意保持一致（装成 → 预检 → 状态 → 发版），
       预检与结果两块直接复用共享组件。 -->
  <el-dialog :model-value="modelValue" :title="title" width="520px" class="asset-import-dialog"
             :close-on-click-modal="false" @update:model-value="onVisible" @closed="reset">
    <div v-if="!done" class="import-body">
      <div class="hint">{{ hint }}</div>

      <div class="picker">
        <input ref="fileRef" type="file" accept=".zip,application/zip" class="file-native" @change="onPick" />
        <el-button size="small" @click="pick">选择资产包…</el-button>
        <span class="file-name" :class="{ empty: !file }">{{ file ? file.name : '未选择文件（.zip）' }}</span>
      </div>

      <div class="name-row">
        <span class="name-label">装成</span>
        <el-input v-model="name" size="small" clearable
                  :placeholder="`留空 = 沿用包里的名字（${kindLabel}，撞名时可在这里改）`" />
      </div>

      <div v-if="inspecting" class="dep-hint">正在检查包内依赖…</div>
      <!-- 与市场安装同一份预检渲染：同名提示 / 分组依赖 / 随包自带 / notes 都在这一个组件里 -->
      <AssetDepCheck v-else-if="deps" :inspect="deps" :kind-label="kindLabel" verb="导入" />

      <!-- 只有自定义工具有「停用」这一态（工作流 / 智能体 / 技能只有正常 / 废弃） -->
      <div v-if="kind === 'custom_tool'" class="name-row">
        <span class="name-label">状态</span>
        <el-radio-group v-model="status" size="small">
          <el-radio :value="1">启用（默认）</el-radio>
          <el-radio :value="2">停用（先复核脚本与配置）</el-radio>
        </el-radio-group>
      </div>

      <!-- 发布是独立动作：默认不发，装进来先当草稿；勾了才在导入成功后顺手发一版 -->
      <div class="publish-row">
        <el-checkbox v-model="publishNow">导入成功后立刻发布一个版本</el-checkbox>
        <div class="publish-hint">
          默认不发：装进来的是草稿，正式入口（端点 / MCP / 工具调用）用的是已发布版本。
          停用或废弃的实体发不出去，会在这里给出原因。
        </div>
      </div>

      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
    </div>

    <!-- 装完：一律留在窗口里让人看完再走，别让 warnings / 跳过文件一闪而过 -->
    <AssetInstallResult v-else :name="installedName" :overwritten="overwritten" :warnings="warnings"
                        :skipped="skipped" :published="published" verb="导入" />

    <template #footer>
      <el-button @click="onVisible(false)">{{ done ? '完成' : '取消' }}</el-button>
      <el-button v-if="!done" type="primary" :loading="submitting" :disabled="!file" @click="submit">
        导入
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ElMessageBox } from 'element-plus'
import AssetDepCheck from './AssetDepCheck.vue'
import AssetInstallResult from './AssetInstallResult.vue'
import {
  importAsset,
  inspectImport,
  ImportError,
  publishAssetVersion,
  OVERWRITE_COPY
} from '../utils/assetTransfer.js'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  // 决定「在哪个列表导入只能装哪一类」：包内类型不符时后端会拒绝
  kind: { type: String, required: true }
})
const emit = defineEmits(['update:modelValue', 'imported'])

const KIND_META = {
  agent: { label: '智能体', title: '导入智能体' },
  skill: { label: '技能', title: '导入技能' },
  workflow: { label: '工作流', title: '导入工作流' },
  custom_tool: { label: '自定义工具', title: '导入自定义工具' }
}
const kindMeta = computed(() => KIND_META[props.kind] || { label: '资产', title: '导入资产' })
const kindLabel = computed(() => kindMeta.value.label)
const title = computed(() => kindMeta.value.title)
const hint = computed(() =>
  `选择之前用「导出」得到的 ${kindLabel.value}资产包。装完就是本租户自己的${kindLabel.value}，` +
  `来源与平台市场无关；同名时会先问一句，确认即覆盖既有${kindLabel.value}。`
)

const fileRef = ref(null)
const file = ref(null)
const name = ref('')
// 自定义工具的落点状态：默认启用（与后端缺省一致）
const status = ref(1)
const submitting = ref(false)
const error = ref('')
const done = ref(false)
const warnings = ref([])
const skipped = ref([])
const installedName = ref('')
const overwritten = ref(false)
const published = ref(null)
// 导入成功后是否顺手发布一个版本：默认不发（发布是独立动作，且会把草稿变成线上内容）
const publishNow = ref(false)
// 依赖预检结果（只读接口）：装之前就让人看到「包里要什么、本租户缺什么」
const deps = ref(null)
const inspecting = ref(false)

const pick = () => fileRef.value?.click()

// loadDeps 选完包立刻跑一次预检。失败不挡导入：服务端在真正导入时还会完整校验一遍，
// 这里只是「早知道」，别让一次网络抖动卡住整个流程。
const loadDeps = async () => {
  deps.value = null
  if (!file.value) return
  inspecting.value = true
  try {
    deps.value = await inspectImport(props.kind, file.value, { name: name.value.trim() })
  } catch (err) {
    deps.value = { error: err?.message || '依赖检查失败' }
  } finally {
    inspecting.value = false
  }
}

const onPick = (e) => {
  const f = e.target?.files?.[0] || null
  file.value = f
  error.value = ''
  void loadDeps()
}

const reset = () => {
  file.value = null
  name.value = ''
  status.value = 1
  error.value = ''
  done.value = false
  warnings.value = []
  skipped.value = []
  installedName.value = ''
  overwritten.value = false
  published.value = null
  publishNow.value = false
  submitting.value = false
  deps.value = null
  inspecting.value = false
  if (fileRef.value) fileRef.value.value = ''
}

const onVisible = (v) => {
  emit('update:modelValue', v)
}

// doImport 发一次导入。overwrite=true 表示「同名就覆盖既有实体」（四类资产都支持）。
const doImport = (overwrite) =>
  importAsset(props.kind, file.value, {
    name: name.value.trim(),
    status: props.kind === 'custom_tool' ? status.value : undefined,
    overwrite
  })

const applyInstalled = (data, { publishedVersion = null, publishError = '' } = {}) => {
  const installed = data.installed || {}
  installedName.value = installed.name || name.value || file.value.name
  overwritten.value = !!data.overwritten
  published.value = publishedVersion
  skipped.value = Array.isArray(data.skipped_files) ? data.skipped_files : []
  const list = Array.isArray(data.warnings) ? data.warnings.filter(Boolean) : []
  if (publishError) list.push(`导入已完成，但发布版本失败：${publishError}（可到版本面板手动发布）`)
  warnings.value = list
  emit('imported', data)
  // 与市场安装一致：结果页常驻，由用户点「完成」离开
  done.value = true
}

const submit = async () => {
  if (!file.value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    let data
    try {
      data = await doImport(false)
    } catch (err) {
      const info = err instanceof ImportError ? err.data : null
      const copy = OVERWRITE_COPY[props.kind]
      if (!info?.can_overwrite || !copy) throw err
      // 同名不是失败，而是「要你确认」：覆盖是破坏性动作，先把替换范围说清楚
      try {
        await ElMessageBox.confirm(`${err.message}。${copy.detail}`, copy.title, {
          type: 'warning', confirmButtonText: '覆盖', cancelButtonText: '取消'
        })
      } catch {
        return // 用户取消：当作没发生，不弹错误
      }
      data = await doImport(true)
    }

    // 发布失败不回滚导入：两步是独立动作，失败只降级成提示
    let publishedVersion = null
    let publishError = ''
    if (publishNow.value) {
      const ref = props.kind === 'custom_tool' ? data?.installed?.name : data?.installed?.id
      try {
        publishedVersion = await publishAssetVersion(props.kind, ref)
      } catch (err) {
        publishError = err?.message || '发布版本失败'
      }
    }
    applyInstalled(data, { publishedVersion, publishError })
  } catch (err) {
    // 撞名不是失败而是「要你确认」：后端把建议名放在 data.suggested_name 里
    const info = err instanceof ImportError ? err.data : null
    const suggested = info?.suggested_name || ''
    if (suggested) {
      name.value = suggested
      error.value = `${err.message}（已填入建议名 ${suggested}，可直接重试）`
    } else {
      error.value = err?.message || '导入失败'
    }
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.import-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.hint {
  font-size: 12px;
  line-height: 1.6;
  color: #888;
}
/* 「导入后立刻发版」：勾选 + 一句解释，别让人误以为不发就用不了 */
.publish-row {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.publish-hint {
  font-size: 12px;
  line-height: 1.6;
  color: #888;
  padding-left: 22px;
}
.picker {
  display: flex;
  align-items: center;
  gap: 10px;
}
.file-native {
  display: none;
}
.file-name {
  font-size: 13px;
  color: #333;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.file-name.empty {
  color: #bbb;
}
.name-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.name-label {
  font-size: 13px;
  color: #666;
  flex: none;
}
.dep-hint {
  font-size: 12px;
  color: #909399;
}
</style>
