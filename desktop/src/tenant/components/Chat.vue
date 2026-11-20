<template>
  <div class="agent-chat">
    <div class="chat-body">
      <!-- 会话侧栏 -->
      <div v-show="chatVisible && sessionPanelVisible" class="session-panel">
        <div class="session-new" :class="{ disabled: !selectedAgentId }" @click="startNewChat">
          <el-icon><Plus /></el-icon>
          <span>{{ t('chat.newChat') }}</span>
          <!-- 会话数配额：只在配置了上限时显示（-1 = 不限制），接近上限变红 -->
          <span
            v-if="sessionsQuotaText"
            class="session-quota"
            :class="{ warn: sessionsQuotaWarn }"
            :title="t('chat.sessionsQuotaTip', { used: sessionsUsed, limit: sessionsLimit })"
          >{{ sessionsQuotaText }}</span>
        </div>
        <el-input
          v-model="sessionSearch"
          :placeholder="t('chat.searchSession')"
          prefix-icon="Search"
          clearable
          class="session-search"
          :disabled="!selectedAgentId"
          @input="onSessionSearch"
        />
        <div class="session-section-title">{{ t('chat.recentSessions') }}</div>
        <div class="session-list">
          <div
            v-for="s in sessions"
            :key="s.id"
            class="session-item"
            :class="{ active: s.id === currentSessionId }"
            @click="switchSession(s)"
          >
            <span class="session-item-title">{{ s.title || t('chat.untitledSession') }}</span>
            <el-icon class="session-item-delete" @click.stop="deleteSession(s)"><Delete /></el-icon>
          </div>
          <div v-if="sessions.length === 0" class="session-empty">{{ t('chat.emptySessions') }}</div>
        </div>
      </div>

      <!-- 对话内容 -->
      <div v-show="chatVisible" class="chat-main">
        <!-- 顶部工具条：会话切换 + agent 选择 -->
        <div class="chat-toolbar">
          <el-button
            :icon="ChatDotRound"
            circle
            size="small"
            :disabled="!selectedAgentId"
            :class="{ active: sessionPanelVisible }"
            :title="sessionPanelVisible ? t('chat.closeSessionPanel') : t('chat.openSessionPanel')"
            @click="toggleSessionPanel"
          />
          <el-select
            v-model="selectedAgentId"
            :placeholder="t('chat.selectAgent')"
            filterable
            clearable
            class="agent-select"
            style="flex: 1"
            @change="handleAgentChange"
          >
            <!-- 选中态：名字 + 小一号的说明。label 仍带后缀（filterable 按它匹配，
                 搜「超级」能直接筛出超级智能体），显示由这个 slot 接管 -->
            <template #label="{ value }">
              <span class="agent-selected">
                {{ agentOf(value)?.name || '' }}
                <span v-if="agentOf(value)?.is_supervisor" class="agent-selected-hint">{{ t('chat.supervisor') }}</span>
              </span>
            </template>
            <el-option
              v-for="a in agents"
              :key="a.id"
              :label="a.is_supervisor ? t('chat.agentOption', { name: a.name }) : a.name"
              :value="a.id"
            >
              <span>{{ a.name }}</span>
              <span v-if="a.is_supervisor" class="agent-opt-badge">{{ t('chat.supervisor') }}</span>
            </el-option>
          </el-select>
          <el-button
            size="small"
            text
            :icon="Close"
            :title="t('chat.closeChat')"
            @click="closeChat"
          />
        </div>

        <!-- 消息区 -->
        <div class="chat-messages" ref="messagesRef">
          <div v-if="hasMore" class="load-more">
            <el-button size="small" text :loading="loadingMore" @click="loadMore">{{ t('chat.loadEarlier') }}</el-button>
          </div>
          <div v-if="messages.length === 0" class="chat-empty">
            <el-icon class="empty-icon"><ChatDotRound /></el-icon>
            <p>{{ selectedAgentId ? t('chat.startChat') : t('chat.selectAgentToChat') }}</p>
          </div>

          <div
            v-for="(m, i) in messages"
            :key="i"
            class="message-row"
            :class="m.role"
          >
            <!-- 助手消息头（仿 workbuddy）：不套气泡，归属就是这一行 ——
                 第一行「图标 + 名字」，第二行「执行用时 N 秒」，正文在它们下面。
                 历史消息后端只回 role/content，没有计时数据，秒数那行不渲染。 -->
            <template v-if="m.role === 'assistant'">
              <div class="msg-head">
                <el-icon class="msg-avatar"><MagicStick /></el-icon>
                <span class="msg-name">{{ agentDisplayName }}</span>
              </div>
              <div v-if="elapsedText(m)" class="msg-status">{{ elapsedText(m) }}</div>
            </template>
            <!-- 用户消息：气泡下方一行「时间 + 复制/编辑」（悬停显示）；
                 点编辑后，编辑框出现在气泡下方，宽度收敛到 380px 而不是铺满整行 -->
            <template v-if="m.role === 'user'">
              <div class="user-msg" @mouseenter="onUserMsgEnter">
                <div class="bubble">{{ m.content }}</div>
                <!-- 本轮附带的文件（仅本次渲染有；历史记录只存正文，不带附件） -->
                <div v-if="(m.files || []).length" class="msg-files">
                  <span v-for="f in m.files" :key="f" class="msg-file">
                    <el-icon><Document /></el-icon>{{ f }}
                  </span>
                </div>
                <div v-if="editingIndex !== i" class="msg-meta">
                  <span class="msg-time">{{ formatMsgTime(m.at) }}</span>
                  <div v-if="!streaming" class="msg-actions">
                    <button class="msg-action" :title="t('chat.copy')" @click="copyMessage(m.content)">
                      <el-icon><CopyDocument /></el-icon>
                    </button>
                    <button class="msg-action" :title="t('chat.edit')" @click="startEdit(i, m)">
                      <el-icon><EditPen /></el-icon>
                    </button>
                  </div>
                </div>
              </div>
              <div v-if="editingIndex === i" class="edit-box">
                <el-input
                  v-model="editingText"
                  type="textarea"
                  :rows="3"
                  resize="none"
                  class="edit-input"
                  @keydown.enter.exact.prevent="submitEdit"
                />
                <div class="edit-actions">
                  <el-button size="small" text @click="cancelEdit">{{ t('chat.cancel') }}</el-button>
                  <el-button size="small" type="primary" :disabled="!editingText.trim()" @click="submitEdit">
                    {{ t('chat.send') }}
                  </el-button>
                </div>
              </div>
            </template>

            <div v-else class="bubble" :class="{ empty: !m.content && !(m.blocks || []).length }">
              <!-- 新消息按 blocks 渲染（思考 / 工具调用 / 正文）；历史消息只有 content -->
              <div v-if="(m.blocks || []).length" class="blocks">
                <template v-for="(b, bi) in m.blocks" :key="bi">
                  <div v-if="b.type === 'thinking'" class="think-block">
                    <span class="think-label" @click="b.collapsed = !b.collapsed">
                      <el-icon><ArrowRight v-if="b.collapsed" /><ArrowDown v-else /></el-icon>
                      {{ t('chat.thinking') }}
                    </span>
                    <div v-show="!b.collapsed" class="think-text">{{ b.text }}</div>
                  </div>
                  <div v-else-if="b.type === 'tool'" class="tool-card">
                    <div class="tool-head" @click="b.collapsed = !b.collapsed">
                      <el-icon class="tool-icon" :class="b.status">
                        <Loading v-if="b.status === 'running'" />
                        <CircleCheck v-else-if="b.status === 'done'" />
                        <CircleClose v-else />
                      </el-icon>
                      <span class="tool-name">{{ b.name }}</span>
                      <span v-if="b.durationMs" class="tool-dur">{{ b.durationMs }}ms</span>
                    </div>
                    <div v-show="!b.collapsed">
                      <div v-if="b.args && b.args !== '{}'" class="tool-args">{{ b.args }}</div>
                      <!-- 子智能体执行过程（call_agent 调用时挂载） -->
                      <div v-if="(b.children || []).length" class="sub-blocks">
                        <div class="sub-label">{{ t('chat.subAgentProcess') }}</div>
                        <template v-for="(c, ci) in b.children" :key="ci">
                          <div v-if="c.type === 'thinking'" class="think-block">
                            <span class="think-label" @click="c.collapsed = !c.collapsed">
                              <el-icon><ArrowRight v-if="c.collapsed" /><ArrowDown v-else /></el-icon>
                              {{ t('chat.thinking') }}
                            </span>
                            <div v-show="!c.collapsed" class="think-text">{{ c.text }}</div>
                          </div>
                          <div v-else-if="c.type === 'tool'" class="tool-card sub">
                            <div class="tool-head" @click="c.collapsed = !c.collapsed">
                              <el-icon class="tool-icon" :class="c.status">
                                <Loading v-if="c.status === 'running'" />
                                <CircleCheck v-else-if="c.status === 'done'" />
                                <CircleClose v-else />
                              </el-icon>
                              <span class="tool-name">{{ c.name }}</span>
                              <span v-if="c.durationMs" class="tool-dur">{{ c.durationMs }}ms</span>
                            </div>
                            <div v-show="!c.collapsed">
                              <div v-if="c.args && c.args !== '{}'" class="tool-args">{{ c.args }}</div>
                              <div v-if="c.result" class="tool-result">{{ c.result }}</div>
                            </div>
                          </div>
                          <div v-else class="text-block sub-text">{{ c.text }}</div>
                        </template>
                      </div>
                      <div v-if="b.result" class="tool-result">{{ b.result }}</div>
                    </div>
                  </div>
                  <!-- 正文块：agent 常回 Markdown，按 md 渲染（内容已经过 DOMPurify 消毒，见 utils/markdown.js） -->
                  <div v-else class="text-block md-body" v-html="renderMarkdown(b.text)"></div>
                </template>
              </div>
              <!-- 兜底渲染：只在没有 blocks 时才用 content（历史消息走这条）。
                   流式消息的正文已经按块渲染过（上面那个 text-block），这里再渲染 content
                   会让同一段回答在气泡里出现两遍。 -->
              <template v-if="!(m.blocks || []).length">
                <div
                  v-if="m.role === 'assistant' && m.content"
                  class="text-block md-body"
                  v-html="renderMarkdown(m.content)"
                ></div>
                <template v-else>{{ m.content || (m.role === 'assistant' ? t('chat.thinkingEllipsis') : '') }}</template>
              </template>

              <!-- 被终止 / 出错的一轮：正文往往是空的，必须在这里说清原因，
                   否则历史里只剩用户那句提问，看不出「这轮为什么没有回答」 -->
              <div
                v-if="m.role === 'assistant' && m.status && m.status !== 'ok'"
                class="msg-status"
                :class="m.status"
              >
                {{ m.status === 'stopped' ? t('chat.stopped') : t('chat.failed') }}<template v-if="m.error">{{ t('chat.statusError', { error: m.error }) }}</template>
              </div>

              <!-- 本轮产物：后端扫描工作区得到的客观事实（这轮写过哪些文件），
                   点一下直接在工作台里打开；价值判断留给用户自己看 -->
              <div v-if="(m.artifacts || []).length" class="msg-artifacts">
                <span class="artifacts-label">{{ t('chat.artifactsCount', { n: m.artifacts.length }) }}</span>
                <div class="artifacts-list">
                  <button
                    v-for="a in m.artifacts"
                    :key="a.path"
                    class="artifact-chip"
                    :title="a.path"
                    @click="openArtifact(a.path)"
                  >
                    <el-icon><Document /></el-icon>
                    <span class="artifact-name">{{ a.name }}</span>
                    <span v-if="a.size" class="artifact-size">{{ formatSize(a.size) }}</span>
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 拖拽手柄：调整输入区高度 -->
        <div class="input-resizer" @mousedown="startInputResize"></div>

        <!-- 输入区 -->
        <div class="chat-input" :style="{ height: inputHeight + 'px' }">
          <div class="input-card">
            <!-- 附件：+ 触发隐藏的 file input，选中即上传到会话工作区 uploads/ -->
            <input ref="attachInputRef" type="file" multiple class="attach-input" @change="onFilesPicked" />
            <div v-if="attachments.length || uploading" class="attach-list">
              <div v-for="(a, i) in attachments" :key="a.path" class="attach-item" :title="a.path">
                <el-icon class="attach-icon"><Document /></el-icon>
                <span class="attach-name">{{ a.name }}</span>
                <span class="attach-size">{{ formatSize(a.size) }}</span>
                <el-icon class="attach-del" :title="t('chat.remove')" @click="removeAttachment(i)"><Close /></el-icon>
              </div>
              <div v-if="uploading" class="attach-item attach-loading">
                <el-icon class="attach-icon"><Loading /></el-icon>{{ t('chat.uploading') }}
              </div>
            </div>
            <el-input
              ref="inputRef"
              v-model="input"
              type="textarea"
              :rows="3"
              resize="none"
              class="input-textarea"
              :placeholder="t('chat.inputPlaceholder')"
              @input="onCommandInput"
              @keydown="onInputKeydown"
            />
            <div class="input-actions">
              <!-- 子调用版本：只管「它调起的 agent / flow」读哪份，本对话的智能体自身始终读草稿。
                   与 flow 调试器的「跟随依赖草稿」同一语义，缺省开。
                   超级智能体例外：它恒读草稿、子调用也恒跟草稿，开关对它无效，
                   所以直接换成只读说明 —— 留一个「关着却仍在跑草稿」的开关只会误导。 -->
              <el-tooltip v-if="selectedIsSupervisor" placement="top"
                          :content="t('chat.supervisorDraftsTip')">
                <span class="follow-drafts readonly">{{ t('chat.supervisorDraftsLabel') }}</span>
              </el-tooltip>
              <el-tooltip v-else placement="top" :content="followDrafts
                ? t('chat.followDraftsOnTip')
                : t('chat.followDraftsOffTip')">
                <label class="follow-drafts">
                  <el-switch v-model="followDrafts" size="small" />
                  <span>{{ t('chat.followDraftsLabel') }}</span>
                </label>
              </el-tooltip>
              <el-button
                circle
                text
                :icon="Plus"
                class="add-btn"
                :disabled="!selectedAgentId || uploading"
                :title="t('chat.addFile')"
                @click="pickFiles"
              />
              <!-- 发送 / 停止：同一个按钮按 streaming 切换
                   空闲时是「发送」（无内容 / 无附件 / 未选 agent 则禁用）；
                   执行中变成红色的「停止」，点击终止本轮运行 -->
              <el-button
                :type="streaming ? 'danger' : 'primary'"
                circle
                :icon="streaming ? VideoPause : Top"
                :disabled="!streaming && (!selectedAgentId || (!input.trim() && !attachments.length))"
                :title="streaming ? t('chat.stop') : t('chat.send')"
                @click="streaming ? stopRun() : send()"
              />
            </div>
          </div>
          <!-- 斜杠命令：只列后端判定为「用户可调用」的技能，可见面与该 agent 的 use_skill 一致
               （未绑定的共享技能不会出现在这里，否则 / 就成了绕过 restricted 的入口）。
               放在输入卡片之外：卡片自身 overflow:hidden，弹层会被它裁掉。 -->
          <div v-if="cmdOpen" class="cmd-panel">
            <div class="cmd-panel-title">{{ t('chat.commandsTitle') }}</div>
            <div v-if="!cmdMatches.length" class="cmd-panel-empty">{{ t('chat.commandsEmpty') }}</div>
            <div v-for="(c, i) in cmdMatches" :key="c.name" class="cmd-item"
                 :class="{ active: i === cmdIndex }" @mousedown.prevent="pickCommand(c)">
              <span class="cmd-name">/{{ c.name }}</span>
              <span class="cmd-desc">{{ c.description }}</span>
              <span v-if="c.dispatch === 'tool'" class="cmd-badge">{{ t('chat.commandsToolBadge') }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- 分割条跟着会话走：会话可见时在工作台左侧（对话区↔工作台）； -->
      <!-- 会话关闭、工作台单独存在时移到右侧（工作台↔内容区）。几何由父级 MainWindow 计算 -->
      <div v-show="wbOpen && chatVisible" class="wb-resizer"
           @mousedown="$emit('wb-resize', $event, 'left')"></div>

      <!-- 工作台 -->
      <Workbench v-show="wbOpen" :divider-side="chatVisible ? 'left' : 'right'"
                 :style="{ width: wbWidth + 'px' }" />

      <div v-show="wbOpen && !chatVisible" class="wb-resizer"
           @mousedown="$emit('wb-resize', $event, 'right')"></div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElSelect, ElOption, ElInput, ElButton, ElIcon, ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, Delete, Document, Plus, Close, Top, ArrowRight, ArrowDown, Loading, CircleCheck, CircleClose, MagicStick, VideoPause, CopyDocument, EditPen } from '@element-plus/icons-vue'
import adminApi from '../config/admin.js'
import { getToken } from '../utils/auth.js'
import { renderMarkdown } from '../utils/markdown.js'
import { sessionContext, requestOpenWsFile } from '../store/sessionContext.js'
import Workbench from './Workbench.vue'
import { formatError } from '../../locales'

// 右侧工作台：开关与宽度由父级 MainWindow 持有，这里只负责渲染与拖拽事件转发。
// chatVisible 用于「会话已关闭、但工作台仍开着」时，只隐藏会话侧栏与对话区。
// sessionPanelOpen：会话侧栏是否展开。父级会持久化它并在刷新时恢复（MainWindow.restoreLayout），
// 由它单向驱动下面 sessionPanelVisible 的初值与后续变化。
const props = defineProps({
  chatVisible: { type: Boolean, default: true },
  wbOpen: { type: Boolean, default: false },
  wbWidth: { type: Number, default: 480 },
  sessionPanelOpen: { type: Boolean, default: true },
})
const emit = defineEmits(['close-chat', 'session-panel-change', 'wb-resize', 'open-workbench', 'open-entity'])

const { t } = useI18n()

const closeChat = () => emit('close-chat')

// ===== 会话 → 内容区联动：建 / 改实体就把右侧内容区切过去并定位到它 =====
// 工具 → 目标页。读类（read_* / list_* / *_run）不抢视图；删除类删完没有目标可看，也不进表。
const NAV_TARGETS = {
  // 工作流：改节点 / 连线、跑流程都落到工作流编辑页（run 还要定位失败节点）
  create_flow: 'wf-instances', update_flow: 'wf-instances',
  create_node: 'wf-instances', insert_node: 'wf-instances',
  update_node: 'wf-instances', delete_node: 'wf-instances',
  create_edge: 'wf-instances', update_edge: 'wf-instances', delete_edge: 'wf-instances',
  run_flow: 'wf-instances',
  // 智能体
  create_agent: 'agents', update_agent: 'agents',
  // 技能：own / shared / agent_skill 三种空间
  create_shared_skill: 'agent-skills', update_shared_skill: 'agent-skills',
  create_own_skill: 'agent-skills', update_own_skill: 'agent-skills',
  create_agent_skill: 'agent-skills', update_agent_skill: 'agent-skills',
  // 自定义工具（工具目录 / 插件是只读或磁盘侧的，会话里建改的是这一种）
  create_custom_tool: 'capabilities', update_custom_tool: 'capabilities',
}
let entityFocusTimer = null
let entityFocusPending = null
// 工具结果在服务端被截断到 2000 字节（agent_impl 的 truncate），JSON.parse 会失败；
// 但 flow_id / id 一般在返回体最前面，正则仍能捞出来 —— 这是解析失败时的兜底。
const pickStringField = (text, key) => {
  const m = new RegExp(`"${key}"\\s*:\\s*"([^"]+)"`).exec(text || '')
  return m ? m[1] : ''
}
// run_flow 的节点轨迹里挑「第一个失败节点」：跑挂了就该停在那个节点上。
const pickFailedNodeId = (text) => {
  const m = /"node_id"\s*:\s*"([^"]+)"[^{}]*?"status"\s*:\s*"failed"/.exec(text || '')
  return m ? m[1] : ''
}
// 一轮里常常是 create 完接着 update / 建连线：去抖合并，取最后一次动作的目标。
// 跨类型也是「最后一次胜出」—— 与工作流同一套语义，行为可预测。
const queueEntityFocus = (data) => {
  if (!data || !data.ok) return
  const nav = NAV_TARGETS[data.tool]
  if (!nav) return
  const raw = typeof data.result === 'string' ? data.result : ''
  let res = null
  try {
    res = raw ? JSON.parse(raw) : null
  } catch {
    res = null
  }
  let args = null
  try {
    args = data.args ? JSON.parse(data.args) : null
  } catch {
    args = null
  }
  const arg = (k) => (args && typeof args[k] === 'string' ? args[k] : '')

  let id = ''
  let name = ''
  let nodeId = ''
  if (nav === 'wf-instances') {
    // flow 标识：flow_id / id；结果被截断时退回调用参数里的 flow / name（Flows 端两者都能匹配）
    id = (res && (res.flow_id || res.id)) || pickStringField(raw, 'flow_id') || arg('flow') || arg('name')
    if (!id) return
    if (data.tool === 'run_flow') {
      nodeId = pickFailedNodeId(raw) || (res?.nodes?.find?.((n) => n.status === 'failed')?.node_id || '')
    } else if (data.tool !== 'delete_node') {
      // 删掉的节点没法定位，只把工作流打开
      nodeId = res?.node?.id || res?.edge?.source || pickStringField(raw, 'node_id')
    }
  } else if (nav === 'capabilities') {
    // 自定义工具按短名定位（页面里也是按 name 取详情），创建 / 更新都把实体裹在 tool 里
    const t = (res && res.tool) || {}
    name = t.name || (res && res.name) || pickStringField(raw, 'name') || arg('name')
    id = String(t.id || '')
    if (!name) return
  } else {
    // 智能体 / 技能：create 回 id + name，update 只回 name（改名时是最终的新名）
    id = (res && res.id) || pickStringField(raw, 'id')
    name = (res && res.name) || pickStringField(raw, 'name') || arg('new_name') || arg('name')
    if (!id && !name) return
  }

  entityFocusPending = {
    nav,
    id: String(id || ''),
    name: String(name || ''),
    node_id: String(nodeId || '')
  }
  clearTimeout(entityFocusTimer)
  entityFocusTimer = setTimeout(() => {
    const p = entityFocusPending
    entityFocusPending = null
    if (p) emit('open-entity', p)
  }, 400)
}

// 点会话里的产物（含 docx / xlsx / zip 这类只能下载的）：先把工作台展开，再请求打开文件。
// 工作台是 v-show 的，收起时标签照样会被创建，只是开在一个看不见的面板里 ——
// 用户会以为点了没反应。
const openArtifact = (path) => {
  emit('open-workbench')
  requestOpenWsFile(path)
}

const agents = ref([])
const selectedAgentId = ref('')
const sessions = ref([])
const currentSessionId = ref('')
// 会话数配额：随列表一起返回（limit === -1 表示不限制，不显示）
const sessionsUsed = ref(0)
const sessionsLimit = ref(-1)
const sessionsQuotaText = computed(() =>
  sessionsLimit.value === -1 ? '' : `${sessionsUsed.value}/${sessionsLimit.value}`)
const sessionsQuotaWarn = computed(() =>
  sessionsLimit.value > 0 && sessionsUsed.value >= sessionsLimit.value)
const messages = ref([])
const input = ref('')
// 斜杠命令（/<技能名>）：列表来自当前 agent 的 user_invocable 技能。
// 每次打开菜单都重新拉：技能设置里改「开放为用户命令 / 直达工具」后，旧快照会让菜单停在改动前的样子，
// 看上去像后端没生效；这是一次只读请求，不值得为它缓存。
const inputRef = ref(null)
const commands = ref([])
const cmdOpen = ref(false)
const cmdIndex = ref(0)
const cmdQuery = ref('')
const streaming = ref(false)
// 当前这一轮对话的 AbortController：停止 = 断开这条 SSE 连接（见 stopRun）
let chatAbort = null
const messagesRef = ref(null)

// 子调用读草稿还是已发布版本：只管本次对话**调起的那些**（call_agent / local__flow / run_flow），
// 本对话的智能体自身始终读草稿。缺省开（与 flow 调试器同一缺省），选择记在本地，刷新后保持。
const readFollowDraftsPref = () => {
  try { return localStorage.getItem('flame.chat.followDrafts') !== '0' } catch { return true }
}
const followDrafts = ref(readFollowDraftsPref())
watch(followDrafts, (v) => {
  try { localStorage.setItem('flame.chat.followDrafts', v ? '1' : '0') } catch { /* 忽略 */ }
})

// ===== 附件（输入框「+」）：上传到会话工作区 uploads/，agent 用文件工具直接读 =====
const attachInputRef = ref(null)
const attachments = ref([])   // [{ name, path, size }]，path 是工作区相对路径（uploads/xxx）
const uploading = ref(false)
const MAX_UPLOAD_BYTES = 20 << 20
const MAX_UPLOAD_FILES = 5
// 初值取父级恢复出来的状态，避免「先展开一帧再收起」
const sessionPanelVisible = ref(props.sessionPanelOpen)
// 侧栏显隐变化 → 通知父级增减 chat 窗口总宽（避免挤压对话区）
watch(sessionPanelVisible, (v) => emit('session-panel-change', v), { immediate: true })
// 父级恢复布局后改这个 prop（如刷新时恢复「侧栏已收起」），跟随它；
// 否则父级按「已收起」算总宽、实际却渲染着侧栏，差额会全压在对话内容区上
watch(() => props.sessionPanelOpen, (v) => {
  if (v !== sessionPanelVisible.value) sessionPanelVisible.value = v
})

// 会话上下文同步给「工作区」面板（Chat 与 Workbench 是兄弟组件，故走模块级 store）
watch([selectedAgentId, currentSessionId], ([aid, sid]) => {
  sessionContext.agentId = aid || ''
  sessionContext.sessionId = sid || ''
}, { immediate: true })
const sessionSearch = ref('')
// 分页加载历史
const hasMore = ref(false)
const oldestCreatedAt = ref('')
const loadingMore = ref(false)
// 输入区高度（可拖拽调整）
const inputHeight = ref(150)

// 助手消息头显示的名字：取当前选中的 agent。切 agent 会清空消息列表，
// 所以每条消息只用「当前 agent 名」即可，不必往消息对象里塞 agent 名。
const agentDisplayName = computed(() => {
  const a = agents.value.find((x) => x.id === selectedAgentId.value)
  return (a && a.name) || t('chat.defaultAgentName')
})

// 超级智能体恒读草稿（后端 resolveDefinitionMode 强制），聊天里的「子调用跟草稿」开关对它无效，
// 所以干脆不渲染开关 —— 显示一个禁用的开关会让人以为关掉就真的跑发布版。
const selectedIsSupervisor = computed(() => {
  const a = agents.value.find((x) => x.id === selectedAgentId.value)
  return !!a?.is_supervisor
})

// 下拉选中态用：按 id 反查 agent（slot 只给 value，不给整条记录）
const agentOf = (id) => agents.value.find((x) => String(x.id) === String(id))

// 执行计时：流式过程中每 200ms 推进一次 nowTick，让「执行用时」实时跳动；
// 跑完由 message.durationMs 定格。历史消息两者都没有 → 那一行不显示。
const nowTick = ref(Date.now())
let tickTimer = null
watch(streaming, (running) => {
  if (running) {
    nowTick.value = Date.now()
    tickTimer = setInterval(() => { nowTick.value = Date.now() }, 200)
  } else if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
})
onBeforeUnmount(() => {
  if (tickTimer) clearInterval(tickTimer)
})

// 耗时取值：跑完用定格值 → 跑着用实时值 → 历史消息 0（不显示）
const elapsedMs = (m) => {
  if (m.durationMs != null) return m.durationMs
  if (m.startedAt && streaming.value) return nowTick.value - m.startedAt
  return 0
}

const elapsedText = (m) => {
  const ms = elapsedMs(m)
  if (ms <= 0) return ''
  if (ms < 1000) return t('chat.elapsedMs', { ms })
  if (ms < 60000) return t('chat.elapsedSeconds', { s: (ms / 1000).toFixed(1) })
  const min = Math.floor(ms / 60000)
  const sec = Math.round((ms % 60000) / 1000)
  return t('chat.elapsedMinutes', { m: min, s: sec })
}

const toggleSessionPanel = () => {
  sessionPanelVisible.value = !sessionPanelVisible.value
}

// 加载类请求失败时把原因说出来。
// 以前这些地方是静默 return：接口一异常（401 / 500 / 路由 404），界面就只剩
// 「暂无对话」，看起来像历史被删了，实际是没读到 —— 排查时完全无从下手。
// ---- 斜杠命令 ----
// 菜单口径由后端 ListCommands 决定（与该 agent 的 use_skill 同源的可见面，再筛 user_invocable）。
// 前端不做权限判断：拿不到列表就当没有命令，手打的 /name 仍由后端解析并校验。
const cmdMatches = computed(() => {
  const q = (cmdQuery.value || '').toLowerCase()
  if (!q) return commands.value
  return commands.value.filter(c =>
    c.name.toLowerCase().includes(q) || (c.description || '').toLowerCase().includes(q))
})

const closeCommands = () => {
  cmdOpen.value = false
  cmdQuery.value = ''
  cmdIndex.value = 0
}

const fetchCommands = async () => {
  const agentId = selectedAgentId.value
  if (!agentId) return
  const token = getToken()
  if (!token) return
  try {
    const res = await fetch(adminApi.agents.commands(agentId), {
      headers: { Authorization: `Bearer ${token}` }
    })
    const body = await res.json().catch(() => ({}))
    // 辅助功能：拉不到就保留上一次的列表（本来没有就当作没有命令），不打断对话（发送时后端还会再判一次）
    if (!res.ok || body.code !== 200) return
    // 请求期间切换了 agent：这份结果属于上一个 agent，丢掉
    if (selectedAgentId.value !== agentId) return
    commands.value = (body.data && body.data.items) || []
  } catch (e) {
    console.error('load commands failed:', e)
  }
}

// 切 agent 后先清空：否则刚切完就敲 "/"，会在新列表回来之前闪出一批不属于它的命令
watch(selectedAgentId, () => {
  commands.value = []
})

// 只有整条输入是 "/token" 时才弹菜单：正文里的路径（/tmp/x）和日期（3/5）不触发；
// 一旦开始写参数（"/name 参数"）就自动收起，免得挡住正文。
// token 允许任意非空白、非斜杠字符（含汉字）：技能名是 ASCII，但描述常是中文，
// 用户按中文描述过滤才有意义；选中后 pickCommand 写回的是真正的技能名，汉字不会流到后端。
const onCommandInput = (val) => {
  const text = typeof val === 'string' ? val : input.value
  const m = /^\/([^\s/]*)$/.exec(text.trim())
  if (!m) { closeCommands(); return }
  cmdQuery.value = m[1]
  cmdIndex.value = 0
  if (!cmdOpen.value) {
    cmdOpen.value = true
    fetchCommands()
  }
}

const pickCommand = (c) => {
  if (!c) return
  input.value = `/${c.name} `
  closeCommands()
  nextTick(() => { if (inputRef.value) inputRef.value.focus() })
}

// 方向键 / Tab / Enter 先交给菜单；其余回车仍然发送。
// （原来模板上是 @keydown.enter.exact.prevent="send"，菜单打开时会把回车抢走）
const onInputKeydown = (e) => {
  if (cmdOpen.value) {
    const list = cmdMatches.value
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (list.length) cmdIndex.value = (cmdIndex.value + 1) % list.length
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (list.length) cmdIndex.value = (cmdIndex.value - 1 + list.length) % list.length
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      closeCommands()
      return
    }
    if (list.length && (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey))) {
      e.preventDefault()
      pickCommand(list[cmdIndex.value])
      return
    }
  }
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    send()
  }
}

const reportLoadError = (what, res, body) => {
  const detail = formatError(body) || (res && !res.ok ? `HTTP ${res.status}` : '')
  ElMessage.error(detail
    ? t('chat.loadErrorWithDetail', { what, detail })
    : t('chat.loadErrorRetry', { what }))
}

const fetchAgents = async () => {
  try {
    const token = getToken()
    if (!token) return
    const response = await fetch(adminApi.agents.base, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ page: 1, limit: -1 })
    })
    if (!response.ok) {
      reportLoadError(t('chat.loadAgentsFailed'), response)
      return
    }
    const data = await response.json()
    if (data.code !== 200) {
      reportLoadError(t('chat.loadAgentsFailed'), response, data)
      return
    }
    agents.value = data.data.items || []
  } catch (e) {
    console.error('load agents failed:', e)
    ElMessage.error(t('chat.loadAgentsNetError'))
  }
}

const fetchSessions = async (keyword = '') => {
  if (!selectedAgentId.value) {
    sessions.value = []
    return
  }
  try {
    const token = getToken()
    if (!token) return
    const base = adminApi.agents.sessions(selectedAgentId.value)
    const url = keyword ? `${base}?keyword=${encodeURIComponent(keyword)}` : base
    const response = await fetch(url, {
      method: 'GET',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!response.ok) {
      reportLoadError(t('chat.loadSessionsFailed'), response)
      return
    }
    const data = await response.json()
    if (data.code !== 200) {
      reportLoadError(t('chat.loadSessionsFailed'), response, data)
      return
    }
    sessions.value = data.data.items || []
    // 列表和配额一起返回：配额只用于展示（计数可能略滞后，拦不拦由后端 507 决定）
    if (data.data.sessions_limit !== undefined) {
      sessionsUsed.value = data.data.sessions_used || 0
      sessionsLimit.value = data.data.sessions_limit ?? -1
    }
  } catch (e) {
    console.error('load sessions failed:', e)
    ElMessage.error(t('chat.loadSessionsNetError'))
  }
}

// 获取当前用户最近一条 agent 会话，返回其 agent_id（无则 null）
const fetchLatestAgent = async () => {
  try {
    const token = getToken()
    if (!token) return null
    const response = await fetch(adminApi.sessions.latest, {
      method: 'GET',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!response.ok) return null
    const data = await response.json()
    if (data.code === 200 && data.data.session) {
      return data.data.session.agent_id
    }
    return null
  } catch (e) {
    console.error('failed to load recent sessions:', e)
    return null
  }
}

let searchTimer = null
const onSessionSearch = () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    fetchSessions(sessionSearch.value)
  }, 300)
}

const handleAgentChange = async () => {
  currentSessionId.value = ''
  messages.value = []
  clearAttachments() // 附件属于某个会话目录，换 agent 后旧列表无效
  await fetchSessions()
  // 优先继续最近的会话（sessions 已按 updated_at 倒序，第一条即最近）
  if (sessions.value.length > 0) {
    await switchSession(sessions.value[0])
  }
}

const createSession = async () => {
  if (!selectedAgentId.value) return
  try {
    const token = getToken()
    if (!token) return
    const response = await fetch(adminApi.agents.sessions(selectedAgentId.value), {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    // 失败必须说出来：会话数超限返回 507（error=quota.user_sessions_exceeded），
    // 这里静默 return 会让人以为「新对话」按钮坏了。
    const data = await response.json().catch(() => ({}))
    if (!response.ok || data.code !== 200) {
      reportLoadError(t('chat.createSessionFailed'), response, data)
      return
    }
    currentSessionId.value = data.data.session_id
    messages.value = []
    clearAttachments()
    fetchSessions()
  } catch (e) {
    console.error('failed to create the session:', e)
    ElMessage.error(t('chat.createSessionNetError'))
  }
}

const startNewChat = async () => {
  await createSession()
  sessionPanelVisible.value = false
}

const switchSession = async (s) => {
  if (streaming.value) return
  currentSessionId.value = s.id
  messages.value = []
  clearAttachments() // 已上传的文件属于上一个会话的工作区
  hasMore.value = false
  oldestCreatedAt.value = ''
  await loadMessages()
}

const loadMessages = async (before = '') => {
  try {
    const token = getToken()
    if (!token) return
    let url = adminApi.sessions.messages(currentSessionId.value) + '?limit=10'
    if (before) url += '&before=' + encodeURIComponent(before)
    const response = await fetch(url, {
      method: 'GET',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (!response.ok) {
      reportLoadError(t('chat.loadMessagesFailed'), response)
      return
    }
    const data = await response.json()
    if (data.code !== 200) {
      reportLoadError(t('chat.loadMessagesFailed'), response, data)
      return
    }
    // 历史消息带 duration_ms（后端 session_records.duration_ms）→ 归一化成 durationMs，
    // 与流式期间的本地计时 / SSE duration_ms 共用同一字段名；0/缺失表示没记录，不显示那一行。
    // status/error：被终止或出错的一轮也会有一条 assistant 消息（正文可能为空），
    // 靠这两个字段在气泡里说明原因，而不是留一行空白。
    const msgs = (data.data.messages || []).map((m) => ({
      role: m.role,
      content: m.content,
      durationMs: m.duration_ms > 0 ? m.duration_ms : null,
      status: m.status || 'ok',
      error: m.error || '',
      at: m.created_at ? Date.parse(m.created_at) : null,
    }))
    messages.value = before ? [...msgs, ...messages.value] : msgs
    hasMore.value = !!data.data.has_more
    oldestCreatedAt.value = data.data.oldest_created_at || ''
    scrollToBottom()
  } catch (e) {
    console.error('load messages failed:', e)
    ElMessage.error(t('chat.loadMessagesNetError'))
  }
}

const loadMore = async () => {
  if (!hasMore.value || !oldestCreatedAt.value || loadingMore.value) return
  loadingMore.value = true
  const el = messagesRef.value
  const prevHeight = el ? el.scrollHeight : 0
  try {
    await loadMessages(oldestCreatedAt.value)
    nextTick(() => {
      if (el) el.scrollTop = el.scrollHeight - prevHeight
    })
  } finally {
    loadingMore.value = false
  }
}

const deleteSession = (s) => {
  ElMessageBox.confirm(t('chat.deleteSessionConfirm'), t('chat.deleteConfirmTitle'), {
    confirmButtonText: t('chat.confirm'),
    cancelButtonText: t('chat.cancel'),
    type: 'warning'
  }).then(async () => {
    try {
      const token = getToken()
      if (!token) return
      const response = await fetch(adminApi.sessions.delete(s.id), {
        method: 'DELETE',
        headers: { 'Authorization': `Bearer ${token}` }
      })
      // 删会话是用户腾配额的手段，失败必须说出来：静默失败会让人以为删掉了、额度却没回来
      const data = await response.json().catch(() => ({}))
      if (!response.ok || data.code !== 200) {
        reportLoadError(t('chat.deleteSessionFailed'), response, data)
        return
      }
      if (currentSessionId.value === s.id) {
        currentSessionId.value = ''
        messages.value = []
      }
      fetchSessions()
    } catch (e) {
      console.error('failed to delete the session:', e)
      ElMessage.error(t('chat.deleteSessionNetError'))
    }
  }).catch(() => {})
}

const scrollToBottom = () => {
  nextTick(() => {
    if (messagesRef.value) {
      messagesRef.value.scrollTop = messagesRef.value.scrollHeight
    }
  })
}

// 拖拽调整输入区高度（80~400px）
const startInputResize = (e) => {
  const startY = e.clientY
  const startHeight = inputHeight.value
  const onMouseMove = (ev) => {
    const delta = startY - ev.clientY
    inputHeight.value = Math.min(400, Math.max(150, startHeight + delta))
  }
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// 合并连续同类型文本块（thinking/text 是分片到达的，避免界面碎片化）
const appendBlock = (list, type, delta) => {
  if (!delta) return
  const last = list[list.length - 1]
  if (last && last.type === type) {
    last.text += delta
  } else {
    const block = { type, text: delta }
    // 思考块默认展开（正在思考时实时可见），思考结束后由 collapseThinking 收起
    if (type === 'thinking') block.collapsed = false
    list.push(block)
  }
}

// 思考结束（开始输出正文或调用工具）→ 自动折叠列表内所有思考块
const collapseThinking = (list) => {
  list.forEach((b) => {
    if (b.type === 'thinking') b.collapsed = true
  })
}

// 当前正在执行的 call_agent 卡片 —— 子智能体的事件挂在它的 children 上
const runningCallBlock = (msg) => {
  for (let i = msg.blocks.length - 1; i >= 0; i--) {
    const b = msg.blocks[i]
    if (b.type === 'tool' && b.name === 'call_agent' && b.status === 'running') return b
  }
  return null
}

// ---- 用户消息：时间 / 复制 / 编辑重发 ----
const editingIndex = ref(-1)
const editingText = ref('')

// 消息时间：只显示「时:分」。历史消息由后端 created_at 提供，新消息用本地时间。
const formatMsgTime = (ts) => {
  if (!ts) return ''
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ''
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

const copyMessage = async (text) => {
  try {
    await navigator.clipboard.writeText(text || '')
    ElMessage.success(t('chat.copied'))
  } catch (e) {
    ElMessage.error(t('chat.copyFailed'))
  }
}

const startEdit = async (idx, m) => {
  if (streaming.value) return
  editingIndex.value = idx
  editingText.value = m.content || ''
  // 编辑框渲染完再量高度：否则拿到的是 0，算不出是否被容器边界挡住
  await nextTick()
  ensureEditVisible()
}

// 悬停浮现「时间 / 复制 / 编辑」时，若这一行被消息区底部裁掉，
// 向下滚刚好让整行露出来。只处理底部：hover 意味着气泡本身已经可见。
const onUserMsgEnter = (e) => {
  const scroller = messagesRef.value
  if (!scroller) return
  const meta = e.currentTarget.querySelector('.msg-meta')
  if (!meta) return
  const metaRect = meta.getBoundingClientRect()
  const areaRect = scroller.getBoundingClientRect()
  const overflow = metaRect.bottom - (areaRect.bottom - 8)
  if (overflow > 0) {
    // 用 smooth 而不是瞬移：hover 是高频动作，瞬移会让内容"跳"一下
    scroller.scrollBy({ top: overflow, behavior: 'smooth' })
  }
}

// 编辑框展开后如果被消息区上下边界挡住，滚动一点点让它和「发送」完整可见。
// 只调整消息区自己的 scrollTop，不牵动整个页面。
const ensureEditVisible = () => {
  const box = document.querySelector('.edit-box')
  const scroller = messagesRef.value
  if (!box || !scroller) return
  const boxRect = box.getBoundingClientRect()
  const areaRect = scroller.getBoundingClientRect()
  const pad = 8

  // 窗口太矮、编辑框本身比可视区还高：对齐顶部，至少能看全输入区（发送按钮在下面，
  // 这种情况只能让用户自己再滚，硬滚到底会把输入框顶出去）。
  if (boxRect.height > areaRect.height - pad * 2) {
    scroller.scrollBy({ top: boxRect.top - (areaRect.top + pad), behavior: 'smooth' })
    return
  }

  const overflowBottom = boxRect.bottom - (areaRect.bottom - pad)
  if (overflowBottom > 0) {
    scroller.scrollBy({ top: overflowBottom, behavior: 'smooth' })
    return
  }
  const overflowTop = areaRect.top + pad - boxRect.top
  if (overflowTop > 0) {
    scroller.scrollBy({ top: -overflowTop, behavior: 'smooth' })
  }
}

const cancelEdit = () => {
  editingIndex.value = -1
  editingText.value = ''
}

// 编辑后重发：先丢掉这条及其之后的本地消息，再用新文本走同一条发送路径。
// 注意：后端会话记录不会回滚（目前没有按消息截断的接口），刷新后旧记录还会在。
const submitEdit = async () => {
  const text = editingText.value.trim()
  const idx = editingIndex.value
  if (!text || idx < 0) return
  messages.value = messages.value.slice(0, idx)
  cancelEdit()
  await sendMessage(text)
}

// 终止当前运行：直接断掉这条 SSE 连接，由后端按「客户端断开」终止本轮。
//
// 为什么不发 /stop：那是一条新的 HTTP 请求，多实例部署时会被负载均衡分到别的实例上，
// 而运行台账在进程内 —— 分错了就是 stopped:0，本机这一轮照跑。连接断开不会走错：
// FIN 只会到达握着这条连接的那个实例。
const stopRun = () => {
  if (!streaming.value) return
  chatAbort?.abort()
}

// ===== 附件上传 =====
const formatSize = (n) => {
  if (!n) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

const pickFiles = () => {
  if (streaming.value || uploading.value || !selectedAgentId.value) return
  attachInputRef.value?.click()
}
const removeAttachment = (i) => { attachments.value.splice(i, 1) }
const clearAttachments = () => { attachments.value = [] }

const onFilesPicked = async (e) => {
  const picked = Array.from(e.target.files || [])
  e.target.value = '' // 清空才能再次选同一个文件
  if (picked.length) await uploadAttachments(picked)
}

// 上传：先落盘再发消息 —— 消息到达时文件必须已经能被工具读到。
// 新对话上传会顺手建会话（后端返回 session_id），这里接住，保证「文件目录 = 本次对话目录」。
const uploadAttachments = async (picked) => {
  const token = getToken()
  if (!token || !selectedAgentId.value) return
  const room = MAX_UPLOAD_FILES - attachments.value.length
  if (room <= 0) {
    ElMessage.warning(t('chat.tooManyFiles', { n: MAX_UPLOAD_FILES }))
    return
  }
  uploading.value = true
  try {
    for (const f of picked.slice(0, room)) {
      if (f.size > MAX_UPLOAD_BYTES) {
        ElMessage.warning(t('chat.fileTooLarge', { name: f.name, size: MAX_UPLOAD_BYTES >> 20 }))
        continue
      }
      const fd = new FormData()
      fd.append('file', f)
      if (currentSessionId.value) fd.append('session_id', currentSessionId.value)
      try {
        // 不手动设 Content-Type：multipart 的 boundary 必须由浏览器生成
        const res = await fetch(adminApi.agents.uploadSessionFile(selectedAgentId.value), {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: fd
        })
        const body = await res.json().catch(() => ({}))
        if (!res.ok || body.code !== 200) {
          // 507：预检可能已经删掉旧产物才判定放不下，先说删了什么，否则文件像凭空消失
          const n = Array.isArray(body.data && body.data.evicted) ? body.data.evicted.length : 0
          if (n) ElMessage.warning(t('chat.evicted', { n }))
          throw new Error(formatError(body) || t('chat.uploadFailed'))
        }
        const d = body.data || {}
        if (d.session_id && !currentSessionId.value) {
          currentSessionId.value = d.session_id
          fetchSessions()
        }
        attachments.value.push({ name: d.name || f.name, path: d.path, size: d.size || f.size })
      } catch (err) {
        ElMessage.error(t('chat.uploadFileFailed', {
          name: f.name, msg: err.message || t('chat.uploadFailed')
        }))
      }
    }
  } finally {
    uploading.value = false
  }
}

const send = async () => {
  if (streaming.value || uploading.value || !selectedAgentId.value) return
  // 只贴文件不写字也允许：给一句中性指令，免得空消息被后端拒
  const text = input.value.trim() || (attachments.value.length ? t('chat.attachOnlyMessage') : '')
  if (!text) return

  input.value = ''
  await sendMessage(text)
}

// sendMessage 用指定文本发起一轮对话：输入框发送与「编辑后重发」共用同一条路径。
const sendMessage = async (text) => {
  if (streaming.value || !selectedAgentId.value) return
  // 附件随这一轮带走（快照 + 清空）：发送失败也不该留在输入框里重复发送
  const files = attachments.value.slice()
  attachments.value = []
  if (!text && !files.length) return
  messages.value.push({
    role: 'user', content: text, files: files.map((f) => f.name), at: Date.now()
  })
  // blocks：按事件顺序记录「思考 / 工具调用 / 正文」，用于展示执行过程
  // 必须用 reactive：普通对象修改属性不会触发视图更新（会表现为「内容最后一起出现」）
  // startedAt / durationMs：给「执行用时」用。durationMs 为 null 表示还在跑（走实时计时）
  // artifacts：本轮产物（后端在 RUN_FINISHED 里下发），只在「刚跑完」的这一轮可见，
  // 刷新后历史消息没有它（session_records 只存了正文与耗时）。
  const assistantMsg = reactive({ role: 'assistant', content: '', blocks: [], artifacts: [], startedAt: Date.now(), durationMs: null })
  messages.value.push(assistantMsg)
  streaming.value = true
  scrollToBottom()

  const token = getToken()
  if (!token) {
    assistantMsg.content = t('chat.notLoggedIn')
    assistantMsg.durationMs = Date.now() - assistantMsg.startedAt
    streaming.value = false
    return
  }

  const body = { message: text }
  if (currentSessionId.value) {
    body.session_id = currentSessionId.value
  }
  if (files.length) {
    body.files = files.map((f) => f.path)
  }
  // 每轮都带：开关只对「本轮调起的子实体」生效，改完立刻对下一轮有效
  body.follow_drafts = followDrafts.value

  // 这一轮的连接句柄：stopRun 靠 abort 它来终止本轮（断连 = 停止信号）
  const ctl = new AbortController()
  chatAbort = ctl
  try {
    const response = await fetch(adminApi.agents.chat(selectedAgentId.value), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify(body),
      signal: ctl.signal
    })

    if (!response.ok) {
      assistantMsg.content = t('chat.requestFailed', { status: response.status })
      return
    }

    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    const handleFrame = (frame) => {
      let event = ''
      let dataStr = ''
      for (const line of frame.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) dataStr += line.slice(5).trim()
      }
      if (!event) return

      let data = {}
      try { data = dataStr ? JSON.parse(dataStr) : {} } catch (e) { /* 忽略 */ }

      if (event === 'RUN_STARTED') {
        if (!currentSessionId.value && data.session_id) {
          currentSessionId.value = data.session_id
          fetchSessions()
        }
        return
      }

      // 带 agent 字段的是子智能体事件 → 挂到当前 running 的 call_agent 卡片内
      const subBlock = data.agent ? runningCallBlock(assistantMsg) : null
      const list = subBlock ? subBlock.children : assistantMsg.blocks

      if (event === 'THINKING_DELTA') {
        appendBlock(list, 'thinking', data.delta || '')
      } else if (event === 'TEXT_MESSAGE_CONTENT') {
        collapseThinking(list) // 开始输出正文 → 收起思考
        if (!data.agent) assistantMsg.content += data.delta || '' // 只有主会话 agent 正文计入消息体
        appendBlock(list, 'text', data.delta || '')
      } else if (event === 'TOOL_CALL_START') {
        collapseThinking(list) // 开始调用工具 → 收起思考
        list.push({
          type: 'tool',
          callId: data.call_id || '',
          name: data.tool || 'tool',
          args: data.args || '',
          status: 'running',
          collapsed: true,
          children: [] // 若该工具是 call_agent，子智能体的过程会挂到这里
        })
        // 试运行可能要跑很久：一开始就把内容区切到该工作流，跑完再按失败节点定位
        if (data.tool === 'run_flow') {
          queueEntityFocus({ tool: 'run_flow', ok: true, args: data.args, result: '' })
        }
      } else if (event === 'TOOL_CALL_END') {
        collapseThinking(list)
        const b = list.find((x) => x.type === 'tool' && x.callId === data.call_id)
        if (b) {
          b.status = data.ok ? 'done' : 'error'
          b.result = data.result || ''
          b.durationMs = data.duration_ms || 0
        }
        queueEntityFocus(data)
      } else if (event === 'RUN_FINISHED') {
        // 服务端权威耗时（与写进 session_records 的同源），避免"刚跑完"与"刷新后"两个数不一致
        if (data.duration_ms > 0) assistantMsg.durationMs = data.duration_ms
        // 本轮产物：后端扫工作区得到的「这轮写过哪些文件」，点一下直接在工作台里打开
        assistantMsg.artifacts = Array.isArray(data.artifacts) ? data.artifacts : []
      } else if (event === 'RUN_ERROR') {
        if (data.duration_ms > 0) assistantMsg.durationMs = data.duration_ms
        const errText = data.message ? t('chat.runError', { msg: data.message }) : ''
        if (errText) {
          assistantMsg.content += '\n' + errText // 留在 content：复制按钮读的是它
          // 同时进块：模板只在「无 blocks」时渲染 content，
          // 此时通常已有思考块，不加这一条错误提示就看不见了。
          appendBlock(assistantMsg.blocks, 'text', errText)
        }
      }
      scrollToBottom()
    }

    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })

      let idx
      while ((idx = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, idx)
        buffer = buffer.slice(idx + 2)
        handleFrame(frame)
      }
    }
  } catch (e) {
    if (e && e.name === 'AbortError') {
      // 用户点了停止：连接已断，服务端据断连终止本轮并记为 stopped。
      // 断开后就收不到 RUN_ERROR 了，这里自己补一条终止标记（与 RUN_ERROR 同形状：
      // 同时进 content（复制按钮读它）和块（模板只在无 blocks 时渲染 content））。
      const stopText = t('chat.stoppedMark')
      assistantMsg.content += (assistantMsg.content ? '\n' : '') + stopText
      appendBlock(assistantMsg.blocks, 'text', stopText)
    } else {
      console.error('chat failed:', e)
      if (!assistantMsg.content) {
        assistantMsg.content = t('chat.chatFailed')
      }
    }
  } finally {
    // 无论正常结束、RUN_ERROR 还是主动断开，都定格本轮耗时（唯一一处收口）
    if (assistantMsg.durationMs == null) {
      assistantMsg.durationMs = Date.now() - assistantMsg.startedAt
    }
    if (chatAbort === ctl) chatAbort = null
    streaming.value = false
    scrollToBottom()
  }
}

onMounted(async () => {
  await fetchAgents()
  // 优先恢复 sessions 表里最近对话的 agent；没有则选 main
  let target = null
  const latest = await fetchLatestAgent()
  if (latest) {
    target = agents.value.find(a => a.id === latest)
  }
  if (!target) {
    target = agents.value.find(a => a.name === 'main' || a.id === 'main')
  }
  if (target) {
    selectedAgentId.value = target.id
    await handleAgentChange()
  }
})
</script>

<style scoped>
.agent-chat {
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.chat-body {
  flex: 1;
  display: flex;
  flex-direction: row;
  overflow: hidden;
  min-height: 0;
}

.session-panel {
  width: 200px;
  flex-shrink: 0;
  border-right: 1px solid #e8e8e8;
  background-color: #fafafa;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.session-new {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 12px;
  padding: 10px 12px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 14px;
  color: #333;
  transition: all 0.2s;
}

.session-new:hover {
  background-color: #f0f0f0;
  color: #1890ff;
}

.session-new.disabled {
  color: #c0c4cc;
  cursor: not-allowed;
}

.session-new.disabled:hover {
  background-color: transparent;
  color: #c0c4cc;
}

/* 会话数配额：本账号已用 / 上限（跨智能体计），超限变红 */
.session-quota {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
}

.session-quota.warn {
  color: #f56c6c;
}

.session-search {
  width: 176px;
  margin: 0 5px 8px 12px;
}

.session-search :deep(.el-input__wrapper) {
  box-shadow: none !important;
  background-color: #f1f1f3;
}

.agent-select :deep(.el-select__wrapper) {
  box-shadow: none !important;
  background-color: #e8e8ea;
  border-radius: 15px;
}

/* 选中态里的说明：比名字小一号，颜色沿用超级权限的紫，不抢主体文字 */
.agent-selected-hint {
  margin-left: 6px;
  font-size: 11px;
  color: #8b5cf6;
}

/* 下拉项里的「超级智能体」徽章：与智能体卡片上的超级权限同色（紫） */
.agent-opt-badge {
  margin-left: 8px;
  padding: 0 5px;
  font-size: 10px;
  line-height: 15px;
  vertical-align: 1px;
  color: #8b5cf6;
  background: #f3efff;
  border-radius: 999px;
}

.session-section-title {
  padding: 12px 12px 6px;
  font-size: 12px;
  color: #999;
}

.session-list {
  flex: 1;
  overflow-y: auto;
}

.session-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px;
  cursor: pointer;
  font-size: 14px;
  color: #555;
  transition: background 0.2s;
}

.session-item:hover {
  background-color: #f0f0f0;
}

.session-item.active {
  background-color: #e6f0f8;
  color: #1890ff;
}

.session-item-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-item-delete {
  flex-shrink: 0;
  color: #bbb;
  opacity: 0;
  transition: opacity 0.2s;
}

.session-item:hover .session-item-delete {
  opacity: 1;
}

.session-item-delete:hover {
  color: #f56c6c;
}

.session-empty {
  text-align: center;
  color: #999;
  font-size: 13px;
  padding: 24px 0;
}

/* 对话内容区（「开始对话吧」那块）：
   最小宽度与工作区（工作台）最小宽保持一致 —— MainWindow 的
   CHAT_MIN_WIDTH / WB_MIN_WIDTH 都是 300，改动时三处要一起改。
   侧栏、工作台都是 flex-shrink:0 的定宽块，宽度不足时缺口会全被
   flex:1 的这一块吸收，所以必须显式给 min-width，否则会被挤成一条缝。 */
.chat-main {
  flex: 1;
  min-width: 300px;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* 内容区折叠时对话面板被 flex 撑满整行，此时允许它让位，免得把工作台挤出面板 */
.chat-panel--full .chat-main {
  min-width: 0;
}

/* 工作台拖拽条（工作台本体的分割线在 Workbench.vue 内） */
.wb-resizer {
  width: 5px;
  flex-shrink: 0;
  cursor: col-resize;
  background: transparent;
  transition: background 0.2s;
}

.wb-resizer:hover {
  background: #e0e0e0;
}

.chat-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px;
  margin-top: 8px;
  border-bottom: 1px solid #f0f0f0;
  flex-shrink: 0;
}

.chat-toolbar .active {
  color: #1890ff;
  background-color: #e6f0f8;
}

.chat-messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  background-color: #fff;
  display: flex;
  flex-direction: column;
  gap: 12px;
  scrollbar-gutter: stable;
  scrollbar-color: transparent transparent;
}

.chat-messages:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.chat-messages::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.chat-messages::-webkit-scrollbar-track {
  background: transparent;
}

.chat-messages::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.chat-messages:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.load-more {
  text-align: center;
  flex-shrink: 0;
}

.chat-empty {
  margin: auto;
  text-align: center;
  color: #999;
  font-size: 13px;
}

.empty-icon {
  font-size: 32px;
  margin-bottom: 8px;
}

/* 纵向排列：助手消息是「消息头 + 正文」两段（正文不套气泡），用户消息只有气泡 */
.message-row {
  display: flex;
  flex-direction: column;
}

.message-row.user {
  align-items: flex-end;   /* 用户气泡靠右（原先靠 row 的 justify-content: flex-end） */
}

.message-row.assistant {
  align-items: stretch;
  gap: 4px;                /* 消息头 / 执行用时 / 正文之间的间距 */
}

.bubble {
  max-width: 85%;
  padding: 10px 14px;
  border-radius: 12px;
  font-size: 14px;
  line-height: 1.6;
  word-break: break-word;
  white-space: pre-wrap;
}

.message-row.user .bubble {
  background-color: #1890ff;
  color: #fff;
  border-bottom-right-radius: 4px;
}

/* ---- 用户消息：气泡 + 下方一行「时间 / 复制 / 编辑」（仿 workbuddy）---- */
.user-msg {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  max-width: 85%;          /* 宽度由外层限制，气泡自身不再算百分比，避免非常规 flex 下失效 */
}
.user-msg .bubble { max-width: 100%; }

/* 用户消息里附带的文件（只在刚发出的那条上有；历史记录只存正文） */
.msg-files {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  justify-content: flex-end;
}
.msg-file {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 240px;
  padding: 2px 8px;
  border-radius: 6px;
  background: #eef3ff;
  color: #3b6fd4;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.msg-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 20px;            /* 定高：hover 显隐不引起上下跳动 */
  opacity: 0;
  transition: opacity 0.12s ease;
}
.user-msg:hover .msg-meta { opacity: 1; }

.msg-time {
  font-size: 12px;
  color: #b1b3b8;
  font-variant-numeric: tabular-nums;
}

.msg-actions {
  display: flex;
  gap: 2px;
}

.msg-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: #909399;
  cursor: pointer;
  font-size: 14px;
  transition: background 0.12s, color 0.12s;
}
.msg-action:hover { background: #f2f3f5; color: #1890ff; }

/* 编辑态：整块用灰色背景承载，不用输入框边框（仿 workbuddy）；
   宽度收敛到 380px，长消息也不铺满整行；margin-top 与气泡留出呼吸感 */
.edit-box {
  width: min(85%, 380px);
  margin-top: 8px;
  padding: 8px 10px;
  border-radius: 10px;
  background-color: #f2f3f5;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
/* textarea 自身透明无边框：灰色的"面"由 .edit-box 提供，
   同时清掉 Element Plus 在 hover / focus 时加的 inset 阴影，否则会出现一圈描边 */
.edit-input :deep(.el-textarea__inner),
.edit-input :deep(.el-textarea__inner:hover),
.edit-input :deep(.el-textarea__inner:focus) {
  background: transparent;
  border: none;
  box-shadow: none;
  padding: 2px 4px;
  font-size: 14px;
  line-height: 1.6;
  color: #303133;
}
.edit-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

/* 助手回复：去气泡（仿 workbuddy）—— 无边框 / 无底色 / 无内边距，与消息头的图标左对齐；
   归属与耗时交给上面的 .msg-head / .msg-status，这里只管正文排版。 */
.message-row.assistant .bubble {
  max-width: 100%;
  background-color: transparent;
  border: none;
  border-radius: 0;
  padding: 0;
  color: #333;
}

/* ---- 过程展示：思考块 / 工具卡片 / 正文块 ---- */
.blocks { white-space: normal; }

/* ---- 助手消息头（仿 workbuddy：图标 + 名字，下一行执行用时） ---- */
.msg-head {
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: normal;   /* 独立的块：关掉继承来的 pre-wrap，免得图标与名字被拆行 */
}
/* 助手徽标：用火焰橙（呼应产品 logo），刻意避开用户气泡的 #1890ff ——
   同色会让「谁说的」一眼分不清。 */
.msg-avatar {
  width: 22px;
  height: 22px;
  flex-shrink: 0;
  border-radius: 6px;
  background: linear-gradient(135deg, #ff9a3d 0%, #f5571d 100%);
  color: #fff;
  font-size: 14px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.msg-name { font-size: 13px; font-weight: 600; color: #303133; }
.msg-status {
  margin-left: 28px;   /* 28 = 头像 22 + gap 6：与名字左对齐；与上下两块的间距由行的 gap 给 */
  font-size: 12px;
  color: #909399;
  white-space: normal;
}
.think-block {
  margin-bottom: 6px;
  border-left: 2px solid #dcdfe6;
  padding-left: 8px;
}
.think-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: #909399;
  cursor: pointer;
}
.think-text {
  font-size: 12px;
  color: #909399;
  white-space: pre-wrap;
  word-break: break-word;
  margin-top: 4px;
}
.tool-card {
  margin: 6px 0;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
  padding: 6px 8px;
  background: #fafafa;
}
.tool-head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  cursor: pointer;
}
.tool-icon { color: #409eff; }
.tool-icon.done { color: #67c23a; }
.tool-icon.error { color: #f56c6c; }
.tool-name { font-weight: 600; color: #303133; }
.tool-dur { color: #909399; margin-left: auto; }
.tool-args,
.tool-result {
  font-size: 12px;
  color: #606266;
  white-space: pre-wrap;
  word-break: break-word;
  margin-top: 4px;
  max-height: 160px;
  overflow: auto;
}
.tool-result { color: #67c23a; }
.text-block { white-space: pre-wrap; word-break: break-word; }

/* ---- 本轮结果提示 ----
   历史里被终止 / 出错的一轮：正文可能为空，用一行说明代替空白气泡。
   stopped 用灰、failed 用红，都与正文区分开，不抢回答本身的注意力。 */
.msg-status {
  margin-top: 8px;
  padding: 6px 10px;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.6;
  word-break: break-word;
}
.msg-status.stopped { color: #909399; background: #f4f4f5; }
.msg-status.failed  { color: #f56c6c; background: #fef0f0; }

/* ---- 本轮产物 ----
   后端 RUN_FINISHED 下发的事实清单（这轮写过哪些文件）。
   一条一行、可点开：点击后由 Workbench 响应 sessionContext.openNonce。 */
.msg-artifacts {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px dashed #e4e7ed;
}
.artifacts-label {
  display: block;
  font-size: 12px;
  color: #909399;
  margin-bottom: 6px;
}
.artifacts-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.artifact-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 100%;
  padding: 3px 8px;
  border: 1px solid #dcdfe6;
  border-radius: 999px;
  background: #fafafa;
  color: #409eff;
  font-size: 12px;
  cursor: pointer;
}
.artifact-chip:hover { background: #ecf5ff; border-color: #b3d8ff; }
.artifact-name {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.artifact-size { color: #909399; }

/* ---- Markdown 正文块 ----
   内容由 v-html 注入，元素上没有 scoped 属性，所以子元素样式必须走 :deep()。
   white-space 改回 normal：md 渲染出的 HTML 自带块级换行，再叠一层继承来的
   pre-wrap 会把源码里的换行也显示出来（段落之间多出空行）。 */
.md-body { white-space: normal; }
.md-body :deep(p) { margin: 0 0 8px; }
.md-body :deep(p:last-child) { margin-bottom: 0; }
.md-body :deep(h1),
.md-body :deep(h2),
.md-body :deep(h3),
.md-body :deep(h4),
.md-body :deep(h5),
.md-body :deep(h6) { margin: 12px 0 8px; line-height: 1.35; font-weight: 600; }
.md-body :deep(h1) { font-size: 20px; }
.md-body :deep(h2) { font-size: 17px; }
.md-body :deep(h3) { font-size: 15px; }
.md-body :deep(h4),
.md-body :deep(h5),
.md-body :deep(h6) { font-size: 14px; }
.md-body :deep(ul),
.md-body :deep(ol) { margin: 0 0 8px; padding-left: 22px; }
.md-body :deep(li) { margin: 2px 0; }
.md-body :deep(a) { color: #1890ff; text-decoration: none; }
.md-body :deep(a:hover) { text-decoration: underline; }
.md-body :deep(code) {
  font-family: Consolas, Monaco, 'Courier New', monospace;
  font-size: 12.5px;
  background: #f2f3f5;
  border-radius: 4px;
  padding: 1px 5px;
}
.md-body :deep(pre) {
  margin: 0 0 8px;
  padding: 10px 12px;
  background: #f6f7f9;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow-x: auto;
}
.md-body :deep(pre code) { background: transparent; padding: 0; line-height: 1.55; }
.md-body :deep(blockquote) {
  margin: 0 0 8px;
  padding: 2px 0 2px 10px;
  border-left: 3px solid #dcdfe6;
  color: #606266;
}
.md-body :deep(table) { border-collapse: collapse; margin: 0 0 8px; font-size: 13px; }
.md-body :deep(th),
.md-body :deep(td) { border: 1px solid #e4e7ed; padding: 4px 8px; }
.md-body :deep(th) { background: #fafafa; font-weight: 600; }
.md-body :deep(hr) { border: none; border-top: 1px solid #e8e8e8; margin: 12px 0; }
.md-body :deep(img) { max-width: 100%; border-radius: 4px; }

/* 子智能体过程（嵌在 call_agent 卡片内） */
.sub-blocks {
  margin-top: 6px;
  padding-left: 8px;
  border-left: 2px solid #e4e7ed;
}
.sub-label { font-size: 12px; color: #909399; margin-bottom: 4px; }
.tool-card.sub { background: #fff; }
.sub-text { font-size: 13px; color: #606266; }

.bubble.empty {
  color: #999;
}

.input-resizer {
  height: 5px;
  flex-shrink: 0;
  cursor: ns-resize;
  background: #f7f8fa;
  transition: background 0.2s;
}

.input-resizer:hover {
  background: #e0e0e0;
}

.chat-input {
  flex-shrink: 0;
  padding: 12px;
  background-color: #fff;
  display: flex;
  flex-direction: column;
  /* 命令弹层向上溢出输入区：自身必须放行，它仍落在 .chat-main 的边界内，不会被外层裁掉 */
  position: relative;
  overflow: visible;
}

/* 斜杠命令弹层：贴在输入卡片上方，最多 260px 高，超出滚动 */
.cmd-panel {
  position: absolute;
  left: 12px;
  right: 12px;
  bottom: calc(100% - 4px);
  max-height: 260px;
  overflow-y: auto;
  padding: 6px;
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 10px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  z-index: 40;
}

.cmd-panel-title {
  padding: 4px 8px 6px;
  font-size: 12px;
  color: #909399;
}

.cmd-panel-empty {
  padding: 10px 8px;
  font-size: 13px;
  color: #909399;
}

.cmd-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 6px;
  cursor: pointer;
}

.cmd-item.active,
.cmd-item:hover {
  background: #f2f6fc;
}

.cmd-name {
  flex-shrink: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 13px;
  color: #1f6feb;
}

.cmd-desc {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font-size: 12px;
  color: #909399;
}

.cmd-badge {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 8px;
  font-size: 11px;
  line-height: 18px;
  color: #b26a00;
  background: #fff4e0;
}

.input-card {
  flex: 1;
  display: flex;
  flex-direction: column;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  background-color: #fff;
  padding: 8px;
  overflow: hidden;
}

/* 附件：隐藏的原生 file input + 输入框上方的已选文件条 */
.attach-input { display: none; }
.attach-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 4px 4px 0;
  flex-shrink: 0;
  max-height: 72px;         /* 选满 5 个也不挤占正文输入区 */
  overflow-y: auto;
}
.attach-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 220px;
  padding: 2px 6px;
  border-radius: 6px;
  background: #f2f3f5;
  color: #606266;
  font-size: 12px;
}
.attach-icon { font-size: 13px; color: #909399; }
.attach-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.attach-size { color: #a0a4aa; font-size: 11px; flex-shrink: 0; }
.attach-del { cursor: pointer; color: #a0a4aa; flex-shrink: 0; }
.attach-del:hover { color: #f56c6c; }
.attach-loading { color: #909399; }

.input-textarea {
  flex: 1;
  min-height: 0;
}

.input-textarea :deep(.el-textarea) {
  height: 100%;
}

.input-textarea :deep(.el-textarea__inner) {
  height: 100%;
  box-shadow: none !important;
}

.input-actions {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  flex-shrink: 0;
}

/* 「子调用跟草稿」：次要设置，靠 margin-right:auto 推到最左，不与发送按钮抢注意力 */
.follow-drafts {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-right: auto;
  font-size: 12px;
  color: #8a8f99;
  cursor: pointer;
  user-select: none;
}
.follow-drafts:hover { color: #606266; }

/* 只读说明（超级智能体）：不显示手型、不加 hover，视觉上明确「这不是开关」 */
.follow-drafts.readonly,
.follow-drafts.readonly:hover {
  color: #b1b3b8;
  cursor: default;
}
</style>
