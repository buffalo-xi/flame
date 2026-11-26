<template>
  <div class="st-wrap">
  <!-- 资产包导入（对话框本体是四类资产共用的通用组件；teleport 到 body，不参与本层布局） -->
  <AssetImportDialog v-model="importVisible" kind="custom_tool" @imported="load" />
  <div v-show="!editorOpen" class="st">
    <!-- 顶部：计数 + 两个动作 -->
    <div class="st-bar">
      <div class="st-summary">
        {{ t('customTools.summary', { n: items.length, active: activeCount }) }}
        <span class="st-muted">{{ t('customTools.refHint', { fn: FN_NAME, ref: GRANT_REF }) }}</span>
      </div>
      <div class="st-actions">
        <el-button size="small" :loading="loading" @click="load">{{ t('common.refresh') }}</el-button>
        <el-button size="small" @click="importVisible = true"
                   :title="t('customTools.importTooltip')">{{ t('assetAction.import') }}</el-button>
        <el-button size="small" type="primary" @click="openNew">{{ t('customTools.newTool') }}</el-button>
      </div>
    </div>

    <!-- 沙箱语言事实：来自 sandboxd 能力快照，不是本页猜的。
         未知时不判死（选项照常可选），只提示"可用性无法确认"。 -->
    <div v-if="sandbox.ready" class="st-meta-line">
      {{ t('customTools.sandboxLine', { mode: sandbox.mode || t('customTools.unknown'), langs: sandboxText }) }}
    </div>
    <div v-else class="st-warnbar">{{ t('customTools.sandboxWarn') }}</div>

    <div v-if="loadError" class="st-error">{{ loadError }}</div>

    <!-- 目录问题：tools/ 下有东西但没能成为工具。列出来，否则它们就是隐形的。 -->
    <div v-if="issues.length" class="st-contract warn" style="margin-bottom: 12px">
      <strong>{{ t('customTools.issues', { n: issues.length }) }}</strong>
      <div v-for="i in issues" :key="i.path + '|' + i.error">
        <code>{{ i.path }}</code>：{{ i.error }}
      </div>
    </div>

    <div v-if="!items.length && !loading" class="st-empty">
      {{ t('customTools.empty', { dir: TOOL_DIR, fn: FN_NAME }) }}
    </div>

    <div class="st-grid">
      <div v-for="p in items" :key="p.name" class="st-card" :class="{ on: p.status === 1 }">
        <div class="st-card-head">
          <div class="st-card-title">
            {{ p.display_name || p.name }}
            <span class="ref-tag" :title="p.name">{{ p.name }}</span>
            <span class="ref-tag" :class="p.status === 1 ? 'ok' : 'off'">{{ statusLabel(p.status) }}</span>
            <!-- 当前发布版本：0 表示还没发布（模型看到的仍是旧版 / 无版本） -->
            <span class="ref-tag ver"
                  :title="p.current_version
                    ? t('customTools.versionTip', { v: p.current_version })
                    : t('customTools.versionNoneTip')">
              {{ p.current_version ? `v${p.current_version}` : t('customTools.unreleased') }}
            </span>
          </div>
          <el-switch
            :model-value="p.status === 1"
            :loading="busy === p.name"
            :disabled="p.status === 3"
            @change="(v) => toggle(p, v)"
          />
        </div>

        <div class="st-card-desc" :title="p.description">{{ p.description || t('customTools.noDescription') }}</div>

        <!-- 卡片先说「这个工具对外是什么」（参数 / 配置 / 出网），
             实现细节（语言 / 入口 / 限制）降为次要信息 —— 卡片不是脚本站。 -->
        <div class="st-card-meta">
          <span>{{ t('customTools.paramCount', { n: paramCount(p) }) }}</span>
          <span>{{ t('customTools.configCount', { n: configCount(p) }) }}</span>
          <span v-if="p.egress" class="st-warn">{{ t('customTools.egressAllowed') }}</span>
          <span v-if="p.contract && p.contract.source !== 'file'" class="st-muted"
                :title="t('customTools.notFileBackedTip')">{{ t('customTools.notFileBacked') }}</span>
          <span class="st-muted" :title="t('customTools.limitsTip', { ms: p.timeout_ms, bytes: p.max_output_bytes })">
            {{ p.lang }} · {{ p.entry }}
          </span>
        </div>

        <div class="st-card-actions">
          <el-button size="small" text @click="exportEntity('custom_tool', p.name, p.name)">{{ t('common.export') }}</el-button>
          <el-button size="small" text @click="openConfig(p)">{{ t('common.config') }}</el-button>
          <el-button size="small" text @click="openEdit(p)">{{ t('common.edit') }}</el-button>
          <!-- Delete is on the card too: the panel danger zone is only reachable after opening a tool, which reads as "cannot be deleted". -->
          <el-button size="small" text type="danger" :loading="deleting === p.name"
                     :title="t('customTools.deleteHint', { dir: 'tools/' + p.name + '/' })"
                     @click="removeTool(p)">{{ t('common.delete') }}</el-button>
        </div>
      </div>
    </div>
  </div>

  <!-- 编辑 / 配置两个入口共用这一个壳：
       - editorOpen=true：全屏目录编辑器（左目录 + 右多 tab），配置退成右侧抽屉；
       - editorOpen=false：只是配置模式 —— 壳退成透明、不拦点击的定位层，背后还是卡片列表。
       **必须留在 .st-wrap 里**：父组件（Capabilities.vue）用 v-show 切 tab，
       Vue 对多根节点组件的 v-show 直接失效 —— 放到根节点外面会让这个 tab 一直可见。 -->
  <div v-if="editing" class="ct-editor" :class="{ 'ct-editor--settings-only': !editorOpen }">
    <div v-if="editorOpen" class="ct-editor-head">
      <!-- 与智能体 / 技能的全屏编辑同款：左上角一个「返回列表」，不用 × 关窗 -->
      <el-button size="small" class="ct-back" @click="closeEditor">
        <el-icon><ArrowLeft /></el-icon>{{ t('customTools.backToList') }}
      </el-button>
      <div class="ct-editor-title">
        {{ isNew ? t('customTools.newTool') : (form.display_name || editing.name) }}
        <span v-if="!isNew" class="ref-tag">{{ editing.name }}</span>
        <span class="ref-tag" :class="editing.status === 1 ? 'ok' : 'off'">{{ statusLabel(editing.status) }}</span>
        <span v-if="formDirty" class="dirty-tip">{{ t('customTools.dirtyConfig') }}</span>
        <span v-if="fileState.hasDirty" class="dirty-tip">{{ t('customTools.dirtyFiles') }}</span>
      </div>
      <!-- 版本：vN / 未发布 / 有改动 三态，与工作流编辑器同口径 -->
      <VersionEntry v-if="!isNew" kind="tool" class="ct-version" :target="editing.name"
                    :current-version="toolVersion" :dirty="toolDirty"
                    :can-release="editing.status === 1" @applied="onVersionApplied"
                    @changed="refreshVersionState" />
      <el-button size="small" :loading="fileState.saving" :disabled="!fileState.canSave"
                 @click="saveFileNow">{{ t('customTools.saveFiles') }}</el-button>
      <el-button size="small" :type="settingsOpen ? 'primary' : ''"
                 @click="settingsOpen = !settingsOpen">{{ t('customTools.settings') }}</el-button>
      <el-button size="small" type="primary" :loading="saving" @click="save">{{ t('customTools.saveConfig') }}</el-button>
    </div>

    <div v-if="editorOpen" class="ct-editor-body">
      <!-- 还没保存的新工具：urls 为空，面板左侧只显示「保存后自动生成 main.py」 -->
      <!-- key 随「载入版本」递增：版本内容整目录替换，必须重挂载才看得到新文件 -->
      <SkillFileEditor :key="(editing.name || 'new') + ':' + fileEpoch" ref="fileEditorRef"
                       :urls="toolUrls" :entry-name="form.entry || 'main.py'"
                       :readonly="editing.status !== 1" @state="onFileState" />
    </div>

    <!-- 配置抽屉：右侧滑出、无遮罩、左缘可拖宽（与插件 / 技能设置面板同款手感）。
         编辑模式下浮在目录树之上；配置模式下它就是全部内容，背后是卡片列表。 -->
    <div v-show="settingsOpen" class="settings-panel" :style="{ width: panelWidth + 'px' }">
      <div class="panel-resizer" @mousedown="startResize"></div>
      <div class="panel-header">
        <div class="panel-title">
          {{ t('customTools.panelTitle') }}
          <span class="ref-tag">{{ namePreview }}</span>
          <span v-if="formDirty" class="dirty-tip">{{ t('customTools.unsaved') }}</span>
          <!-- 配置模式下没有目录编辑器头部，版本入口放这里（编辑器打开时由头部那个负责，避免重复） -->
          <VersionEntry v-if="!isNew && !editorOpen" kind="tool" :target="editing.name"
                        :current-version="toolVersion" :dirty="toolDirty"
                        :can-release="editing.status === 1" @applied="onVersionApplied"
                        @changed="refreshVersionState" />
        </div>
        <el-button size="small" type="primary" :loading="saving" @click="save">{{ t('common.save') }}</el-button>
        <span class="close-icon" @click="closeSettings">&#xd7;</span>
      </div>

      <div class="panel-body">
        <div v-if="!isNew" class="panel-meta">
          {{ t('customTools.panelMeta', { status: statusLabel(editing.status), fn: 'custom__' + editing.name }) }}
        </div>

        <!-- 契约来源：磁盘是唯一真源；没有文件时回退表（打开列表会自动补写一份） -->
        <div v-if="!isNew && editing.contract" class="st-contract"
             :class="{ warn: editing.contract.source !== 'file' }">
          <span>
            {{ editing.contract.source === 'file'
              ? t('customTools.contractFile')
              : t('customTools.contractTable') }}
          </span>
          <div v-for="w in editing.contract.warnings || []" :key="w" class="st-warn">{{ w }}</div>
        </div>

        <div class="st-field">
          <div class="st-label">{{ t('customTools.displayName') }}</div>
          <el-input v-model="form.display_name" size="small" maxlength="64"
                    :placeholder="t('customTools.displayNamePlaceholder')" />
        </div>

        <div class="st-field">
          <div class="st-label">{{ t('customTools.nameLabel') }} <span class="st-req">*</span></div>
          <el-input v-model="form.name" size="small" maxlength="64" :disabled="!isNew"
                    :placeholder="t('customTools.namePlaceholder')" />
          <div class="st-hint">
            {{ t('customTools.nameHint', { fn: 'custom__' + namePreview, ref: 'custom:' + namePreview }) }}
          </div>
        </div>

        <div class="st-field">
          <div class="st-label">{{ t('customTools.descLabel') }}</div>
          <el-input v-model="form.description" type="textarea" :autosize="{ minRows: 5, maxRows: 14 }"
                    :placeholder="t('customTools.descPlaceholder')" />
        </div>

        <!-- 工具契约在前：模型只看得到这几项，它们决定「什么时候调用、怎么填参数」。
             实现细节（语言 / 入口 / 限制 / 代码）收进下面的折叠区。 -->
        <div class="st-field">
          <div class="st-label">{{ t('customTools.inSchema') }}</div>
          <div class="st-hint">{{ t('customTools.inSchemaHint') }}</div>
          <!-- schema 是这里最长的一段文字：给足高度，别让人在 6 行里上下蹭 -->
          <el-input v-model="form.parametersText" type="textarea" :autosize="{ minRows: 14, maxRows: 32 }"
                    class="st-code" spellcheck="false" placeholder='{"type":"object","properties":{}}' />
        </div>

        <div class="st-field">
          <div class="st-label">{{ t('customTools.outSchema') }}</div>
          <el-input v-model="form.outputText" type="textarea" :autosize="{ minRows: 9, maxRows: 24 }"
                    class="st-code" spellcheck="false" :placeholder="t('customTools.outSchemaPlaceholder')" />
        </div>

        <div class="st-field">
          <div class="st-label">{{ t('customTools.configDecl') }}</div>
          <div class="st-hint">{{ t('customTools.configDeclHint') }}</div>
          <div v-for="(f, i) in form.configFields" :key="i" class="st-cfg-row">
            <div class="st-row">
              <el-input v-model="f.key" size="small" placeholder="key" style="width: 120px" />
              <el-input v-model="f.label" size="small" :placeholder="t('customTools.displayName')" style="width: 120px" />
              <el-select v-model="f.type" size="small" style="width: 96px">
                <el-option label="string" value="string" />
                <el-option label="int" value="int" />
                <el-option label="bool" value="bool" />
              </el-select>
              <el-checkbox v-model="f.secret" size="small">{{ t('customTools.masked') }}</el-checkbox>
              <el-checkbox v-model="f.required" size="small">{{ t('customTools.required') }}</el-checkbox>
              <el-button text size="small" @click="form.configFields.splice(i, 1)">{{ t('common.remove') }}</el-button>
            </div>
            <div class="st-row">
              <el-input v-model="f.help" size="small" :placeholder="t('customTools.helpPlaceholder')" style="flex: 1" />
              <el-input v-model="f.default" size="small" :placeholder="t('customTools.defaultPlaceholder')" style="width: 180px" />
            </div>
          </div>
          <el-button size="small" text type="primary" @click="addField">{{ t('customTools.addConfig') }}</el-button>
        </div>

        <div v-if="form.configFields.length" class="st-field">
          <div class="st-label">{{ t('customTools.configValues') }}</div>
          <div v-for="f in form.configFields" :key="f.key" class="st-cfg-value">
            <div class="st-cfg-name">
              {{ f.label || f.key || t('customTools.unnamed') }}<span v-if="f.required" class="st-req">*</span>
            </div>
            <el-switch v-if="f.type === 'bool'" :model-value="!!cfgValue(f.key)"
                       @update:model-value="(v) => setCfg(f.key, v)" />
            <el-input-number v-else-if="f.type === 'int'"
                             :model-value="Number(cfgValue(f.key) ?? f.default ?? 0)"
                             size="small" style="width: 160px"
                             @update:model-value="(v) => setCfg(f.key, v)" />
            <el-input v-else :model-value="cfgValue(f.key) ?? ''"
                      :type="f.secret ? 'password' : 'text'" :show-password="!!f.secret" size="small"
                      :placeholder="f.secret ? t('customTools.secretSaved') : (f.default ?? '')"
                      @update:model-value="(v) => setCfg(f.key, v)" />
            <div v-if="f.help" class="st-hint">{{ f.help }}</div>
          </div>
        </div>

        <!-- 实现区：语言 / 入口 / 限制 / 代码都在这里，默认收起（新建时展开，语言是必填）。
             这些是「怎么跑」而不是「是什么」—— 收起来之后，面板第一屏才是工具契约。 -->
        <div class="st-field">
          <div class="st-impl-head" @click="implOpen = !implOpen">
            <span class="st-label" style="margin: 0">{{ t('customTools.implHead') }}</span>
            <span class="st-muted">{{ implOpen ? t('common.collapse') : t('common.expand') }}</span>
          </div>

          <div v-show="implOpen" class="st-impl-body">
            <div class="st-field">
              <div class="st-label">{{ t('customTools.langEntry') }}</div>
              <div class="st-row">
                <el-select v-model="form.lang" size="small" style="width: 200px" @change="onLangChange">
                  <el-option v-for="l in LANGS" :key="l.value"
                             :label="l.label + (langAvailable(l.value) ? '' : t('customTools.langUnavailable'))"
                             :value="l.value" :disabled="!langAvailable(l.value)" />
                </el-select>
                <el-input v-model="form.entry" size="small" placeholder="main.py" style="flex: 1" />
              </div>
              <div class="st-hint">{{ t('customTools.entryHint', { dir: TOOL_DIR }) }}</div>
              <div v-if="!langAvailable(form.lang)" class="st-warn">
                {{ t('customTools.langWarn', { lang: form.lang, alt: availableLangHint }) }}
              </div>
            </div>

            <div class="st-field">
              <div class="st-label">{{ t('customTools.limits') }}</div>
              <div class="st-row">
                <el-input-number v-model="form.timeout_ms" :min="1000" :step="1000" size="small"
                                 controls-position="right" style="width: 150px" />
                <span class="st-unit">{{ t('customTools.timeoutUnit') }}</span>
                <el-input-number v-model="form.max_output_bytes" :min="1024" :step="1024" size="small"
                                 controls-position="right" style="width: 170px" />
                <span class="st-unit">{{ t('customTools.bytesUnit') }}</span>
              </div>
              <div class="st-row">
                <el-switch v-model="form.egress" />
                <span class="st-unit">{{ t('customTools.egressToggle') }}</span>
              </div>
            </div>

            <div class="st-field">
              <div class="st-label">{{ t('customTools.codeSection') }}</div>
              <div class="st-hint">{{ t('customTools.codeHint', { dir: TOOL_DIR }) }}</div>
              <el-button v-if="!isNew" size="small" @click="goFiles">{{ t('customTools.goFiles') }}</el-button>
              <span v-else class="st-hint">{{ t('customTools.saveFirst') }}</span>
            </div>
          </div>
        </div>

        <!-- 试跑：与线上同一条执行内核（同能力闸、同隔离参数、同遮蔽规则） -->
        <div class="st-field">
          <div class="st-label">{{ t('customTools.runSection') }}</div>
          <div class="st-hint">{{ t('customTools.runHint') }}</div>
          <el-input v-model="runArgs" type="textarea" :rows="4" class="st-code" spellcheck="false"
                    placeholder='{"month": "2026-09"}' />
          <div v-if="runRequiredHint" class="st-hint">{{ runRequiredHint }}</div>
          <div class="st-row" style="margin-top: 8px">
            <el-button size="small" :loading="running" :disabled="!canRun" @click="runOnce">{{ t('customTools.runOnce') }}</el-button>
            <span v-if="runHint" class="st-warn">{{ runHint }}</span>
          </div>

          <div v-if="runResult" class="st-run-result">
            <div class="st-run-head">
              <span class="ref-tag" :class="runResult.ok ? 'ok' : 'off'">
                {{ runResult.ok ? t('customTools.runOk') : t('customTools.runFail') }}
              </span>
              <span v-if="runResult.duration_ms != null" class="st-muted">{{ runResult.duration_ms }} ms</span>
              <span v-if="runResult.exit_code" class="st-muted">{{ t('customTools.exitCode', { code: runResult.exit_code }) }}</span>
              <span v-if="runResult.truncated" class="st-warn">{{ t('customTools.runTruncated') }}</span>
              <span v-if="runResult.masked" class="st-muted">{{ t('customTools.runMasked') }}</span>
            </div>
            <div v-for="w in runResult.warnings || []" :key="w" class="st-warn">{{ w }}</div>
            <div v-if="runResult.error" class="st-error" style="margin: 6px 0 0">{{ runResult.error }}</div>

            <template v-if="runResult.result !== null && runResult.result !== undefined">
              <div class="st-label" style="margin-top: 8px">{{ t('customTools.runResult') }}</div>
              <pre class="st-pre">{{ prettyJSON(runResult.result) }}</pre>
            </template>
            <template v-else-if="runResult.stdout">
              <div class="st-label" style="margin-top: 8px">{{ t('customTools.runStdout') }}</div>
              <pre class="st-pre">{{ runResult.stdout }}</pre>
            </template>

            <template v-if="runResult.stderr">
              <div class="st-label" style="margin-top: 8px">stderr</div>
              <pre class="st-pre st-pre-err">{{ runResult.stderr }}</pre>
            </template>
          </div>
        </div>

        <div v-if="!isNew" class="st-danger">
          <el-button size="small" type="danger" plain :loading="deleting === editing.name" @click="removeTool()">{{ t('customTools.deleteTool') }}</el-button>
          <span class="st-hint">{{ t('customTools.deleteHint', { dir: 'tools/' + editing.name + '/' }) }}</span>
        </div>
      </div>
    </div>
  </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { exportEntity } from '../../utils/assetTransfer.js'
import { useEscClose } from '../../composables/useEscClose'
import AssetImportDialog from '../AssetImportDialog.vue'
// 目录编辑复用技能那套文件面板（同一形态：一个内容目录 + 多文件），不另写一份
import SkillFileEditor from '../skills/SkillFileEditor.vue'
import VersionEntry from '../versions/VersionEntry.vue'
import { formatError } from '../../../locales'

const emit = defineEmits(['count'])

const { t } = useI18n()

// 命名记号：对外名与授权 ref 的形态是后端约定，不是界面文案，所以只把「叫什么」交给语言包
const FN_NAME = 'custom__<name>'
const GRANT_REF = 'custom:<name>'
const TOOL_DIR = 'tools/<name>/'

// 跨页跳转（会话里建 / 改了自定义工具）：切到本页后直接打开该工具的全屏编辑器。
// 页面里按短名定位（详情接口也是按 name 取），id 只作为兜底匹配。
const props = defineProps({ focus: { type: Object, default: null } })

// 资产包导入（对话框本体是通用组件，这里只管开关）
const importVisible = ref(false)

const LANGS = [
  { value: 'python', label: 'Python', entry: 'main.py' },
  { value: 'js', label: 'JavaScript', entry: 'main.js' },
  { value: 'lua', label: 'Lua', entry: 'main.lua' },
  { value: 'sh', label: 'Shell', entry: 'main.sh' }
]

// 与后端 tools.ValidName 同口径（paths.ValidName）：字母数字与短横线，不能以短横线开头。
// 另外禁止连续下划线 —— 对外名是 custom__<name>，名字里再有 __ 会把前缀切乱。
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9-]*$/

// 沙箱语言事实：来自 GET /admin/customtools/ 的 sandbox 字段（sandboxd 能力快照）。
// 语言名与 sandboxd 声明的 key 可能不同名（js 在镜像里叫 node），按别名匹配。
const sandbox = ref({ ready: false, mode: '', langs: [] })
const LANG_ALIASES = {
  python: ['python', 'python3'],
  js: ['js', 'node', 'javascript'],
  lua: ['lua'],
  sh: ['sh', 'shell']
}
const sandboxText = computed(() => (sandbox.value.langs || []).join(' / ') || t('customTools.notConfigured'))
// ready=false（不知道环境）时一律视为可用：此时不判死，只靠顶部横幅提示
const langAvailable = (v) => {
  if (!sandbox.value.ready) return true
  const declared = (sandbox.value.langs || []).map((x) => String(x).toLowerCase())
  return (LANG_ALIASES[v] || [v]).some((n) => declared.includes(n))
}
const availableLangHint = computed(() => {
  const hit = LANGS.find((l) => langAvailable(l.value))
  return hit ? hit.label : t('customTools.otherLang')
})
// 新建时的默认语言：优先 python，不可用则取第一个可用的
const defaultLang = () => (LANGS.find((l) => langAvailable(l.value)) || LANGS[0]).value
const entryFor = (lang) => (LANGS.find((l) => l.value === lang) || LANGS[0]).entry

const items = ref([])
// 目录层问题（缺 TOOL.json / 契约写坏 / 记录残留）：盘上有东西但没能成为工具时，
// 至少要让管理员看见 —— 以前这类目录是完全隐形的。
const issues = ref([])
const loading = ref(false)
const loadError = ref('')
const busy = ref('')
// Name of the tool being deleted, so only that button shows a spinner (the card and the panel each have one).
const deleting = ref('')

// 编辑面板：editing 非空即打开；没有 id 表示新建
const editing = ref(null)
const isNew = computed(() => !!editing.value && !editing.value.id)
const saving = ref(false)

// ===== 两种打开方式 =====
// 工具可能是一棵目录树（lib/ templates/ 多层），所以「编辑」的主舞台是全屏目录编辑器；
// 元数据表单退成右侧滑出的「配置」抽屉，「配置」入口则只浮出这个抽屉（不打开编辑器）。
const editorOpen = ref(false) // 目录编辑器（全屏）
const settingsOpen = ref(false) // 配置抽屉
const fileEditorRef = ref(null)
// 面板自己持有 tab / 脏状态，父组件只拿这几个开关去渲染头部按钮
const fileState = ref({ canSave: false, hasDirty: false, saving: false })
const onFileState = (s) => { fileState.value = s }
// 版本状态（后端详情接口算好）：0 = 从未发布；dirty = 草稿与当前版本不一致（比脚本文件 + 契约元数据）
const toolVersion = ref(0)
const toolDirty = ref(false)
// 「载入版本」整目录替换草稿：文件面板靠这个 key 重挂载
const fileEpoch = ref(0)
const toolUrls = computed(() => {
  const name = editing.value?.name
  if (!name) return null // 还没保存：目录不存在，面板不加载
  return {
    key: name,
    files: () => adminApi.customTools.files(name),
    file: () => adminApi.customTools.file(name),
    upload: () => adminApi.customTools.upload(name),
    download: () => adminApi.customTools.download(name)
  }
})
const saveFileNow = () => fileEditorRef.value?.saveCurrentFile()

const form = reactive({
  name: '',
  display_name: '',
  description: '',
  lang: 'python',
  entry: 'main.py',
  timeout_ms: 60000,
  max_output_bytes: 65536,
  egress: false,
  parametersText: '',
  outputText: '',
  configFields: [],
  configs: {}
})

// 设置抽屉宽度（左缘可拖，320–760）
const panelWidth = ref(520)

// 脏检查：拿打开面板那一刻的快照做基线（默认值在基线之前补好，避免"刚打开就脏"）
const baseline = ref('')
const formSnapshot = () => JSON.stringify({
  name: form.name,
  display_name: form.display_name,
  description: form.description,
  lang: form.lang,
  entry: form.entry,
  timeout_ms: form.timeout_ms,
  max_output_bytes: form.max_output_bytes,
  egress: form.egress,
  parametersText: form.parametersText,
  outputText: form.outputText,
  configFields: form.configFields,
  configs: form.configs
})
const formDirty = computed(() => !!editing.value && baseline.value !== formSnapshot())

const authHeaders = () => ({ Authorization: `Bearer ${getToken()}`, 'Content-Type': 'application/json' })

const activeCount = computed(() => items.value.filter((p) => p.status === 1).length)
const STATUS_LABEL_KEYS = {
  1: 'statusEnabled',
  2: 'statusDisabled',
  3: 'statusDeprecated'
}
const statusLabel = (s) => (STATUS_LABEL_KEYS[s] ? t(`customTools.${STATUS_LABEL_KEYS[s]}`) : t('customTools.statusUnknown'))
// 名字预览：不能直接在插值里写 '<名>'（会被当 HTML 实体原样输出）
const namePreview = computed(() => form.name || '<name>')

// 参数个数：只数 properties 的键，schema 不是对象结构时按 0
const paramCount = (p) => {
  const props = p.parameters?.properties
  return props && typeof props === 'object' ? Object.keys(props).length : 0
}

// 配置项个数（卡片 meta 用）
const configCount = (p) => (Array.isArray(p.config_fields) ? p.config_fields.length : 0)

// 实现区折叠：新建时展开（语言是必填），编辑时收起（第一屏留给工具契约）
const implOpen = ref(true)

// 试跑：参数、结果、进行中
const runArgs = ref('{}')
const runResult = ref(null)
const running = ref(false)

// 试跑只跑**盘上已保存**的代码：脏表单时禁用 —— 否则看到的结果与线上跑的不是同一份代码
const runHint = computed(() => {
  if (isNew.value) return t('customTools.runNeedSave')
  if (formDirty.value) return t('customTools.runDirty')
  // 代码在文件面板里，那边的脏状态同样会让试跑跑的是旧代码
  if (fileState.value.hasDirty) return t('customTools.runFileDirty')
  return ''
})
const canRun = computed(() => !!editing.value && !isNew.value && !formDirty.value && !running.value)

const prettyJSON = (v) => {
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

// 按入参 schema 生成试跑参数骨架：只带 schema 里给了 default 的键。
// 不猜值（比如给 principal 填 0）——那会让人以为能跑，实际必填校验必然失败。
const runArgsTemplate = () => {
  let schema
  try {
    schema = JSON.parse(form.parametersText || '{}')
  } catch {
    return '{}'
  }
  const props = schema && typeof schema.properties === 'object' ? schema.properties : {}
  const obj = {}
  for (const [key, p] of Object.entries(props)) {
    if (p && p.default !== undefined) obj[key] = p.default
  }
  return JSON.stringify(obj, null, 2)
}

// 必填参数提示：让人知道第一次跑至少要填哪几个键，而不是点一下再看 exit 2
const runRequiredHint = computed(() => {
  try {
    const s = JSON.parse(form.parametersText || '{}')
    const req = Array.isArray(s.required) ? s.required : []
    return req.length ? t('customTools.requiredKeys', { keys: req.join(t('common.listJoiner')) }) : ''
  } catch {
    return ''
  }
})

const runOnce = async () => {
  const name = editing.value?.name
  if (!name) return
  const text = String(runArgs.value || '').trim() || '{}'
  let args
  try {
    args = JSON.parse(text)
  } catch {
    ElMessage.error(t('customTools.argsNotJson'))
    return
  }
  if (!args || typeof args !== 'object' || Array.isArray(args)) {
    ElMessage.error(t('customTools.argsNotObject'))
    return
  }
  running.value = true
  runResult.value = null
  try {
    const res = await fetch(adminApi.customTools.run(name), {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ args })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('customTools.runFailed'))
    runResult.value = body.data || {}
  } catch (e) {
    runResult.value = { ok: false, error: e.message || t('customTools.runFailed') }
  } finally {
    running.value = false
  }
}

const hasKeys = (v) => !!v && typeof v === 'object' && Object.keys(v).length > 0

const load = async () => {
  loading.value = true
  loadError.value = ''
  try {
    const res = await fetch(adminApi.customTools.list, { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('customTools.loadFailed'))
    items.value = body.data?.tools || []
    issues.value = body.data?.issues || []
    // 沙箱语言事实：老后端没有这个字段时保持默认（未知），不影响列表
    if (body.data?.sandbox) sandbox.value = body.data.sandbox
    // 面板开着时刷新列表：换成新对象让状态徽标不过期（表单是独立副本，不受影响）
    if (editing.value && !isNew.value) {
      const fresh = items.value.find((x) => x.name === editing.value.name)
      if (fresh) editing.value = fresh
    }
  } catch (e) {
    loadError.value = e.message || t('customTools.loadFailed')
  } finally {
    loading.value = false
    emit('count', items.value.length)
  }
}

const resetForm = () => {
  // 默认语言跟着沙箱事实走：python 不可用时退到第一个可用的，别让用户一进来就选了跑不起来的
  const lang = defaultLang()
  Object.assign(form, {
    name: '',
    display_name: '',
    description: '',
    lang,
    entry: entryFor(lang),
    timeout_ms: 60000,
    max_output_bytes: 65536,
    egress: false,
    parametersText: '{\n  "type": "object",\n  "properties": {}\n}',
    outputText: '',
    configFields: [],
    configs: {}
  })
}

// 新建：先填配置（目录要保存时才创建），保存后自动进目录编辑器。
const openNew = () => {
  editing.value = { name: '', status: 1 }
  toolVersion.value = 0
  toolDirty.value = false
  resetForm()
  implOpen.value = true // 新建时实现区要展开：语言/入口是必填
  runArgs.value = runArgsTemplate()
  runResult.value = null
  baseline.value = formSnapshot()
  editorOpen.value = false
  settingsOpen.value = true
}

// 卡片「编辑」：直接进目录树，配置抽屉按需点开
const openEdit = (p) => openTool(p, true)
// 卡片「配置」：只浮出配置抽屉，不打开编辑器（背后还是卡片列表）
const openConfig = (p) => openTool(p, false)

const openTool = async (p, withEditor) => {
  editorOpen.value = withEditor
  settingsOpen.value = !withEditor
  try {
    // 列表里只有元数据，代码要单独取
    const res = await fetch(adminApi.customTools.get(p.name), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('customTools.loadFailed'))
    const t = body.data?.tool || {}
    editing.value = t
    // 版本状态与契约同一份响应：后端按「当前版本 vs 草稿」算好，前端不自己推
    toolVersion.value = body.data?.current_version || 0
    toolDirty.value = !!body.data?.dirty
    Object.assign(form, {
      name: t.name || '',
      display_name: t.display_name || '',
      description: t.description || '',
      lang: t.lang || 'python',
      entry: t.entry || 'main.py',
      timeout_ms: t.timeout_ms || 60000,
      max_output_bytes: t.max_output_bytes || 65536,
      egress: !!t.egress,
      parametersText: hasKeys(t.parameters) ? JSON.stringify(t.parameters, null, 2) : '',
      outputText: hasKeys(t.output) ? JSON.stringify(t.output, null, 2) : '',
      configFields: (t.config_fields || []).map((f) => ({ ...f })),
      configs: JSON.parse(JSON.stringify(t.configs || {}))
    })
    implOpen.value = false // 编辑已有工具：第一屏看契约，实现细节收起来
    runArgs.value = runArgsTemplate()
    runResult.value = null
    baseline.value = formSnapshot()
  } catch (e) {
    // 读不到就别留个空壳：否则会看到一层什么都没有的编辑器 / 抽屉
    editing.value = null
    editorOpen.value = false
    settingsOpen.value = false
    ElMessage.error(e.message || t('customTools.loadFailed'))
  }
}

// 跨页跳转落点：父组件（Capabilities）已经把页签切到「自定义工具」，这里打开对应工具。
// 列表可能是本页挂载时的旧快照（会话里刚建的工具还不在里面），找不到就先重拉一次。
watch(() => props.focus, async (f) => {
  if (!f || f.nav !== 'capabilities' || !(f.name || f.id)) return
  const find = () => items.value.find((x) =>
    (f.name && x.name === f.name) || (f.id && String(x.id) === String(f.id)))
  let p = find()
  if (!p) {
    await load()
    p = find()
  }
  if (p) openTool(p, true)
})

// 关抽屉：编辑模式下只收起抽屉（目录和已开 tab 都留着）；
// 配置模式下抽屉就是全部内容，收起等于退出这次配置
const closeSettings = () => {
  if (editorOpen.value) settingsOpen.value = false
  else closeEditor()
}

// 配置抽屉 → 目录编辑器（同一份会话，不重新拉数据）
const goFiles = () => {
  settingsOpen.value = false
  editorOpen.value = true
}

const closeEditor = async () => {
  // 两处脏都要问：配置在抽屉里，代码在目录面板里，任一处丢了都是白改
  const dirty = []
  if (formDirty.value) dirty.push(t('customTools.partConfig'))
  if (editorOpen.value && fileState.value.hasDirty) dirty.push(t('customTools.partFiles'))
  if (dirty.length) {
    try {
      await ElMessageBox.confirm(
        t('customTools.closeConfirm', { dirty: dirty.join(t('customTools.dirtyJoiner')) }),
        t('customTools.closeTitle'),
        { type: 'warning', confirmButtonText: t('common.confirm'), cancelButtonText: t('common.cancel') }
      )
    } catch {
      return
    }
  }
  settingsOpen.value = false
  editorOpen.value = false
  editing.value = null
  toolVersion.value = 0
  toolDirty.value = false
}

// ===== 版本（vN / 未发布 / 有改动） =====
// 发布 / 载入之后：载入会把整份定义写回草稿（元数据 + 脚本目录），必须重挂载文件面板 + 重读详情
const onVersionApplied = async () => {
  const t = editing.value
  if (!t?.name) return
  const keepEditor = editorOpen.value
  fileEpoch.value++
  await openTool(t, keepEditor)
  load()
}

// 草稿被改（存文件 / 存配置）后只刷版本徽标，不重载编辑器
const refreshVersionState = async () => {
  const name = editing.value?.name
  if (!name) return
  try {
    const res = await fetch(adminApi.customTools.get(name), { headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) return
    toolVersion.value = body.data?.current_version || 0
    toolDirty.value = !!body.data?.dirty
  } catch {
    // 版本状态是锦上添花：拿不到就不更新，不影响编辑
  }
}

// 文件保存成功后 hasDirty 会回落：此刻草稿已变，版本徽标要跟着变
watch(() => fileState.value.hasDirty, (now, before) => {
  if (before && !now) refreshVersionState()
})

// ESC 关抽屉：走 closeSettings（编辑模式下只收抽屉，配置模式下退出并问未保存）
useEscClose(settingsOpen, () => closeSettings())

// 左缘拖拽调宽：与插件 / 技能设置面板同款手感（越往左拖越宽）
const startResize = (e) => {
  const startX = e.clientX
  const startW = panelWidth.value
  const onMove = (ev) => {
    panelWidth.value = Math.max(320, Math.min(startW + (startX - ev.clientX), 760))
  }
  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
  }
  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
}

// 点配置抽屉外部：编辑模式下只收起抽屉（目录 / 已开 tab 都留着），
// 配置模式下等于退出这次配置（先问过未保存的改动）。
const onDocMouseDown = (e) => {
  if (!settingsOpen.value) return
  const t = e.target
  if (!t || !t.closest) return
  // Element Plus 的下拉 / 提示 / 对话框都 teleport 到 body，不算「外面」
  if (t.closest('.el-popper') || t.closest('.el-select-dropdown') || t.closest('.el-overlay')) return
  if (t.closest('.settings-panel')) return
  // 头部是编辑器级动作（返回列表 / 保存文件 / 保存配置 / 设置开关）：
  // 尤其「设置」是手动开关，若被 mousedown 先收起、click 再打开，点击会看起来没反应
  if (t.closest('.ct-editor-head')) return
  // 卡片上的「编辑 / 配置」是切到另一个工具，不该先弹一次「有未保存修改」的确认
  if (t.closest('.st-card-actions')) return
  closeSettings()
}

const addField = () => {
  form.configFields.push({ key: '', label: '', type: 'string', help: '', secret: false, required: false })
}

const cfgValue = (key) => (key ? form.configs[key] : undefined)
const setCfg = (key, value) => {
  if (!key) return
  form.configs[key] = value
}

// 切换语言：新建态且入口还是某个默认入口时，跟着换成新语言的默认入口
const onLangChange = (lang) => {
  const target = LANGS.find((l) => l.value === lang)
  if (!target) return
  if (isNew.value && LANGS.some((l) => l.entry === form.entry)) form.entry = target.entry
}

// 配置声明转成后端要的形状：丢掉空 key，默认值按 type 转型
const normalizeFields = () => form.configFields
  .filter((f) => String(f.key || '').trim())
  .map((f) => {
    const out = {
      key: String(f.key).trim(),
      label: f.label || '',
      type: f.type || 'string',
      help: f.help || '',
      secret: !!f.secret,
      required: !!f.required
    }
    const d = String(f.default ?? '').trim()
    if (d !== '') {
      if (out.type === 'int') out.default = Number(d) || 0
      else if (out.type === 'bool') out.default = d === 'true'
      else out.default = d
    }
    return out
  })

const buildPayload = () => {
  // ok/value 分开：值本身可以是 null（用户手写 "null"），不能用 null 兼作失败标记
  const parseJSON = (text, label) => {
    const s = String(text || '').trim()
    if (!s) return { ok: true, value: undefined }
    try {
      return { ok: true, value: JSON.parse(s) }
    } catch {
      ElMessage.error(t('customTools.invalidJson', { label }))
      return { ok: false }
    }
  }
  const parameters = parseJSON(form.parametersText, t('customTools.inSchemaShort'))
  if (!parameters.ok) return null
  const output = parseJSON(form.outputText, t('customTools.outSchemaShort'))
  if (!output.ok) return null

  const payload = {
    display_name: form.display_name.trim(),
    description: form.description.trim(),
    // 空对象 = 没有 schema（后端 ParamSchema/OutputSchema 对空对象返回 nil）
    parameters: parameters.value || {},
    output: output.value || {},
    lang: form.lang,
    entry: form.entry.trim() || undefined,
    timeout_ms: form.timeout_ms || 60000,
    max_output_bytes: form.max_output_bytes || 65536,
    egress: form.egress,
    config_fields: normalizeFields(),
    configs: form.configs,
    // 代码不在这里：它属于 tools/<name>/ 下的文件，用「文件」面板编辑（唯一写口）
    status: editing.value?.status || 1
  }
  if (isNew.value) payload.name = form.name.trim()
  return payload
}

const save = async () => {
  if (!editing.value) return
  const name = isNew.value ? form.name.trim() : editing.value.name
  if (!NAME_RE.test(name)) {
    ElMessage.warning(t('customTools.nameInvalid'))
    return
  }
  if (name.includes('__')) {
    ElMessage.warning(t('customTools.nameDunder'))
    return
  }
  const payload = buildPayload()
  if (!payload) return

  saving.value = true
  try {
    const url = isNew.value ? adminApi.customTools.create : adminApi.customTools.update(editing.value.name)
    const res = await fetch(url, {
      method: isNew.value ? 'POST' : 'PUT',
      headers: authHeaders(),
      body: JSON.stringify(payload)
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('customTools.saveFailed'))
    const created = isNew.value ? body.data?.tool : null
    // 目录是保存时才创建的：新建后必须重新按名字载入一次，文件面板才有目标
    if (created?.name) {
      await openEdit(created) // settingsOpen=false：把舞台让给目录
      ElMessage.success(t('customTools.savedGoCode'))
      return
    }
    // 编辑已有工具：就地更新基线，留在编辑器里（后面多半还要改代码，别把人踢回列表）
    ElMessage.success(t('customTools.saved'))
    await load()
    const fresh = items.value.find((x) => x.name === name)
    if (fresh) editing.value = { ...editing.value, ...fresh }
    baseline.value = formSnapshot()
    // 契约元数据（参数 schema / 描述 / 入口）也是定义：改完徽标可能变成「有改动」
    refreshVersionState()
  } catch (e) {
    ElMessage.error(e.message || t('customTools.saveFailed'))
  } finally {
    saving.value = false
  }
}

const toggle = async (p, on) => {
  busy.value = p.name
  try {
    // 只传 status：其余字段省略（后端按「没传 = 不改」处理）
    const res = await fetch(adminApi.customTools.update(p.name), {
      method: 'PUT',
      headers: authHeaders(),
      body: JSON.stringify({ status: on ? 1 : 2 })
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('customTools.saveFailed'))
    ElMessage.success(t(on ? 'customTools.statusEnabled' : 'customTools.statusDisabled'))
    await load()
  } catch (e) {
    ElMessage.error(e.message || t('customTools.saveFailed'))
  } finally {
    busy.value = ''
  }
}

// Delete, shared by the card action and the settings-panel danger zone. The argument is the card's tool
// object; the panel passes nothing and means "the tool being edited".
const removeTool = async (target) => {
  const name = target?.name || editing.value?.name
  if (!name) return
  try {
    await ElMessageBox.confirm(
      t('customTools.deleteBody', { dir: `tools/${name}/` }),
      t('customTools.deleteTitle', { name }),
      {
        type: 'warning',
        confirmButtonText: t('common.delete'),
        cancelButtonText: t('common.cancel')
      }
    )
  } catch {
    return
  }
  deleting.value = name
  try {
    const res = await fetch(adminApi.customTools.remove(name), { method: 'DELETE', headers: authHeaders() })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('customTools.deleteFailed'))
    ElMessage.success(t('customTools.deleted'))
    // Only close the open panel when it belongs to the deleted tool: deleting another card must not throw
    // away the unsaved config / files of the one being edited.
    if (!editing.value || editing.value.name === name) {
      settingsOpen.value = false
      editorOpen.value = false
      editing.value = null
    }
    await load()
  } catch (e) {
    ElMessage.error(e.message || t('customTools.deleteFailed'))
  } finally {
    deleting.value = ''
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
.st-wrap {
  position: relative;
  height: 100%;
}

.st {
  padding: 0 20px 20px;
  overflow-y: auto;
  height: 100%;
  box-sizing: border-box;
  /* 与面板同口径：滚动条常驻占位、平时透明，鼠标进来才上色 */
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.st::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.st::-webkit-scrollbar-track {
  background: transparent;
}

.st::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.st-wrap:hover .st {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.st-wrap:hover .st::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.st-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.st-summary { color: #666; font-size: 13px; }
.st-muted { color: #9aa0a6; margin-left: 8px; }
.st-actions { display: flex; gap: 8px; }

/* 沙箱语言事实：一行灰色小字（已知）或黄色提示条（未知） */
.st-meta-line {
  margin-bottom: 12px;
  color: #9aa0a6;
  font-size: 12px;
}

.st-warnbar {
  margin-bottom: 12px;
  padding: 8px 12px;
  background: #fdf6ec;
  border: 1px solid #faecd8;
  border-radius: 6px;
  color: #b88230;
  font-size: 13px;
}

.st-error {
  margin-bottom: 12px;
  padding: 8px 12px;
  background: #fef0f0;
  border: 1px solid #fde2e2;
  border-radius: 6px;
  color: #c45656;
  font-size: 13px;
}

.st-empty {
  padding: 40px 12px;
  text-align: center;
  color: #9aa0a6;
  font-size: 13px;
  line-height: 1.9;
}

/* 卡片刻意做小一号（对比智能体/技能卡）：一屏多看几个，
   工具卡的信息量本来就少（名字 + 描述 + 三五个短 meta），不需要 330px 的宽卡 */
.st-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 8px;
  /* 每张卡按自身内容取高，不被同一行最高的那张拉齐 —— 描述长短不一时空白更少 */
  align-items: start;
}

.st-card {
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 8px;
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  /* A long tool name would otherwise raise the track's min-content width and defeat the 240px column. */
  min-width: 0;
}

.st-card.on { border-color: #a7d8c2; box-shadow: 0 1px 6px rgba(0, 112, 74, 0.08); }

.st-card-head { display: flex; align-items: center; justify-content: space-between; gap: 6px; }
.st-card-title { font-weight: 600; font-size: 13px; color: #222; display: flex; align-items: center; gap: 5px; flex-wrap: wrap; }
/* 描述最多两行，超出裁掉；全文靠 title 悬停看 —— 卡片是速览，不是详情页 */
.st-card-desc {
  color: #666;
  font-size: 12px;
  line-height: 1.45;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
}
.st-card-meta { display: flex; flex-wrap: wrap; gap: 8px; color: #888; font-size: 11px; }
.st-warn { color: #e6a23c; }
/* Four actions now (export / config / edit / delete): wrap instead of pushing the danger action out of the card. */
.st-card-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 2px; }
/* 操作按钮跟着卡片缩：默认 small 高 32px，会把卡片撑回原来的高度 */
.st-card-actions :deep(.el-button) { height: 24px; padding: 0 8px; font-size: 12px; }

.ref-tag {
  font-size: 11px;
  color: #666;
  background: #f2f3f5;
  border-radius: 4px;
  padding: 1px 6px;
  font-weight: 400;
  /* Narrow cards: the name tag ellipsises instead of pushing the card wider (the full name is in its title). */
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ref-tag.ok { color: #00704a; background: #e6f2ec; }
.ref-tag.off { color: #c45656; background: #fdecec; }
/* 版本胶囊：等宽数字，v1 → v12 不会左右跳（与智能体/技能/工作流卡片同款灰） */
.ref-tag.ver { font-variant-numeric: tabular-nums; }

/* 契约来源提示：file（正常）淡蓝，table（未文件化 / 文件有错）淡黄 */
.st-contract {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 14px;
  padding: 8px 10px;
  background: #f7f9fc;
  border: 1px solid #e8eef7;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: #5b6b7f;
}
.st-contract.warn { background: #fffbea; border-color: #f6e7b4; color: #8a6d1f; }

/* 面板里的表单块 */
.st-field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 18px; }
.st-label { font-weight: 600; font-size: 13px; color: #333; }
.st-hint { color: #9aa0a6; font-size: 12px; line-height: 1.6; }
.st-req { color: #f56c6c; }
.st-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.st-unit { font-size: 12px; color: #909399; }
.st-cfg-row { display: flex; flex-direction: column; gap: 6px; padding: 8px; background: #fafafa; border-radius: 6px; margin-bottom: 6px; }
.st-cfg-value { display: flex; flex-direction: column; gap: 4px; margin-bottom: 8px; }
.st-cfg-name { font-size: 13px; color: #555; }
.st-danger {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-top: 14px;
  border-top: 1px solid #f1f1f1;
}

/* 实现区折叠头：整行可点，右侧给「展开 / 收起」提示 */
.st-impl-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  background: #fafafa;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
  cursor: pointer;
  user-select: none;
}

.st-impl-head:hover { background: #f2f3f5; }

.st-impl-body {
  margin-top: 10px;
  padding-left: 10px;
  border-left: 2px solid #f0f2f5;
}

/* 试跑结果：结果 / 诊断分块展示，失败时 stderr 用红底 */
.st-run-result {
  margin-top: 10px;
  padding: 10px;
  background: #fafafa;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.st-run-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }

.st-pre {
  margin: 0;
  padding: 8px;
  background: #fff;
  border: 1px solid #eee;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 260px;
  overflow: auto;
}

.st-pre-err { background: #fef7f7; border-color: #fde2e2; color: #a85656; }

/* 代码 / JSON 输入：等宽字体（项目没有引入 code editor，统一用 textarea）。
   滚动条：鼠标没进输入框时完全透明（不滚动就别在右边挂一条灰边），
   进去才上色；占位用 scrollbar-gutter 留着，上色时文字不会左右跳。 */
.st-code :deep(textarea) {
  font-family: ui-monospace, Menlo, Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.7;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.st-code :deep(textarea)::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.st-code :deep(textarea)::-webkit-scrollbar-track {
  background: transparent;
}

.st-code :deep(textarea)::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.st-code :deep(textarea:hover) {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.st-code :deep(textarea:hover)::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

/* ============ 编辑面板：对齐插件 / 技能设置面板 ============
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
  /* 常驻占位：滚动条位置一直留着，平时透明，鼠标进面板才上色 */
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

.panel-meta {
  color: #888;
  font-size: 12px;
  margin-bottom: 14px;
}

/* ============ 全屏编辑器：列表的位置，与技能编辑同构 ============
   左目录 + 右多 tab 由 SkillFileEditor 自己渲染，这里只提供头和 flex 容器；
   设置抽屉（.settings-panel）叠在右侧，同属这个定位上下文。 */
.ct-editor {
  position: absolute;
  inset: 0;
  z-index: 100;
  display: flex;
  flex-direction: column;
  background: #f5f7fa;
}

/* 配置模式（编辑器没打开）：壳退成透明、不拦点击的定位层，
   背后仍是卡片列表；只有抽屉本身接手鼠标事件。 */
.ct-editor--settings-only {
  background: transparent;
  pointer-events: none;
}

.ct-editor--settings-only .settings-panel {
  pointer-events: auto;
}

.ct-editor-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 16px;
  background: #fff;
  border-bottom: 1px solid #ebeef5;
  flex-shrink: 0;
}

/* 头部动作与智能体 / 技能编辑页同款：圆角胶囊按钮，且不随标题参与换行挤压 */
.ct-editor-head .el-button {
  border-radius: 20px;
  height: 32px;
  padding: 0 16px;
  flex-shrink: 0;
}

.ct-editor-title {
  flex: 1;
  min-width: 0;
  font-weight: 600;
  color: #222;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.ct-editor-body {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px;
  overflow: hidden;
}
</style>
