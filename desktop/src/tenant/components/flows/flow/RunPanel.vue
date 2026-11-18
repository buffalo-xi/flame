<template>
  <div class="run-panel" ref="panelRef" :style="{ width: props.panelWidth + 'px' }">
    <div class="panel-resizer" @mousedown="startResize"></div>
    <div class="panel-header">
      <div class="panel-tabs">
        <button class="panel-tab-btn" :class="{ active: activeTab === 'debug' }" @click="activeTab = 'debug'">本次运行</button>
        <button class="panel-tab-btn" :class="{ active: activeTab === 'history' }" @click="activeTab = 'history'">历史运行</button>
      </div>
      <span
        v-if="activeTab === 'debug' && debugRuns.length"
        class="collapse-all-btn"
        :title="allCollapsed ? '全部展开' : '全部收起'"
        @click="toggleAllCards"
      >{{ allCollapsed ? '全部展开' : '全部收起' }}</span>
      <span v-if="activeTab === 'debug'" class="clear-icon" title="清空所有卡片" @click="onClearRuns"><el-icon><Delete /></el-icon></span>
      <span class="close-icon" @click="$emit('close')">×</span>
    </div>
    <template v-if="activeTab === 'debug'">
    <div ref="bodyRef" class="panel-body pane-scroll">
      <div class="panel-content">
        <div v-if="debugRuns.length" ref="runCardsRef" class="run-cards pane-scroll">
          <div v-for="run in debugRuns" :key="run.id" class="run-group">
            <!-- 运行中：展示完整过程卡片 -->
            <template v-if="run.status === 'running'">
              <!-- 卡片 1：输入 -->
              <div class="run-card">
                <div class="run-card-head">
                  <span class="run-card-title">输入</span>
                  <a class="run-card-toggle" @click="toggleCard(run.id + '-input')">{{ collapsedMap[run.id + '-input'] ? '展开' : '收起' }}</a>
                </div>
                <pre v-show="!collapsedMap[run.id + '-input']" class="run-part-input">{{ JSON.stringify(run.input, null, 2) }}</pre>
              </div>
              <!-- 卡片 2：执行 -->
              <div class="run-card">
                <div class="run-card-head">
                  <span class="run-card-title">执行</span>
                  <a class="run-card-toggle" @click="toggleCard(run.id + '-exec')">{{ collapsedMap[run.id + '-exec'] ? '展开' : '收起' }}</a>
                </div>
                <div v-show="!collapsedMap[run.id + '-exec']">
                  <div v-for="(log, i) in run.logs" :key="i" class="output-line" :class="log.type">{{ log.message }}</div>
                </div>
              </div>
              <!-- 卡片 3：流式进度 -->
              <div class="run-card">
                <div class="run-card-head">
                  <span class="run-card-title">过程信息-progress</span>
                  <a class="run-card-toggle" @click="toggleCard(run.id + '-progress')">{{ collapsedMap[run.id + '-progress'] ? '展开' : '收起' }}</a>
                </div>
                <div v-show="!collapsedMap[run.id + '-progress']">
                  <pre v-if="run.progress" class="run-part-progress pane-scroll">{{ run.progress }}</pre>
                  <div v-else class="run-part-empty">无信息</div>
                </div>
              </div>
              <!-- 卡片 4：流式结果 -->
              <div class="run-card">
                <div class="run-card-head">
                  <span class="run-card-title">过程信息-result</span>
                  <a class="run-card-toggle" @click="toggleCard(run.id + '-result')">{{ collapsedMap[run.id + '-result'] ? '展开' : '收起' }}</a>
                </div>
                <div v-show="!collapsedMap[run.id + '-result']">
                  <ResultView v-if="run.result" :text="run.result" />
                  <div v-else class="run-part-empty">无信息</div>
                </div>
              </div>
              <!-- 卡片 5：结果 -->
              <div class="run-card">
                <div class="run-card-head">
                  <span class="run-card-title run-card-title-final">结果</span>
                  <a class="run-card-toggle" @click="toggleCard(run.id + '-final')">{{ collapsedMap[run.id + '-final'] ? '展开' : '收起' }}</a>
                </div>
                <div v-show="!collapsedMap[run.id + '-final']">
                  <ResultView v-if="run.finalResult" :text="run.finalResult" />
                  <div v-else class="run-part-empty">无结果</div>
                </div>
              </div>
            </template>
            <!-- 运行结束：tab 卡片，默认显示结果 -->
            <template v-else>
              <div class="run-tabs">
                <span class="run-tab" :class="{ active: (activeRunTab[run.id] || 'final') === 'final' }" @click="setRunTab(run.id, 'final')">结果</span>
                <span class="run-tab" :class="{ active: activeRunTab[run.id] === 'progress' }" @click="setRunTab(run.id, 'progress')">过程</span>
                <span class="run-tab" :class="{ active: activeRunTab[run.id] === 'exec' }" @click="setRunTab(run.id, 'exec')">执行</span>
                <span class="run-tab" :class="{ active: activeRunTab[run.id] === 'input' }" @click="setRunTab(run.id, 'input')">输入</span>
                <span class="run-meta">
                  <span class="run-time">{{ run.startTime }}</span>
                  <span v-if="run.endTs" class="run-cost">{{ formatCost(run.endTs - run.startTs) }}</span>
                </span>
                <span class="run-status" :class="runStatusClass(run.status)">{{ runStatusText(run.status) }}</span>
                <a class="run-card-toggle run-tabs-toggle" @click="toggleCard(run.id + '-group')">{{ collapsedMap[run.id + '-group'] ? '展开' : '收起' }}</a>
              </div>
              <div v-show="!collapsedMap[run.id + '-group']" class="run-tab-body">
                <template v-if="(activeRunTab[run.id] || 'final') === 'final'">
                  <div v-if="run.error" class="run-part-error">{{ run.error }}</div>
                  <ResultView v-else-if="run.finalResult" :text="run.finalResult" />
                  <div v-else class="run-part-empty">无结果</div>
                </template>
                <template v-else-if="activeRunTab[run.id] === 'progress'">
                  <template v-if="run.progress || run.result">
                    <div v-if="run.progress" class="run-part-block">
                      <div class="run-part-label">progress</div>
                      <pre class="run-part-progress pane-scroll">{{ run.progress }}</pre>
                    </div>
                    <div v-if="run.result" class="run-part-block">
                      <div class="run-part-label">result</div>
                      <ResultView :text="run.result" />
                    </div>
                  </template>
                  <div v-else class="run-part-empty">无信息</div>
                </template>
                <template v-else-if="activeRunTab[run.id] === 'exec'">
                  <div v-if="run.logs.length">
                    <div v-for="(log, i) in run.logs" :key="i" class="output-line" :class="log.type">{{ log.message }}</div>
                  </div>
                  <div v-else class="run-part-empty">无日志</div>
                </template>
                <template v-else>
                  <pre class="run-part-input">{{ JSON.stringify(run.input, null, 2) }}</pre>
                </template>
              </div>
            </template>
          </div>
        </div>
        <div v-else class="output-empty">暂无运行记录，点「运行」开始</div>
      </div>
    </div>
    <!-- 上下分区的拖动条：面板是 flex column，改 tail 的固定高度就等于改两段的比例。
         双击复位（回到「内容自适应 + 38% 上限」的默认布局）。 -->
    <div
      class="panel-splitter"
      title="拖动调整上下高度，双击复位"
      @pointerdown="startTailResize"
      @dblclick="resetTailHeight"
    ></div>
    <div ref="tailRef" class="panel-tail pane-scroll" :style="tailStyle">
      <div class="tail-header">
        <span class="tail-title">输入参数</span>
        <div ref="toolbarRef" class="debug-toolbar"
             :class="{ 'debug-toolbar--floating': toolbarPos }"
             :style="toolbarPos ? { left: toolbarPos.x + 'px', top: toolbarPos.y + 'px' } : null">
          <span class="tb-handle" title="按住拖动，双击复位"
                @pointerdown="onToolbarDragStart" @dblclick="resetToolbarPos">
            <i v-for="n in 6" :key="n" class="tb-dot"></i>
          </span>
          <!-- 运行 / 暂停合成一个按钮：两者互斥（运行中只能暂停，其余只能运行 / 继续），
               拆成两个位置等于常驻一个灰按钮。图标 / 配色 / 提示跟着当前可用动作走 -->
          <el-tooltip :content="runBtn.tip" placement="top">
            <button class="dbg-btn" :class="runBtn.mode" @click="runBtn.onClick">
              <VideoPause v-if="runBtn.mode === 'pause'" />
              <VideoPlay v-else />
            </button>
          </el-tooltip>
          <!-- 步过 / 步入 / 步出：箭头 + 落点圆点（VS Code 调试条那套图标）。
               Element Plus 没有这三个字形，自己画：24 视口描边，样式见 .dbg-ico -->
          <el-tooltip content="步过" placement="top">
            <button class="dbg-btn" :disabled="dbgDisabled.step" @click="onStepOver">
              <svg class="dbg-ico" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M4.6 14.6C4.6 2.8 17.2 2.8 17.2 13.6" />
                <path d="M14.7 11.1 17.2 13.6 19.7 11.1" />
                <circle class="dot" cx="12" cy="18" r="1.9" />
              </svg>
            </button>
          </el-tooltip>
          <el-tooltip content="步入" placement="top">
            <button class="dbg-btn" :disabled="dbgDisabled.step" @click="onStepIn">
              <svg class="dbg-ico" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 4.2V14.4" />
                <path d="M8.6 11 12 14.4 15.4 11" />
                <circle class="dot" cx="12" cy="18" r="1.9" />
              </svg>
            </button>
          </el-tooltip>
          <el-tooltip content="步出" placement="top">
            <button class="dbg-btn" :disabled="dbgDisabled.step" @click="onStepOut">
              <svg class="dbg-ico" viewBox="0 0 24 24" aria-hidden="true">
                <path d="M12 14.4V4.2" />
                <path d="M8.6 7.6 12 4.2 15.4 7.6" />
                <circle class="dot" cx="12" cy="18" r="1.9" />
              </svg>
            </button>
          </el-tooltip>
          <el-tooltip content="停止" placement="top">
            <button class="dbg-btn stop" :disabled="dbgDisabled.stop" @click="onStop"><Close /></button>
          </el-tooltip>
          <el-tooltip content="查看变量" placement="top">
            <button class="dbg-btn" :disabled="dbgDisabled.inspect" @click="onInspect"><Search /></button>
          </el-tooltip>
        </div>
      </div>
      <!-- 运行状态 + 子流程版本合成一条 meta 栏：两条灰条叠在一起太碎，输入区也被挤下去。
           子流程这一项只管它调起的子流程，主流程永远是画布上这份草稿 -->
      <div v-if="flowDebug" class="debug-meta">
        <span class="dm-dot" :class="{ 'dm-dot--on': flowDebug.connected.value }"></span>
        <span class="dm-text" :title="`模式 ${flowDebug.mode.value} · 当前 ${currentDisplay}`">
          {{ flowDebug.mode.value }} · {{ currentDisplay }}
        </span>
        <span class="dm-scope-label">子流程</span>
        <el-tooltip :content="scopeTip" placement="top">
          <el-radio-group v-model="scopeMode" size="small" class="scope-radio">
            <el-radio-button value="draft">草稿</el-radio-button>
            <el-radio-button value="published">正式</el-radio-button>
          </el-radio-group>
        </el-tooltip>
      </div>
      <div class="tail-content pane-scroll">
        <div v-if="requiredFields.length === 0" class="empty-fields">
          <p>这个工作流没有声明入参</p>
          <span>直接点「运行」就能跑</span>
        </div>
        <div v-else class="fields-container">
          <div 
            v-for="field in requiredFields" 
            :key="field.key" 
            class="field-item"
          >
            <div class="field-head">
              <span class="field-icon" :class="getIconClass(field)">
                <span v-if="field.type === 'array' && field.items?.type === 'number'" class="icon-text">N</span>
                <span v-else-if="field.type === 'array' && field.items?.type === 'string'" class="icon-text">S</span>
                <span v-else-if="field.type === 'array' && field.items?.type === 'boolean'" class="icon-text">B</span>
                <span v-else-if="field.type === 'array' && field.items?.type === 'object'" class="icon-text">O</span>
                <span v-else-if="field.type === 'number'" class="icon-text">N</span>
                <span v-else-if="field.type === 'string'" class="icon-text">S</span>
                <span v-else-if="field.type === 'boolean'" class="icon-text">B</span>
                <span v-else class="icon-text">O</span>
              </span>
              <span class="field-label">{{ field.label }}<span class="required-mark">*</span></span>
              <span class="field-type">{{ fieldTypeText(field) }}</span>
            </div>
            <div v-if="field.description" class="field-desc">{{ field.description }}</div>
            <div class="field-control">
              <div v-if="field.type === 'boolean'" class="boolean-control">
                <button 
                  class="bool-btn" 
                  :class="{ active: fieldValues[field.key] === true }"
                  @click="fieldValues[field.key] = true"
                >
                  是
                </button>
                <button 
                  class="bool-btn" 
                  :class="{ active: fieldValues[field.key] === false }"
                  @click="fieldValues[field.key] = false"
                >
                  否
                </button>
              </div>
              <textarea 
                v-else-if="field.type === 'array'"
                class="text-input json-textarea"
                v-model="fieldValues[field.key]"
                :placeholder="`请输入${field.label}的JSON数组`"
              ></textarea>
              <textarea 
                v-else-if="field.type === 'string'"
                class="text-input string-textarea"
                v-model="fieldValues[field.key]"
                :placeholder="`请输入${field.label}`"
              ></textarea>
              <input 
                v-else-if="field.type === 'number'"
                type="number"
                class="text-input number-input"
                v-model="fieldValues[field.key]"
                :placeholder="`请输入${field.label}`"
              />
              <textarea 
                v-else
                class="text-input json-textarea"
                v-model="fieldValues[field.key]"
                :placeholder="`请输入${field.label}的JSON值`"
              ></textarea>
            </div>
          </div>
        </div>
      </div>
    </div>
    </template>

    <div v-else class="history-view">
      <div v-show="!runDetail" class="history-filter">
        <select v-model="historyFilter.status" class="hf-status">
          <option value="">全部状态</option>
          <option value="success">成功</option>
          <option value="failed">失败</option>
          <option value="cancelled">已取消</option>
        </select>
        <select :value="historyRange" class="hf-status" @change="applyHistoryRange($event.target.value)">
          <option value="all">全部时间</option>
          <option value="today">今天</option>
          <option value="7d">近 7 天</option>
          <option value="30d">近 30 天</option>
          <option value="custom">自定义</option>
        </select>
        <div class="hf-links">
          <a v-if="hasHistoryFilter" class="hf-reset" @click="resetHistoryFilter">重置</a>
          <a v-if="historyRuns.length" class="hf-clear" title="清空该工作流的全部历史运行记录" @click="clearAllRuns">清空</a>
        </div>
        <template v-if="historyRange === 'custom'">
          <input type="datetime-local" v-model="historyFilter.startFrom" class="hf-time" />
          <span class="hf-sep">至</span>
          <input type="datetime-local" v-model="historyFilter.startTo" class="hf-time" />
        </template>
      </div>
      <div v-show="!runDetail" class="history-list pane-scroll" @scroll="onHistoryScroll">
        <div v-if="!historyRuns.length && !historyLoading" class="history-empty">
          {{ hasHistoryFilter ? '没有符合条件的记录' : '暂无历史运行记录' }}
        </div>
        <div v-for="run in historyRuns" :key="run.id" class="history-item"
             :class="{ active: run.id === selectedRunId }" @click="loadRunDetail(run.id)">
          <span class="hi-time">{{ formatTime(run.started_at) }}</span>
          <span class="hi-meta">
            <span v-if="run.run_type === 'debug'" class="hi-tag">调试</span>
            <span v-else-if="run.run_type === 'task'" class="hi-tag hi-tag-task">任务</span>
            <span class="hi-nodes">{{ run.node_count }} 节点</span>
          </span>
          <span class="hi-cost">{{ run.duration_ms }}ms</span>
          <span class="hi-status" :class="statusClass(run.status)">{{ statusText(run.status) }}</span>
          <span class="hi-del" title="删除这条记录" @click.stop="deleteRun(run.id)">
            <el-icon><Delete /></el-icon>
          </span>
        </div>
        <div v-if="historyLoading" class="history-more">加载中...</div>
        <div v-else-if="historyFinished && historyRuns.length" class="history-more">没有更多了</div>
      </div>
      <div v-show="runDetail" class="history-detail pane-scroll">
        <div v-if="runDetail">
          <div class="hd-back">
            <a class="hd-back-btn" @click="closeRunDetail">← 返回列表</a>
            <span class="hd-back-info">{{ formatTime(runDetail.started_at) }} · {{ statusText(runDetail.status) }}</span>
          </div>
          <div class="hd-meta">
            <div class="hd-meta-row">
              <span>状态: {{ statusText(runDetail.status) }}</span>
              <span>耗时: {{ runDetail.duration_ms }}ms</span>
              <span>节点数: {{ runDetail.node_count }}</span>
            </div>
            <div class="hd-meta-row">
              <span>开始: {{ formatTime(runDetail.started_at) }}</span>
              <span>结束: {{ formatTime(runDetail.finished_at) }}</span>
            </div>
          </div>
          <div v-if="runDetail.err_msg" class="hd-err">{{ runDetail.err_msg }}</div>
          <div class="hd-section">
            <div class="hd-title">
              <span class="hd-title-text">结果</span>
              <a class="hd-toggle" @click="toggleHdSection('result')">{{ hdCollapsed.result ? '展开' : '收起' }}</a>
            </div>
            <ResultView v-show="!hdCollapsed.result" :text="runDetail.result || '（无）'" variant="detail" />
          </div>
          <div v-if="progressText" class="hd-section">
            <div class="hd-title">
              <span class="hd-title-text">过程信息-progress</span>
              <a class="hd-toggle" @click="toggleHdSection('progress')">{{ hdCollapsed.progress ? '展开' : '收起' }}</a>
            </div>
            <pre v-show="!hdCollapsed.progress">{{ progressText }}</pre>
          </div>
          <div v-if="resultStreamText" class="hd-section">
            <div class="hd-title">
              <span class="hd-title-text">过程信息-result</span>
              <a class="hd-toggle" @click="toggleHdSection('resultStream')">{{ hdCollapsed.resultStream ? '展开' : '收起' }}</a>
            </div>
            <ResultView v-show="!hdCollapsed.resultStream" :text="resultStreamText" variant="detail" />
          </div>
          <!-- 输入放在最后：与运行卡片的 tab 顺序（结果 / 过程 / 执行 / 输入）一致。
               逐项列出，与节点「执行结果」里的输入 / 输出变量同一套展示（VarList） -->
          <div v-if="runArgRows.length" class="hd-section">
            <div class="hd-title">
              <span class="hd-title-text">输入</span>
              <a class="hd-toggle" @click="toggleHdSection('input')">{{ hdCollapsed.input ? '展开' : '收起' }}</a>
            </div>
            <VarList v-show="!hdCollapsed.input" :rows="runArgRows" />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, inject, reactive, nextTick, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { VideoPlay, VideoPause, Close, Search, Delete } from '@element-plus/icons-vue'
import { getAuthHeader } from '@/tenant/utils/auth.js'
import { apiBase } from '@/tenant/config/base.js'
import { fillRunInfo, setNodeResult, clearNodeResults } from '@/tenant/composables/runData.js'
import ResultView from './ResultView.vue'
import VarList from './VarList.vue'
import { useEscClose } from '../../../composables/useEscClose'

const props = defineProps({
  panelWidth: {
    type: Number,
    default: 360
  },
  flowId: {
    type: [String, Number],
    default: ''
  },
  flowArguments: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits(['close', 'resize', 'run'])

// 父级 v-if 挂载：挂上即开着
useEscClose(true, () => emit('close'))

// ── 历史运行（含试运行试跑与任务调用）──
const activeTab = ref('debug')
const historyRuns = ref([])
const selectedRunId = ref(null)
const runDetail = ref(null)
const historyFilter = reactive({ status: '', startFrom: '', startTo: '' })
// 时间范围快捷选择：all（缺省，不限）/ today / 7d / 30d / custom（手填起止）
const historyRange = ref('all')
const historyTotal = ref(0)
const historyOffset = ref(0)
const historyLoading = ref(false)
const historyFinished = ref(false)
const HISTORY_PAGE_SIZE = 20
// 加载中又改筛选条件时的待重查标记（见 loadHistoryRuns）
let historyPendingReset = false

function statusText(s) { return { success: '成功', failed: '失败', cancelled: '取消' }[s] || s || '-' }
function statusClass(s) { return { success: 'status-success', failed: 'status-failed', cancelled: 'status-cancelled' }[s] || '' }
function formatTime(t) { if (!t) return '-'; try { return new Date(t).toLocaleString() } catch (e) { return t } }

// 是否设过筛选条件：空列表文案要能区分"本来就没有"和"被筛掉了"
const hasHistoryFilter = computed(() => !!(historyFilter.status || historyFilter.startFrom || historyFilter.startTo))

// datetime-local 需要 "YYYY-MM-DDTHH:mm"（本地时区，不带秒与 Z）
function toLocalInput(d) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

// 快捷时间范围：只设下界、不设上界 —— 上界留空表示"一直到最新"，
// 这样选完范围之后新产生的记录也能看到，不用隔一会儿再重新选一次。
function applyHistoryRange(mode) {
  historyRange.value = mode
  if (mode === 'custom') return
  if (mode === 'all') {
    historyFilter.startFrom = ''
    historyFilter.startTo = ''
    return
  }
  const days = mode === 'today' ? 0 : mode === '7d' ? 6 : 29
  const from = new Date()
  from.setDate(from.getDate() - days)
  from.setHours(0, 0, 0, 0)
  historyFilter.startFrom = toLocalInput(from)
  historyFilter.startTo = ''
}

function resetHistoryFilter() {
  historyFilter.status = ''
  applyHistoryRange('all')
}

// 历史详情里各段的折叠状态：结果动辄几十上百行，允许逐段收起。
// 跨记录保持状态（用户收起过程信息后，切下一条也不用再收一次）。
const hdCollapsed = reactive({ result: false, progress: false, resultStream: false, input: false })
function toggleHdSection(key) {
  hdCollapsed[key] = !hdCollapsed[key]
}


// 值的展示形态：字符串原样（避免多一层引号与转义），对象/数组缩进 JSON（与节点执行结果同一口径）
const formatVar = (v) => {
  if (v === null) return 'null'
  if (v === undefined) return '（未赋值）'
  if (typeof v === 'string') return v
  return JSON.stringify(v, null, 2)
}

// 输入：建运行记录时落库的那份入参（run_args），与试运行卡片里的「输入」同源。
// 后端给的是 json.RawMessage（解出来就是对象）；个别历史数据若是字符串化的 JSON 再解一层，
// 解不开就当成一个整体值展示，别让一段坏数据把整块详情吞掉。
const runArgRows = computed(() => {
  const v = runDetail.value?.run_args
  if (v === null || v === undefined || v === '') return []
  let args = v
  if (typeof args === 'string') {
    try { args = JSON.parse(args) } catch { return [{ name: 'input', text: args }] }
  }
  if (args && typeof args === 'object') {
    return Object.keys(args).map((name) => ({ name, text: formatVar(args[name]) }))
  }
  return [{ name: 'input', text: formatVar(args) }]
})

// 过程信息：progress / result 消息数组拼接成连续文本
const progressText = computed(() => {
  const arr = runDetail.value?.progress?.progress
  return Array.isArray(arr) && arr.length ? arr.join('') : ''
})
const resultStreamText = computed(() => {
  const arr = runDetail.value?.progress?.result
  return Array.isArray(arr) && arr.length ? arr.join('') : ''
})

// 清空当前历史详情：筛选后选中记录已不在列表里时必须做，
// 否则会出现"列表说没有记录、右边还挂着上一条"的错位感。
function clearRunSelection() {
  selectedRunId.value = null
  runDetail.value = null
  fillRunInfo(null)
  clearNodeResults()
  if (flowDebug && flowDebug.applyHistoryNodeStates) {
    flowDebug.applyHistoryNodeStates([])
  }
}

// 详情覆盖列表：返回列表即清空选中（列表用 v-show 保留，滚动位置不会丢）
function closeRunDetail() {
  clearRunSelection()
}

async function loadHistoryRuns(reset = false) {
  if (!props.flowId) return
  if (historyLoading.value) {
    // 加载过程中又改了筛选条件：记下待重查，等这一轮结束再按新条件查一遍，
    // 否则旧结果会把新条件覆盖掉，看起来就是"筛选没生效"。
    if (reset) historyPendingReset = true
    return
  }
  if (reset) {
    historyOffset.value = 0
    historyRuns.value = []
    historyFinished.value = false
  }
  if (historyFinished.value) return
  historyLoading.value = true
  try {
    const params = new URLSearchParams()
    params.set('limit', String(HISTORY_PAGE_SIZE))
    params.set('offset', String(historyOffset.value))
    if (historyFilter.status) params.set('status', historyFilter.status)
    if (historyFilter.startFrom) params.set('start_from', new Date(historyFilter.startFrom).toISOString())
    if (historyFilter.startTo) params.set('start_to', new Date(historyFilter.startTo).toISOString())
    const resp = await fetch(`${apiBase}/admin/flows/${props.flowId}/runs?${params.toString()}`, { headers: { ...getAuthHeader() } })
    const data = await resp.json()
    if (data.code === 200) {
      const items = data.data?.items || []
      const total = data.data?.total || 0
      if (reset) {
        historyRuns.value = items
      } else {
        historyRuns.value = historyRuns.value.concat(items)
      }
      historyTotal.value = total
      historyOffset.value += items.length
      historyFinished.value = historyRuns.value.length >= total
      // 改了筛选条件（reset）后，原来选中的那条若已不在结果里，详情必须一起清掉，
      // 不然列表显示"没有记录"、右侧却还开着上一次的历史执行。
      if (reset && selectedRunId.value
        && !historyRuns.value.some((r) => String(r.id) === String(selectedRunId.value))) {
        clearRunSelection()
      }
    }
  } finally {
    historyLoading.value = false
    if (historyPendingReset) {
      historyPendingReset = false
      loadHistoryRuns(true)
    }
  }
}

// 滚动到底部自动加载下一页
function onHistoryScroll(e) {
  const el = e.target
  if (el.scrollTop + el.clientHeight >= el.scrollHeight - 20) {
    loadHistoryRuns(false)
  }
}

async function loadRunDetail(runId) {
  if (!props.flowId || !runId) return
  selectedRunId.value = runId
  const resp = await fetch(`${apiBase}/admin/flows/${props.flowId}/runs/${runId}`, { headers: { ...getAuthHeader() } })
  const data = await resp.json()
  if (data.code === 200) {
    runDetail.value = data.data?.run || null
    // 跟调试一致：回填运行数据存储（点击任何历史记录都先清空节点数据，避免残留）
    fillRunInfo(runDetail.value)
    clearNodeResults()
    ;(data.data?.nodes || []).forEach(setNodeResult)
    // 同步画布节点运行状态（清空 + 按历史记录重设）
    if (flowDebug && flowDebug.applyHistoryNodeStates) {
      flowDebug.applyHistoryNodeStates(data.data?.nodes || [])
    }
  }
}

// 删除单条：先在本地剔除，再回拉一页补齐，避免用户停在列表深处被重置到第一页
async function deleteRun(runId) {
  if (!props.flowId || !runId) return
  try {
    await ElMessageBox.confirm('删除这条历史运行记录？删除后不可恢复。', '提示', {
      type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消'
    })
  } catch { return }

  try {
    const resp = await fetch(`${apiBase}/admin/flows/${props.flowId}/runs/${runId}`, {
      method: 'DELETE',
      headers: { ...getAuthHeader() }
    })
    const data = await resp.json()
    if (data.code !== 200) throw new Error(data.msg || '删除失败')

    // 删的正好是详情里那条：先清选中，否则「列表里没了、右边还挂着」
    if (String(selectedRunId.value) === String(runId)) clearRunSelection()
    historyRuns.value = historyRuns.value.filter((r) => String(r.id) !== String(runId))
    historyTotal.value = Math.max(0, historyTotal.value - 1)
    historyOffset.value = Math.max(0, historyOffset.value - 1)
    ElMessage.success('已删除')

    // 不满一页且后面还有：补一页，保持滚动位置不跳
    if (!historyFinished.value) loadHistoryRuns(false)
  } catch (e) {
    ElMessage.error(e?.message || '删除失败')
  }
}

// 清空：删该工作流的全部记录（调试 + 正式，不受当前筛选影响，文案里说清楚）
async function clearAllRuns() {
  if (!props.flowId) return
  try {
    await ElMessageBox.confirm(
      '清空该工作流的全部历史运行记录（含调试与正式运行，不受当前筛选影响）？删除后不可恢复。',
      '提示',
      { type: 'warning', confirmButtonText: '清空', cancelButtonText: '取消' }
    )
  } catch { return }

  try {
    const resp = await fetch(`${apiBase}/admin/flows/${props.flowId}/runs`, {
      method: 'DELETE',
      headers: { ...getAuthHeader() }
    })
    const data = await resp.json()
    if (data.code !== 200) throw new Error(data.msg || '清空失败')

    clearRunSelection()
    historyRuns.value = []
    historyTotal.value = 0
    historyOffset.value = 0
    // 必须复位，否则下次切回该 tab 会因「已加载完」直接 return，看到空列表却是旧状态
    historyFinished.value = false
    ElMessage.success('已清空')
  } catch (e) {
    ElMessage.error(e?.message || '清空失败')
  }
}

watch(activeTab, (tab) => { if (tab === 'history') loadHistoryRuns(true) })
watch(historyFilter, () => loadHistoryRuns(true))

// 调试通道（由 Flow.vue provide，可能为 null）
const flowDebug = inject('flowDebug', null)
const FlowNodes = inject('FlowNodes', ref([]))

// 当前挂起节点的名字（根据 highlightNodeId 在节点列表里查 label）
const currentNodeLabel = computed(() => {
  const id = flowDebug?.highlightNodeId?.value
  if (!id) return ''
  const node = FlowNodes.value.find((n) => String(n.id) === String(id))
  return node?.data?.label || ''
})
const currentDisplay = computed(() => {
  const id = flowDebug?.highlightNodeId?.value
  if (!id) return '-'
  return currentNodeLabel.value ? `${currentNodeLabel.value}（${id}）` : id
})

// 子流程版本：读写调试通道的 followDrafts（true = 跟随草稿），与后端 DebugScope 同一语义。
// 「正式版本」只影响被调子流程——主流程始终是画布上的这份草稿。
const scopeMode = computed({
  get: () => (flowDebug?.followDrafts?.value === false ? 'published' : 'draft'),
  set: (v) => {
    if (flowDebug?.followDrafts) flowDebug.followDrafts.value = v === 'draft'
  }
})
const scopeTip = computed(() => scopeMode.value === 'draft'
  ? '本次运行调起的子流程读它们自己的草稿（含未发布的修改）。切到「正式版本」可以验证线上那一套。'
  : '本次运行调起的子流程按已发布的正式版本运行（没发布过的子流程会直接报错）。主流程仍是画布草稿。'
)

// ===== 上下分区可拖（运行信息 / 输入参数）=====
// tailHeight = 0 表示默认布局（输入参数区内容自适应，最多 38%）；
// 一旦拖动就写死像素高度。上限按「拖动开始时的两段总高」算 —— 拖动过程中上半区
// 会跟着收缩，实时量总高会让上限越拖越小。
const MIN_TAIL = 96   // 输入参数区最小高度：标题栏 + meta 栏 + 一行内容
const MIN_BODY = 120  // 运行信息区最小高度

// 拖动结果按像素记在本地：下次打开面板直接沿用上次的高度
const TAIL_HEIGHT_KEY = 'app_flame_run_panel_tail_height'

const readSavedTailHeight = () => {
  try {
    const n = Number(localStorage.getItem(TAIL_HEIGHT_KEY))
    // 没存过 / 存坏了 / 比最小高度还小，都当作「没拖过」
    return Number.isFinite(n) && n >= MIN_TAIL ? Math.round(n) : 0
  } catch {
    return 0
  }
}

const saveTailHeight = () => {
  try {
    if (tailHeight.value) localStorage.setItem(TAIL_HEIGHT_KEY, String(Math.round(tailHeight.value)))
    else localStorage.removeItem(TAIL_HEIGHT_KEY)
  } catch {
    /* 隐私模式等场景下忽略 */
  }
}

const bodyRef = ref(null)
const tailRef = ref(null)
const tailHeight = ref(readSavedTailHeight())
let tailDrag = null

const tailStyle = computed(() => {
  if (!tailHeight.value) return null
  return { height: tailHeight.value + 'px', maxHeight: tailHeight.value + 'px', flex: '0 0 auto' }
})

const startTailResize = (e) => {
  const tail = tailRef.value
  const body = bodyRef.value
  if (!tail || !body || e.button !== 0) return
  const tRect = tail.getBoundingClientRect()
  const bRect = body.getBoundingClientRect()
  tailDrag = {
    startY: e.clientY,
    startHeight: tRect.height,
    maxHeight: Math.max(MIN_TAIL, tRect.height + bRect.height - MIN_BODY),
    minHeight: Math.min(MIN_TAIL, tRect.height),
  }
  // 这里先不写死高度：只点一下不拖，布局该保持原来的自适应；真正移动了才落成像素值
  // 拖动时鼠标会掠过别的元素，光标与选区都要在 body 上兜住
  document.body.style.cursor = 'row-resize'
  document.body.style.userSelect = 'none'
  window.addEventListener('pointermove', onTailResizeMove)
  window.addEventListener('pointerup', onTailResizeEnd)
  e.preventDefault()
}

const onTailResizeMove = (e) => {
  if (!tailDrag) return
  // 往上拖（clientY 变小）= 输入参数区变高
  const next = tailDrag.startHeight + (tailDrag.startY - e.clientY)
  tailHeight.value = Math.min(Math.max(tailDrag.minHeight, next), tailDrag.maxHeight)
}

const onTailResizeEnd = () => {
  tailDrag = null
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
  window.removeEventListener('pointermove', onTailResizeMove)
  window.removeEventListener('pointerup', onTailResizeEnd)
  // 只在松手时落盘：拖动过程中每帧都写 localStorage 太吵
  saveTailHeight()
}

// 双击复位：回到默认布局，免得拖到极值后找不回来
const resetTailHeight = () => {
  tailHeight.value = 0
  saveTailHeight()  // 一并清掉记住的高度，否则下次进来又变回固定高度
}

// 面板整体变矮（或换到更矮的窗口）时把写死的高度收回边界内，否则上半区会被挤成 0。
// 上限按面板自身的高度算：面板是绝对定位 + top/bottom 定死的，不受子元素影响；
// 若改用「body 当前高度 + tail 高度」，等 body 已经被挤成 0 时就再也算不回来了。
const clampTailHeight = () => {
  const panel = panelRef.value
  if (!tailHeight.value || !panel) return
  const panelH = panel.getBoundingClientRect().height
  const headerH = panel.querySelector('.panel-header')?.getBoundingClientRect().height || 0
  const max = Math.max(MIN_TAIL, panelH - headerH - 6 - MIN_BODY)
  if (tailHeight.value > max) tailHeight.value = max
}

// ===== 调试工具条可拖 =====
// null = 待在自己位置（tail-header 右侧，参与文档流）；有坐标 = 浮在面板上、按住把手拖到哪算哪
const panelRef = ref(null)
const toolbarRef = ref(null)
const toolbarPos = ref(null)
let tbDrag = null

const onToolbarDragStart = (e) => {
  const panel = panelRef.value
  const tb = toolbarRef.value
  if (!panel || !tb || e.button !== 0) return
  const pRect = panel.getBoundingClientRect()
  const tRect = tb.getBoundingClientRect()
  // 记下按点相对工具条的偏移：首次拖动以当前真实位置起算，切 absolute 时不会跳一下
  tbDrag = { dx: e.clientX - tRect.left, dy: e.clientY - tRect.top, w: tRect.width, h: tRect.height }
  toolbarPos.value = { x: tRect.left - pRect.left, y: tRect.top - pRect.top }
  window.addEventListener('pointermove', onToolbarDragMove)
  window.addEventListener('pointerup', onToolbarDragEnd)
  e.preventDefault()
}

const onToolbarDragMove = (e) => {
  const panel = panelRef.value
  if (!panel || !tbDrag) return
  const r = panel.getBoundingClientRect()
  // 夹在面板内：拖出边界就找不回来了
  const x = Math.min(Math.max(0, e.clientX - r.left - tbDrag.dx), Math.max(0, r.width - tbDrag.w))
  const y = Math.min(Math.max(0, e.clientY - r.top - tbDrag.dy), Math.max(0, r.height - tbDrag.h))
  toolbarPos.value = { x, y }
}

const onToolbarDragEnd = () => {
  tbDrag = null
  window.removeEventListener('pointermove', onToolbarDragMove)
  window.removeEventListener('pointerup', onToolbarDragEnd)
}

// 双击把手复位：免得拖到角落后再也放不回去
const resetToolbarPos = () => {
  toolbarPos.value = null
}

onMounted(() => {
  window.addEventListener('resize', clampTailHeight)
  // 记下的高度是按上次的窗口算的，这次窗口可能更矮：等布局量出来再收回边界
  nextTick(clampTailHeight)
})

onUnmounted(() => {
  window.removeEventListener('resize', clampTailHeight)
  window.removeEventListener('pointermove', onToolbarDragMove)
  window.removeEventListener('pointerup', onToolbarDragEnd)
  // 拖到一半被卸载时，别把 body 的光标 / 选区锁留着
  window.removeEventListener('pointermove', onTailResizeMove)
  window.removeEventListener('pointerup', onTailResizeEnd)
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
})

const fieldValues = ref({})

const requiredFields = computed(() => {
  const args = props.flowArguments
  if (!args || !args.properties) {
    return []
  }
  
  const required = args.required || []
  const fields = []
  
  for (const key of required) {
    const prop = args.properties[key]
    if (prop) {
      fields.push({
        key,
        label: key,
        type: prop.type || 'string',
        items: prop.items || null,
        description: prop.description || ''
      })
    }
  }
  
  return fields
})

watch(requiredFields, (newFields) => {
  const newValues = {}
  newFields.forEach(field => {
    if (fieldValues.value[field.key] === undefined) {
      newValues[field.key] = field.type === 'boolean' ? false : ''
    } else {
      newValues[field.key] = fieldValues.value[field.key]
    }
  })
  fieldValues.value = newValues
}, { immediate: true })

// 字段类型标签：数组带上元素类型，比图标里那个字母信息量大
const fieldTypeText = (f) => {
  if (f.type === 'array') return `array<${f.items?.type || 'any'}>`
  return f.type || 'string'
}

const getIconClass = (field) => {
  if (field.type === 'array') {
    if (field.items?.type === 'number') return 'field-icon-number-array'
    if (field.items?.type === 'string') return 'field-icon-string-array'
    if (field.items?.type === 'boolean') return 'field-icon-boolean-array'
    if (field.items?.type === 'object') return 'field-icon-object-array'
  }
  if (field.type === 'number') return 'field-icon-number'
  if (field.type === 'string') return 'field-icon-string'
  if (field.type === 'boolean') return 'field-icon-boolean'
  return 'field-icon-object'
}

const handleRunFlow = () => {
  emit('run', fieldValues.value)
  // 同时驱动 WS 调试通道运行（若已接调试通道）
  if (flowDebug) flowDebug.run(buildInput())
}

// 运行前自动保存画布的能力（由 Flow.vue provide）
const saveDiagram = inject('saveDiagram', null)
const isDirty = inject('isDirty', ref(false))
// 新建工作流还没落库时，点运行先把它创建出来（由 Flows.vue provide）
const ensureFlowSaved = inject('ensureFlowSaved', null)

// 调试控制：优先走 WS 通道，未提供时回退到原 emit('run')
const onRun = async () => {
  // 调试通道是按 flowId 建连的（/debug/flow/:flowId），所以必须先把 id 拿到。
  // 新建未保存时先落库，而不是直接报错 —— 用户的意图就是"把它跑起来"。
  let flowId = props.flowId
  if (!flowId) {
    if (!ensureFlowSaved) {
      ElMessage.error('请先保存工作流，再运行')
      return
    }
    // 失败时 handleSave 自己已经提示了原因（如「请输入工作流名称」），
    // 这里不再叠一条，避免两个弹窗糊在一起
    flowId = await ensureFlowSaved()
    if (!flowId) return
    // 等一轮渲染：id 回写后 Flow.vue 的 watch 会 setFlowId/loadBreakpoints/resetRunData，
    // 让它在 run 之前跑完，避免刚启动的那一轮本地轨迹被清掉
    await nextTick()
  }
  // 「继续」（断点挂起后）不保存；全新「运行」且存在未保存修改时先落库
  if (!isResume.value && isDirty.value && saveDiagram) {
    const ok = await saveDiagram()
    // 有未保存修改且保存失败，则阻断调试运行
    if (!ok) return
  }
  // 必须用局部 flowId：刚创建的那一拍 props.flowId 还没更新（prop 要等下一次渲染），
  // 依赖它会把调试通道连到空 id 上。
  flowDebug ? flowDebug.run(buildInput(), flowId) : handleRunFlow()
}
const onPause = () => flowDebug?.pause(props.flowId)
const onStepOver = () => flowDebug?.step('over', props.flowId)
const onStepIn = () => flowDebug?.step('in', props.flowId)
const onStepOut = () => flowDebug?.step('out', props.flowId)
const onStop = () => flowDebug?.stop()
const openFlowVarsResult = inject('openFlowVarsResult', null)
const onInspect = () => {
  if (openFlowVarsResult) {
    openFlowVarsResult()
  } else {
    flowDebug?.inspect()
  }
}

// 调试按钮可用性状态机（基于 flowDebug 的运行/暂停状态）：
// 1) 调试未开始（!running）：运行可点，其余禁用
// 2) 运行中且未在断点停下（running && mode!=='paused'）：暂停 / 停止 / 查看变量可点
// 3) 停在断点（mode==='paused'）：继续 / 步过 / 步入 / 步出 / 停止 可点
const isPaused = computed(() => !!(flowDebug && flowDebug.mode.value === 'pause'))
const isRunning = computed(() => !!(flowDebug && flowDebug.running.value))
// 「继续」的准确条件是：运行还在（running）且停在断点。只看 mode 会被通道断开后残留的
// 'pause' 骗到，把一次全新运行误判成继续，从而跳过运行前的自动保存。
const isResume = computed(() => isRunning.value && isPaused.value)

// 运行 / 暂停合并成一个按钮：两者互斥，任意时刻只有一个动作可用。
// 图标同时承担状态指示：▶ 表示可以跑（运行 / 继续），⏸ 表示正在跑、可以停一下。
const runBtn = computed(() => (isRunning.value && !isPaused.value
  ? { mode: 'pause', tip: '暂停', onClick: onPause }
  : { mode: 'run', tip: isResume.value ? '继续' : '运行', onClick: onRun }))

const dbgDisabled = computed(() => ({
  step: !isPaused.value,      // 仅停断点时可单步
  stop: !isRunning.value,     // 仅运行中可停止
  inspect: !isRunning.value   // 仅运行中可查看变量
}))

// 运行卡片列表（显示在试运行窗口上半部分「执行」区）
const debugRuns = computed(() => {
  const pd = flowDebug
  if (pd && pd.runs && pd.runs.value) return pd.runs.value
  return []
})

// 每个卡片的折叠状态（key 形如 run.id + '-input'/'-exec'/'-progress'/'-result'）
const collapsedMap = reactive({})
function toggleCard(key) {
  collapsedMap[key] = !collapsedMap[key]
}

// 一条运行记录占用的折叠 key：运行中是 5 张卡片，结束后是整组（-group）
const RUNNING_CARD_SUFFIXES = ['-input', '-exec', '-progress', '-result', '-final']
function runCardKeys(run) {
  return run.status === 'running'
    ? RUNNING_CARD_SUFFIXES.map((s) => run.id + s)
    : [run.id + '-group']
}

// 是否已经全部收起：决定「全部收起 / 全部展开」按钮的方向
const allCollapsed = computed(() => {
  const runs = debugRuns.value
  if (!runs.length) return false
  return runs.every((run) => runCardKeys(run).every((k) => !!collapsedMap[k]))
})

function toggleAllCards() {
  // 还有展开的就全部收起，反之全部展开
  const collapse = !allCollapsed.value
  debugRuns.value.forEach((run) => {
    runCardKeys(run).forEach((k) => { collapsedMap[k] = collapse })
    // 运行中的卡片收起后若这条记录随后结束，整组（-group）也应保持收起，避免又弹开
    if (run.status === 'running') collapsedMap[run.id + '-group'] = collapse
  })
}

// 已结束运行卡片的 tab 状态（默认 final）
const activeRunTab = reactive({})
function setRunTab(runId, tab) {
  activeRunTab[runId] = tab
  // 收起状态下点 tab 表示"要看这个 tab"，顺手展开，省一次点击
  if (collapsedMap[runId + '-group']) collapsedMap[runId + '-group'] = false
}

function runStatusText(s) {
  return { finished: '成功', failed: '错误', cancelled: '取消' }[s] || s || ''
}
function runStatusClass(s) {
  return { finished: 'run-status-success', failed: 'run-status-failed', cancelled: 'run-status-cancelled' }[s] || ''
}
function formatCost(ms) {
  if (ms == null || isNaN(ms) || ms < 0) return ''
  if (ms < 1000) return ms + 'ms'
  if (ms < 60000) return (ms / 1000).toFixed(1) + 's'
  const totalSec = Math.floor(ms / 1000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m}分${s}秒`
}

// 清空所有运行卡片
function onClearRuns() {
  if (flowDebug && flowDebug.clearRuns) {
    flowDebug.clearRuns()
  }
}

// 卡片内容更新时，自动滚动到最底部
const runCardsRef = ref(null)
watch(
  () => debugRuns.value.map((r) => r.logs.length + r.progress.length + r.result.length).join(','),
  () => {
    nextTick(() => {
      if (runCardsRef.value) {
        runCardsRef.value.scrollTop = runCardsRef.value.scrollHeight
      }
    })
  }
)

// 将输入参数原样传给后端（类型还原可后续增强）
function buildInput() {
  return { ...fieldValues.value }
}

const startResize = (e) => {
  const startX = e.clientX
  const startWidth = props.panelWidth
  
  const onMouseMove = (e) => {
    const deltaX = startX - e.clientX
    const newWidth = Math.max(280, startWidth + deltaX)
    emit('resize', newWidth)
  }
  
  const onMouseUp = () => {
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }
  
  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}
</script>

<style scoped>
.run-panel {
  position: absolute;
  z-index: 100;
  right: 0;
  top: 2%;
  bottom: 2%;
  background: #ffffff;
  border: none;
  border-radius: 8px 0 0 8px;
  /* 窗口投影保留，但收成"只从左边透出去"：负 spread 把光晕收回来，
     否则 blur 会往右、往下扩散，面板外侧那圈灰在下方空白区显得格外脏 */
  box-shadow: -8px 0 16px -8px rgba(0, 0, 0, 0.22);
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
  /* 与 tail-header / debug-meta 统一 14px 水平内边距，三段内容左边缘对齐 */
  padding: 8px 14px 6px;
  color: black;
  gap: 8px;
}
.panel-tabs {
  display: flex;
  gap: 16px;
  flex: 1;
}
.panel-tab-btn {
  padding: 6px 4px;
  border: none;
  background: transparent;
  font-size: 14px;
  font-weight: 600;
  color: #666;
  cursor: pointer;
  position: relative;
}
.panel-tab-btn:hover {
  color: #1890ff;
}
.panel-tab-btn.active {
  color: #1890ff;
}
.panel-tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 2px;
  background: #1890ff;
}

.panel-title {
  flex: 1;
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

.clear-icon {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 4px 6px;
  border-radius: 4px;
  font-size: 14px;
  color: #909399;
}

.clear-icon:hover {
  background: rgba(0, 0, 0, 0.08);
  color: #f56c6c;
}

/* 全部收起 / 全部展开：文字按钮，和清空图标并排 */
.collapse-all-btn {
  flex-shrink: 0;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 12px;
  color: #409eff;
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.collapse-all-btn:hover {
  background: rgba(0, 0, 0, 0.06);
  color: #69b1ff;
}

/* 上下分区之间的拖动条：命中区 6px，只画一条 1px 细线；
   hover / 拖动时变 2px 蓝线，提示「这里可以拉」。底色保持透明，
   否则白底面板上会多出一条灰带。 */
.panel-splitter {
  position: relative;
  flex: 0 0 6px;
  height: 6px;
  cursor: row-resize;
  background: transparent;
  touch-action: none; /* 触控设备上别把拖动当成页面滚动 */
}
.panel-splitter::after {
  content: '';
  position: absolute;
  left: 10px;
  right: 10px;
  top: 50%;
  height: 1px;
  border-radius: 1px;
  background: #e4e7ed;
  transform: translateY(-50%);
  transition: background 0.15s, height 0.15s;
}
.panel-splitter:hover::after,
.panel-splitter:active::after {
  height: 2px;
  background: #409eff;
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  /* 原来贴边，卡片边框和面板边缘叠成两条线；留一圈白更透气 */
  padding: 8px 10px;
  min-height: 0;
}

/* ---- 滚动条：位置常驻、拇指透明，鼠标进入「各自区域」才上色 ----
   一旦自定义 ::-webkit-scrollbar，滚动条就变成"常驻占位"型：宽度不会因为
   内容不溢出而消失，所以 hover 显形时也不会出现内容横向跳动。
   显形按块判断：鼠标在哪一块，就只有那一块（含它内部的滚动区）亮起来。
   挂在 .run-panel:hover 上会让「运行信息」和「输入参数」的条一起冒出来。
   .panel-tail 自身不滚动（overflow: visible，滚动在 .tail-content），
   所以补一条父级 hover，进入「输入参数」区域（含标题栏）就显出内容区的条。
   与 AdminSystemView / Skills 等处的滚动条口径一致。 */
.pane-scroll {
  scrollbar-width: thin; /* Firefox：常驻细条，占位固定 */
  scrollbar-color: transparent transparent; /* 不悬浮：看不见但占位 */
}
.pane-scroll::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.pane-scroll::-webkit-scrollbar-track {
  background: transparent;
}
.pane-scroll::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.pane-scroll:hover,
.panel-tail:hover .pane-scroll {
  scrollbar-color: rgba(144, 147, 153, 0.45) transparent;
}
.pane-scroll:hover::-webkit-scrollbar-thumb,
.panel-tail:hover .pane-scroll::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.45);
}

.panel-content {
  height: 100%;
  display: flex;
  flex-direction: column;
  background-color: #fff;
  /* 不要外框：它左右两条竖线从标签栏底下一直拉到面板底，看着就是两条灰边。
     这里的层级交给里面的运行卡片（.run-card 自带边框），容器只负责布局 */
  border: none;
  border-radius: 10px;
  overflow: hidden;
}

.output-header {
  flex-shrink: 0;
  padding: 8px 12px;
  font-size: 13px;
  font-weight: 600;
  color: #333;
  background-color: #f2f4f5;
  border-bottom: 1px solid #e6e8eb;
}

.output-result {
  flex-shrink: 0;
  padding: 8px 12px;
  background-color: #f7f9fb;
  border-bottom: 1px solid #e6e8eb;
}

.output-result-body {
  margin: 0;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 12px;
  line-height: 1.5;
  color: #1f2329;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 160px;
  overflow-y: auto;
}

.output-logs {
  flex: 1;
  overflow-y: auto;
  padding: 8px 12px;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 12px;
  line-height: 1.6;
}

.output-line {
  display: flex;
  gap: 8px;
  padding: 1px 0;
  word-break: break-all;
}
/* 执行卡片内的日志：字体小一点 + 左侧缩进 */
.run-card .output-line {
  display: block;
  font-size: 12px;
  line-height: 1.6;
  padding-left: 8px;
  color: #555;
}
/* 结束 tab 卡片「执行」页的日志：字体与其它 tab 保持一致 */
.run-tab-body .output-line {
  font-size: 12px;
  line-height: 1.6;
  color: #1f2329;
}

.output-time {
  flex-shrink: 0;
  color: #9aa0a6;
}

.output-msg {
  color: #1f2329;
}

.output-line.error .output-msg {
  color: #d83931;
}

.output-line.warn .output-msg {
  color: #c47f00;
}

.output-empty {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #9aa0a6;
  font-size: 13px;
}

/* 运行卡片 */
.run-cards {
  flex: 1;
  width: 100%;
  box-sizing: border-box;
  overflow-y: auto;
  padding: 8px 2px;
}
.run-group {
  width: 100%;
  margin-bottom: 12px;
  padding-bottom: 4px;
  border-bottom: 1px dashed #e6e8eb;
}
.run-group:last-child {
  border-bottom: none;
}
.run-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0;
  border-bottom: 1px solid #e5e5e5;
  background: #fafafa;
}
/* 四个 tab（结果 / 过程 / 执行 / 输入）左右各 12px 内边距，光间距就吃掉近百像素，
   面板只有 ~360px，还要放时间、状态、收起：横向收到 8px，tab 之间不留 gap */
.run-tab {
  padding: 6px 8px;
  font-size: 12px;
  color: #666;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  user-select: none;
}
.run-tab:hover {
  color: #333;
}
.run-tab.active {
  color: #333;
  font-weight: 600;
}
.run-tab-body {
  padding: 8px;
}
.run-meta {
  display: flex;
  gap: 8px;
  font-size: 11px;
  color: #000;
  align-self: center;
  white-space: nowrap;
}
.run-time {
  color: #000;
}
.run-cost {
  color: #000;
}
.run-status {
  margin-left: auto;
  padding: 2px 10px;
  font-size: 11px;
  border-radius: 10px;
  align-self: center;
  white-space: nowrap;
}
.run-status-success {
  color: #67c23a;
  background: #f0f9eb;
}
.run-status-failed {
  color: #f56c6c;
  background: #fef0f0;
}
.run-status-cancelled {
  color: #909399;
  background: #f4f4f5;
}
.run-part-block {
  margin-bottom: 8px;
}
.run-part-block:last-child {
  margin-bottom: 0;
}
.run-part-label {
  font-size: 11px;
  font-weight: 600;
  color: #909399;
  margin-bottom: 4px;
}
.run-card {
  width: 100%;
  box-sizing: border-box;
  border: 1px solid #e6e8eb;
  border-radius: 8px;
  margin-bottom: 8px;
  padding: 8px 10px;
  background: #fafbfc;
}
.run-card-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
}
.run-card-head .run-card-title {
  margin-bottom: 0;
}
.run-card-title {
  position: relative;
  font-size: 12px;
  font-weight: 600;
  color: #333;
  padding-left: 8px;
}
/* border 只会画实线/虚线/点线，点划线得自己铺：
   2px 宽竖条，一长划（6px）+ 一点（2px），每 14px 一个周期 */
.run-card-title::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 1px;
  background-image: repeating-linear-gradient(
    to bottom,
    #409eff 0 6px,
    transparent 6px 9px,
    #409eff 9px 11px,
    transparent 11px 14px
  );
}
/* 结果仍是实线，并且比过程那几条粗一档 */
.run-card-title-final::before {
  background-image: none;
  background-color: #00704A;
  width: 2px;
}
.run-card-toggle {
  font-size: 12px;
  color: #409eff;
  cursor: pointer;
  user-select: none;
  text-decoration: none;
}
.run-card-toggle:hover {
  color: #69b1ff;
}
/* 已结束运行整组的收起按钮：贴在状态标签右侧，与运行中卡片的「收起」同一观感。
   面板只有 ~360px，四个 tab + 时间 + 状态已经很挤，这里刻意收窄外边距。 */
.run-tabs-toggle {
  align-self: center;
  margin: 0 6px 0 2px;
  font-size: 11px;
  white-space: nowrap;
}
/* result / final 由 ResultView.vue 自己渲染（原始 / Markdown 可切换），这里只管输入与进度 */
.run-part-input,
.run-part-progress {
  margin: 0;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 12px;
  line-height: 1.5;
  color: #1f2329;
  white-space: pre-wrap;
  word-break: break-all;
  overflow-y: auto;
  background: #fff;
  border-radius: 4px;
  padding: 6px 8px;
}
/* 输入参数保持短窗口：它是诊断用的次要信息，不该把下面的过程/结果挤下去 */
.run-part-input {
  max-height: 160px;
}
/* 过程信息默认铺开：300 行以内不出块内滚动条，超过才滚（外层 panel-body 本来就会滚） */
.run-part-progress {
  max-height: calc(1.5em * 300);
}
.run-part-error {
  margin: 0;
  padding: 8px 10px;
  color: #d83931;
  background: #fff5f5;
  border: 1px solid #ffccc7;
  border-radius: 4px;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
.run-part-empty {
  color: #c0c4cc;
  font-size: 12px;
  padding: 4px 6px;
}
/* 错误行压过容器给的默认字色：执行中的「节点执行失败 / 执行失败 / 错误:」不管落在
   运行卡片还是结束后的 tab 里，都必须红（.run-card / .run-tab-body 的 .output-line 各有默认色） */
.run-card .output-line.error,
.run-tab-body .output-line.error,
.output-line.error {
  color: #d83931;
}
.run-card .output-line.result,
.run-tab-body .output-line.result,
.output-line.result {
  color: #52c41a;
}

.panel-tail {
  border-top: 0px solid #e0e0e0;
  display: flex;
  flex-direction: column;
  max-height: 38%;
  /* 不能是 auto/y-auto：工具条拖出 tail 区域时会被这里裁掉。
     滚动交给 .tail-content，顺手让 header / meta 固定不滚 */
  overflow: visible;
  /* 白底：面板整体一块白，不再垫灰，也不加分区线 */
  background-color: #fff;
  border-top: none;
  border-radius: 0px;  
}

.tail-header {
  flex-shrink: 0;
  padding: 8px 14px;
  background: #fff;
  border-bottom: none;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.tail-title {
  font-size: 12px;
  font-weight: 600;
  color: #1f2329;
  padding-left: 6px;
  /* 与运行卡片的 .run-card-title 同一套标题语言：左侧 3px 竖线定层级 */
  border-left: 3px solid #409eff;
}

.run-button {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  color: white;
  border: none;
  width: 26px;
  height: 26px;
  border-radius: 50%;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.2s;
  display: flex;
  align-items: center;
  justify-content: center;
}

.run-button:hover {
  opacity: 0.9;
  transform: scale(1.05);
}

.run-button svg {
  width: 16px;
  height: 16px;
}

/* 工具条：一条白胶囊装完所有按钮，按钮本身无边框无底色，
   只靠图标颜色区分语义（暂停蓝 / 继续绿 / 停止红 / 单步灰），hover 才浮出灰底 */
.debug-toolbar {
  display: flex;
  gap: 2px;
  align-items: center;
  padding: 3px;
  background: #fff;
  /* 边框压深一档 + 两层投影 + 顶部一道白高光：卡片有厚度，不再是浮在纸上的淡影 */
  border: 1px solid #e4e7ed;
  border-radius: 12px;
  box-shadow:
    0 1px 2px rgba(16, 24, 40, 0.06),
    0 2px 8px rgba(16, 24, 40, 0.08),
    inset 0 1px 0 rgba(255, 255, 255, 0.9);
  user-select: none;  /* 拖动时别把面板里的文字一起选上 */
}
/* 拖过之后浮在面板上：阴影加重，和「放下来的东西」匹配 */
.debug-toolbar--floating {
  position: absolute;
  z-index: 40;
  /* 浮起时把投影拉远一层，顶面的白高光跟着保留，质感与静止态一致 */
  box-shadow:
    0 8px 20px rgba(16, 24, 40, 0.16),
    0 2px 6px rgba(16, 24, 40, 0.08),
    inset 0 1px 0 rgba(255, 255, 255, 0.9);
}
/* 六点把手：2 列 x 3 行。
   左侧留 6px：胶囊内边距只有 3px，把手的点又小又浅，贴着边框会像被挤出去 */
.tb-handle {
  display: grid;
  grid-template-columns: repeat(2, 3px);
  grid-auto-rows: 3px;
  gap: 2px;
  padding: 0 4px 0 6px;
  cursor: grab;
  flex-shrink: 0;
  touch-action: none;  /* 触屏拖动时别带着面板滚 */
}
.tb-handle:active {
  cursor: grabbing;
}
.tb-dot {
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: #d0d5dd;
}
.tb-handle:hover .tb-dot {
  background: #98a2b3;
}
.dbg-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: none;
  border-radius: 9px;
  background: transparent;
  color: #667085;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}
.dbg-btn:hover:not(:disabled) {
  background: #f2f4f7;
  color: #344054;
}
.dbg-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}
.dbg-btn svg {
  width: 18px;
  height: 18px;
  /* Element Plus 的图标是纯填充路径，改 font-weight 没用：
     沿路径再加一圈同色描边（viewBox 是 1024，40 ≈ 4% 加粗），线条才真的变粗 */
  stroke: currentColor;
  stroke-width: 40;
  stroke-linejoin: round;
}
/* 步过 / 步入 / 步出是自绘图标（24 视口描边路径），不能吃上面那条 40 的描边宽度，
   否则线条糊成一团；圆点是实心落点标记，单独给填充 */
.dbg-btn svg.dbg-ico {
  fill: none;
  stroke-width: 1.7;
  stroke-linecap: round;
  stroke-linejoin: round;
}
.dbg-btn svg.dbg-ico .dot {
  fill: currentColor;
  stroke: none;
}
.dbg-btn.run {
  color: #17b26a;
}
.dbg-btn.run:hover:not(:disabled) {
  background: #ecfdf3;
  color: #099250;
}
.dbg-btn.pause {
  color: #2f8bff;
}
.dbg-btn.pause:hover:not(:disabled) {
  background: #eff6ff;
  color: #1677ff;
}
.dbg-btn.stop {
  color: #f04438;
}
.dbg-btn.stop:hover:not(:disabled) {
  background: #fef3f2;
  color: #d92d20;
}
/* ===== 调试 meta 栏：状态 + 子流程版本，一条装完 ===== */
.debug-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  padding: 6px 14px;
  font-size: 12px;
  color: #6b7280;
  /* 白底、无线：与上下两块连成一片，靠间距分层 */
  background: #fff;
  border-top: none;
}
.dm-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #d0d5dd;
  flex-shrink: 0;
}
.dm-dot--on {
  background: #12b76a;
  box-shadow: 0 0 0 3px rgba(18, 183, 106, 0.12);
}
.dm-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.dm-scope-label {
  color: #98a2b3;
}
.scope-radio :deep(.el-radio-button__inner) {
  padding: 3px 8px;
  font-size: 12px;
  line-height: 1.2;
}
.tail-content {
  flex: 1;
  min-height: 0;  /* flex 子项要能收缩，否则内部滚动不生效、内容会把 tail 撑破 */
  overflow-y: auto;
  padding: 12px;
}

.empty-fields {
  text-align: center;
  padding: 24px 12px;
  color: #98a2b3;
}

.empty-fields p {
  margin: 0 0 4px;
  font-size: 13px;
  color: #475467;
}

.empty-fields span {
  font-size: 12px;
}

.fields-container {
  display: flex;
  flex-direction: column;
  /* 字段有了卡片外框，间距要比裸字段时紧一点才不散 */
  gap: 8px;
}

/* 每个入参一张白卡：与上面的运行卡片同一套语言，不再裸平铺在灰底上 */
.field-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  background: #fff;
  /* 底色整块转白之后，#eef0f3 的边框在白底上几乎看不见，压深一档保住卡片的轮廓 */
  border: 1px solid #e9edf2;
  border-radius: 8px;
}

/* 字段头：徽章 + 名称 + 右侧类型标签，描述另起一行 —— 原来三样挤一行，描述一长就挤变形 */
.field-head {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.field-label {
  font-size: 13px;
  font-weight: 500;
  color: #1f2329;
  display: inline-flex;
  align-items: baseline;
  gap: 2px;
}

.required-mark {
  color: #f04438;
  font-size: 12px;
}

.field-type {
  margin-left: auto;
  flex-shrink: 0;
  padding: 1px 6px;
  font-family: 'SFMono-Regular', Consolas, monospace;
  font-size: 11px;
  color: #98a2b3;
  background: #f7f9fb;
  border-radius: 4px;
}

.field-desc {
  font-size: 12px;
  line-height: 1.5;
  color: #98a2b3;
}

.field-icon {
  width: 18px;
  height: 18px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  flex-shrink: 0;
}

.field-icon-number {
  background: #409eff;
  border-radius: 50%;
}

.field-icon-string {
  background: #67c23a;
  border-radius: 50%;
}

.field-icon-boolean {
  background: #9c27b0;
  border-radius: 50%;
}

.field-icon-object {
  background: #909399;
  border-radius: 50%;
}

.field-icon-number-array {
  background: #e6a23c;
  border-radius: 4px;
}

.field-icon-string-array {
  background: #f56c6c;
  border-radius: 4px;
}

.field-icon-boolean-array {
  background: #9c27b0;
  border-radius: 4px;
}

.field-icon-object-array {
  background: #909399;
  border-radius: 4px;
}

/* 形状统一：图标是徽章不是圆点，圆角与卡片刻度一致（写在类型规则之后才好覆盖） */
.field-icon-number,
.field-icon-string,
.field-icon-boolean,
.field-icon-object,
.field-icon-number-array,
.field-icon-string-array,
.field-icon-boolean-array,
.field-icon-object-array {
  border-radius: 6px;
}

.icon-text {
  line-height: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.field-control {
  display: flex;
  gap: 8px;
}

/* 布尔值：连体分段控件（二选一），不再是两枚各自独立的按钮 */
.boolean-control {
  display: flex;
  gap: 0;
}

.bool-btn {
  flex: 1;
  padding: 7px 12px;
  border: 1px solid #e4e7ed;
  border-radius: 0;
  background: #fff;
  font-size: 12px;
  color: #666;
  cursor: pointer;
  transition: all 0.15s;
}

.bool-btn + .bool-btn {
  margin-left: -1px;  /* 两条边框叠成一条，看起来是一整块 */
}

.bool-btn:first-child {
  border-radius: 8px 0 0 8px;
}

.bool-btn:last-child {
  border-radius: 0 8px 8px 0;
}

.bool-btn:hover {
  color: #1677ff;
}

.bool-btn.active {
  position: relative;   /* 让选中态的边框盖住相邻那条 */
  z-index: 1;
  background: #eff6ff;  /* 实心蓝改淡蓝：这里是"选了哪个"，不是"点了按钮" */
  border-color: #409eff;
  color: #1677ff;
}

.text-input {
  width: 100%;
  /* 关键：没有它，width:100% + 左右 padding 会撑出卡片（textarea 原先各自带，input 才露馅） */
  box-sizing: border-box;
  padding: 7px 10px;
  border: 1px solid #e4e7ed;
  border-radius: 8px;
  font-size: 13px;
  background: #fff;
}

.text-input:focus {
  outline: none;
  border-color: #409eff;
  /* 只换边框色太"跳"，补一圈淡光晕，聚焦点更柔和 */
  box-shadow: 0 0 0 2px rgba(64, 158, 255, 0.12);
}

.number-input {
  min-height: 34px;
  width: 100%;
}

.string-textarea {
  width: 100%;
  min-height: 64px;
  padding: 7px 10px;
  border-radius: 8px;
  resize: vertical;
  box-sizing: border-box;
}

.json-textarea {
  width: 100%;
  min-height: 80px;
  padding: 7px 10px;
  border-radius: 8px;
  font-size: 12px;
  font-family: monospace;
  resize: vertical;
  box-sizing: border-box;
}

.preview-section {
  margin-bottom: 20px;
}

.preview-section h3 {
  margin: 0 0 8px 0;
  color: #333;
  font-size: 16px;
}

.preview-section p {
  margin: 0;
  color: #666;
  font-size: 14px;
}

.preview-controls {
  margin-bottom: 20px;
}

.preview-button {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  color: white;
  border: none;
  padding: 8px 16px;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
}

.preview-button:hover {
  opacity: 0.9;
}

.preview-results {
  background: #f8f9fa;
  border-radius: 8px;
  padding: 12px;
}

.preview-results h4 {
  margin: 0 0 12px 0;
  color: #333;
  font-size: 14px;
}

.result-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.result-item {
  display: flex;
  align-items: center;
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 13px;
}

.result-item.success {
  background: #d4edda;
  color: #155724;
}

.result-item.error {
  background: #f8d7da;
  color: #721c24;
}

.result-icon {
  margin-right: 8px;
  font-weight: bold;
}

.result-text {
  flex: 1;
}

/* 历史执行 */
.history-view {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
}
.history-list {
  /* 详情改为覆盖列表区域显示后，列表独占剩余高度 */
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  border-bottom: 1px solid #ebeef5;
}
.history-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 16px;
  border-bottom: 1px solid #f0f0f0;
  cursor: pointer;
  font-size: 12px;
}
.history-item:hover {
  background: #f5f7fa;
}
.history-item.active {
  background: #e6f7ff;
}
.hi-time {
  color: #333;
  white-space: nowrap;
}
.hi-meta {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 6px;
  color: #999;
  font-size: 12px;
  min-width: 0;
}
.hi-tag {
  padding: 1px 6px;
  border-radius: 3px;
  background: #e6f7ff;
  color: #1890ff;
  font-size: 11px;
  white-space: nowrap;
}
.hi-tag-task {
  background: #fff7e6;
  color: #fa8c16;
}
.hi-nodes {
  color: #999;
  white-space: nowrap;
}
.hi-status {
  font-weight: 600;
}
.hi-cost {
  color: #999;
}
.status-success { color: #67c23a; }
.status-failed { color: #f56c6c; }
.status-cancelled { color: #909399; }
.history-detail {
  flex: 1;
  overflow-y: auto;
  padding: 12px 16px;
}
.history-empty {
  padding: 24px 0;
  text-align: center;
  color: #999;
  font-size: 13px;
}
.history-more {
  padding: 8px 16px;
  text-align: center;
  color: #999;
  font-size: 12px;
}
.history-filter {
  display: flex;
  align-items: center;
  flex-wrap: wrap; /* 面板窄：自定义起止时间会换行，而不是被父级 overflow 裁掉 */
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid #ebeef5;
  background: #fafbfc;
  flex-shrink: 0;
}
.hf-reset {
  margin-left: auto;
  font-size: 12px;
  color: #409eff;
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.hf-reset:hover {
  color: #69b1ff;
}
/* 右侧操作链接组：整组靠右，避免两个 margin-left:auto 平分剩余空间把「重置」顶到中间 */
.hf-links {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
.hf-clear {
  font-size: 12px;
  color: #f56c6c;
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.hf-clear:hover {
  color: #f89898;
}
/* 单条记录的删除图标：默认隐藏，hover 才出现，避免常显挤占窄面板的对齐 */
.hi-del {
  display: flex;
  align-items: center;
  color: #c0c4cc;
  cursor: pointer;
  opacity: 0;
  transition: opacity .15s;
  flex-shrink: 0;
}
.history-item:hover .hi-del {
  opacity: 1;
}
.hi-del:hover {
  color: #f56c6c;
}
.hf-status {
  padding: 4px 6px;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  font-size: 12px;
  background: #fff;
}
.hf-time {
  padding: 4px 6px;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  font-size: 12px;
  background: #fff;
}
.hf-sep {
  color: #999;
  font-size: 12px;
}
/* 详情返回条：吸顶，滚动时始终可见（负外边距抵消 .history-detail 的内边距） */
.hd-back {
  position: sticky;
  top: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 8px;
  margin: -12px -16px 10px;
  padding: 8px 16px;
  background: #fff;
  border-bottom: 1px solid #ebeef5;
}
.hd-back-btn {
  font-size: 12px;
  color: #409eff;
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.hd-back-btn:hover {
  color: #69b1ff;
}
.hd-back-info {
  margin-left: auto;
  font-size: 11px;
  color: #909399;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.hd-meta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 0 12px;
  font-size: 12px;
  color: #555;
  border-bottom: 1px solid #ebeef5;
  margin-bottom: 12px;
}
.hd-meta-row {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}
.hd-err {
  padding: 8px 10px;
  background: #fff5f5;
  color: #f56c6c;
  border-radius: 4px;
  font-size: 12px;
  margin-bottom: 12px;
}
.hd-section {
  margin-bottom: 16px;
}
.hd-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 600;
  color: #333;
  margin-bottom: 6px;
  padding-left: 6px;
  border-left: 3px solid #409eff;
}
.hd-title-text {
  flex: 1;
  min-width: 0;
}
.hd-toggle {
  font-size: 11px;
  font-weight: 400;
  color: #409eff;
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}
.hd-toggle:hover {
  color: #69b1ff;
}

.hd-section pre {
  margin: 0;
  padding: 10px;
  background: #fafbfc;
  border: 1px solid #e6e8eb;
  border-radius: 4px;
  font-size: 12px;
  font-family: monospace;
  white-space: pre-wrap;
  word-break: break-all;
  /* 与运行卡片同口径：300 行以内铺开，超过才在此块滚动 */
  max-height: calc(1.5em * 300);
  overflow: auto;
}
.hd-sub-title {
  font-size: 12px;
  color: #666;
  margin: 6px 0 4px;
}
.hd-msg {
  font-size: 12px;
  color: #555;
  padding: 2px 6px;
  word-break: break-all;
}
.hd-empty {
  color: #c0c4cc;
  font-size: 12px;
  padding: 4px 6px;
}

/* 滚动条外观统一交给 .pane-scroll：常驻占位、鼠标进入各自区域才显形 */
:deep(.tail-content) {
  overflow-y: auto !important;
  /* 原来这里给两侧各垫了 5px 的 #eaeceb 竖边，就是下方参数区看起来有左右灰带的原因 */
  border-left: none;
  border-right: none;
}


</style>