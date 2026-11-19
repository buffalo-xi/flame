<template>
  <div class="loop-node-props">
    <div class="tabs">
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'settings' }"
        @click="activeTab = 'settings'"
      >
        {{ t('flow.nodeProps.tabConfig') }}
      </button>
      <button
        class="tab-btn"
        :class="{ active: activeTab === 'lastRun' }"
        @click="activeTab = 'lastRun'"
      >
        {{ t('flow.nodeProps.tabResult') }}
      </button>
    </div>

    <div class="tab-content">
      <div v-if="activeTab === 'settings'" class="settings-panel">
        <div class="props-section">
          <div class="prop-group-horizontal">
            <div class="prop-item">
              <el-radio-group v-model="loopType" class="radio-group" @change="handleLoopTypeChange">
                <el-radio label="count">{{ t('flow.loopNode.modeCount') }}</el-radio>
                <el-radio label="each">{{ t('flow.loopNode.modeEach') }}</el-radio>
                <el-radio label="while">{{ t('flow.loopNode.modeWhile') }}</el-radio>
                <el-radio label="do_while">{{ t('flow.loopNode.modeDoWhile') }}</el-radio>
              </el-radio-group>
            </div>
          </div>
        </div>
        
        <div class="props-section" v-if="loopType === 'count'">
          <div class="section-title">
            <span>{{ t('flow.loopNode.countSection') }}</span>
          </div>
          
          <div class="prop-group-horizontal">
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.loopNode.countLabel') }}</label>
              <el-input-number
                v-model="loopCount"
                class="prop-input"
                :min="1"
                :max="1000"
                :step="1"
                :precision="0"
                controls-position="right"
                @change="handleLoopCountChange"
              />
            </div>
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.loopNode.countVarName') }}</label>
              <el-input 
                type="text" 
                v-model="loopVariableName" 
                class="prop-input"
                disabled
                placeholder="index"
              />
            </div>
          </div>
        </div>
        
        <div class="props-section" v-if="loopType === 'each'">
          <div class="section-title">
            <span>{{ t('flow.loopNode.eachSection') }}</span>
          </div>
          
          <div class="prop-group-horizontal">
            <div class="prop-item flex-expand">
              <label class="prop-label">{{ t('flow.loopNode.eachVar') }}</label>
              <button 
                class="show-content-btn expand-btn variable-select-btn" 
                @click="handleShowVariableSelectForEach($event)"
              >
                <template v-if="foreachSourceData.source_key">
                  <span class="type-badge" :class="getDataTypeIconClass(foreachSourceType)">
                    {{ getDataTypeIcon(foreachSourceType) }}
                  </span>
                  <span class="var-key">{{ foreachSourceData.source_key }}</span>
                  <template v-if="getSourceIcon(foreachSourceSrcType, foreachSourceRef)">
                    <template v-if="foreachSourceSrcType === 'node'">
                      <span class="node-icon" :class="getSourceIcon(foreachSourceSrcType, foreachSourceRef).class">
                        <el-icon><component :is="getSourceIcon(foreachSourceSrcType, foreachSourceRef).component" /></el-icon>
                      </span>
                      <span class="node-label">{{ getSourceIcon(foreachSourceSrcType, foreachSourceRef).nodeLabel }}</span>
                    </template>
                    <template v-else>
                      <el-icon :class="getSourceIcon(foreachSourceSrcType, foreachSourceRef).class">
                        <component :is="getSourceIcon(foreachSourceSrcType, foreachSourceRef).component" />
                      </el-icon>
                    </template>
                  </template>
                </template>
                <template v-else>
                  {{ t('flow.loopNode.eachVarPlaceholder') }}
                </template>
              </button>
            </div>
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.loopNode.itemVar') }}</label>
              <el-input 
                type="text" 
                v-model="foreachItemName" 
                class="prop-input small-input"
                disabled
                placeholder="item"
              />
            </div>
          </div>
        </div>
        
        <div class="props-section" v-if="loopType === 'while' || loopType === 'do_while'">
          <div class="section-title">
            <span>{{ t('flow.loopNode.conditionSection') }}</span>
          </div>

          <div class="section-title">
            <span class="title-as-label">{{ t('flow.common.inputVars') }}</span>
            <button class="add-variable-icon-btn" @click="handleAddLoopInputVariable">
              <el-icon><Plus /></el-icon>
            </button>
          </div>

          <div class="input-variables-list">
            <div v-if="Object.keys(loopInputVariables).length === 0" class="input-empty-state">
              <p>{{ t('flow.common.noInputVars') }}</p>
            </div>

            <div
              v-for="(variable, name) in loopInputVariables"
              :key="name"
              class="input-variable-row"
            >
              <input
                type="text"
                :value="name"
                class="input-variable-name-input"
                @focus="handleLoopInputVariableFocus(name)"
                @blur="handleUpdateLoopInputVariableName(name, $event.target.value)"
                :placeholder="t('flow.common.varName')"
              />
              <div class="input-variable-content">
                <button
                  class="show-content-btn input-variable-btn"
                  @click="handleShowLoopInputVariableSelect(name, $event, variable)"
                >
                  <template v-if="variable.source_key">
                    <span class="input-type-badge" :class="getDataTypeIconClass(variable.type)">
                      {{ getDataTypeIcon(variable.type) }}
                    </span>
                    <span class="input-var-key">{{ variable.source_key }}</span>
                    <template v-if="getSourceIcon(variable.source_type, variable.source_ref)">
                      <template v-if="variable.source_type === 'node'">
                        <span class="input-node-icon" :class="getSourceIcon(variable.source_type, variable.source_ref).class">
                          <el-icon><component :is="getSourceIcon(variable.source_type, variable.source_ref).component" /></el-icon>
                        </span>
                        <span class="input-node-label">{{ getSourceIcon(variable.source_type, variable.source_ref).nodeLabel }}</span>
                      </template>
                      <template v-else>
                        <el-icon :class="getSourceIcon(variable.source_type, variable.source_ref).class">
                          <component :is="getSourceIcon(variable.source_type, variable.source_ref).component" />
                        </el-icon>
                      </template>
                    </template>
                  </template>
                  <template v-else>
                    {{ t('flow.common.selectVar') }}
                  </template>
                </button>
                <button class="input-delete-btn" @click="handleDeleteLoopInputVariable(name)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>

          <div class="prop-group">
            <textarea
              class="prop-textarea"
              v-model="loopCondition"
              @keydown="handleConditionKeydown"
              @blur="handleConditionChange"
              :placeholder="t('flow.loopNode.conditionPlaceholder')"
            ></textarea>
          </div>

          <div class="prop-group-horizontal">
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.common.scriptLanguage') }}</label>
              <el-select v-model="conditionLang" class="prop-select" @change="handleConditionLangChange">
                <el-option v-for="lang in scriptLanguages" :key="lang" :label="lang" :value="lang" />
              </el-select>
            </div>
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.loopNode.maxIterations') }}</label>
              <el-input-number
                v-model="maxLoopCount"
                class="prop-input"
                :min="1"
                :max="1000"
                :step="1"
                :precision="0"
                controls-position="right"
                :placeholder="t('flow.loopNode.maxIterationsPlaceholder')"
                @change="handleMaxLoopCountChange"
              />
            </div>
          </div>

          <div class="prop-group-horizontal">
            <div class="prop-item">
              <label class="prop-label">{{ t('flow.loopNode.timeoutSeconds') }}</label>
              <el-switch
                v-model="conditionTimeoutEnabled"
                @change="handleConditionTimeoutChange"
              />
              <el-input
                v-if="conditionTimeoutEnabled"
                type="number"
                v-model="conditionTimeout"
                class="prop-input"
                @blur="handleConditionTimeoutChange"
                :min="1"
                :max="3600"
                placeholder="30"
              />
            </div>
          </div>
        </div>
        
        <OutputVarsConfig
          :selected-node="selectedNode"
          :existing-names="existingOutputNames"
          :reserved-names="['index', 'item']"
          @update-node-data="(data) => emit('update-node-data', data)"
        />

        <div class="props-section">
          <div ref="localSectionRef" class="section-title">
            <span>{{ t('flow.common.localVars') }}</span>
            <el-dropdown @command="handleAddLocalVariable" trigger="click" placement="bottom-end"
              popper-class="loop-local-popper"
              @visible-change="handleLocalDropdownVisible">
              <button class="add-variable-icon-btn">
                <el-icon><Plus /></el-icon>
              </button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="string">
                    <span class="dropdown-icon icon-string-inline">S</span>{{ t('flow.typeOptions.string') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="number">
                    <span class="dropdown-icon icon-number-inline">N</span>{{ t('flow.typeOptions.number') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="boolean">
                    <span class="dropdown-icon icon-boolean-inline">B</span>{{ t('flow.typeOptions.boolean') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="object">
                    <span class="dropdown-icon icon-object-inline">O</span>{{ t('flow.typeOptions.object') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="array[string]">
                    <span class="dropdown-icon icon-string-array-inline">S</span>{{ t('flow.typeOptions.arrayString') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="array[number]">
                    <span class="dropdown-icon icon-number-array-inline">N</span>{{ t('flow.typeOptions.arrayNumber') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="array[boolean]">
                    <span class="dropdown-icon icon-boolean-array-inline">B</span>{{ t('flow.typeOptions.arrayBoolean') }}
                  </el-dropdown-item>
                  <el-dropdown-item command="array[object]">
                    <span class="dropdown-icon icon-object-array-inline">O</span>{{ t('flow.typeOptions.arrayObject') }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>
          
          <div class="output-variables-cards">
            <div v-if="Object.keys(localVariables).length === 0" class="output-empty-state">
              <p>{{ t('flow.common.noLocalVars') }}</p>
            </div>
            
            <div
              v-for="(config, key) in localVariables"
              :key="key"
              class="output-variable-row"
            >
              <div class="output-variable-header">
                <span class="output-type-badge" :class="getOutputTypeClass(config.type)">
                  {{ getOutputTypeIcon(config.type) }}
                </span>
                <input
                  type="text"
                  :value="key"
                  :data-variable-key="`local-${key}`"
                  class="output-variable-name-input"
                  @change="handleRenameLocalVariable(key, $event.target.value)"
                  :placeholder="t('flow.common.varName')"
                />
              </div>
              <div class="output-variable-content">
                <input
                  type="text"
                  :value="config.description"
                  class="output-description-input"
                  :placeholder="t('flow.common.addDescription')"
                  @change="handleUpdateLocalVariable(key, 'description', $event.target.value)"
                />
                <button class="output-delete-btn" @click="handleDeleteLocalVariable(key)">
                  <el-icon><Delete /></el-icon>
                </button>
              </div>
            </div>
          </div>
        </div>

        <OnErrorConfig
          :selected-node="selectedNode"
          @update-node-data="(data) => emit('update-node-data', data)"
        />
      </div>

      <div v-if="activeTab === 'lastRun'" class="last-run-panel">
        <div v-if="!loopResult" class="nr-empty">{{ t('flow.node.run.empty') }}</div>
        <div v-else class="props-section">
          <div class="result-item result-meta-row">
            <span class="meta-item">
              <label class="result-label">{{ t('flow.node.run.status') }}</label>
              <span class="result-value" :class="loopStatusClass">{{ loopStatusText }}</span>
            </span>
            <span class="meta-item">
              <label class="result-label">{{ t('flow.node.run.time') }}</label>
              <span class="result-value">{{ loopStartTimeText || '-' }}</span>
            </span>
            <span class="meta-item">
              <label class="result-label">{{ t('flow.node.run.duration') }}</label>
              <span class="result-value">{{ loopResult.duration_ms }}ms</span>
            </span>
          </div>

          <div class="result-item result-meta-row">
            <span class="meta-item">
              <label class="result-label">{{ t('flow.loopNode.resultType') }}</label>
              <span class="result-value">{{ loopTypeText || '-' }}</span>
            </span>
            <span class="meta-item">
              <label class="result-label">{{ t('flow.loopNode.resultIterations') }}</label>
              <span class="result-value">{{ loopIterations }}</span>
            </span>
          </div>

          <div class="result-item">
            <label class="result-label">{{ t('flow.node.run.input') }}</label>
            <pre class="result-output">{{ loopInputText }}</pre>
          </div>

          <div class="result-item">
            <label class="result-label">{{ t('flow.node.run.output') }}</label>
            <pre class="result-output">{{ loopOutputText }}</pre>
          </div>

          <div v-if="loopLocalText" class="result-item">
            <label class="result-label">{{ t('flow.node.run.localVars') }}</label>
            <pre class="result-output">{{ loopLocalText }}</pre>
          </div>

          <div v-if="loopLogs.length" class="result-item">
            <label class="result-label">{{ t('flow.node.run.logs') }}</label>
            <div class="nr-logs">
              <div v-for="(log, i) in loopLogs" :key="i" class="nr-log">{{ log }}</div>
            </div>
          </div>

          <div v-if="loopResult.err_msg" class="result-item">
            <label class="result-label">{{ t('flow.node.run.error') }}</label>
            <div class="nr-log nr-error">{{ loopResult.err_msg }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>import { ref, watch, computed, inject, nextTick } from 'vue';
import { nodeResults } from '@/tenant/composables/runData.js';
import { ElRadio, ElRadioGroup, ElInput, ElSelect, ElOption, ElDropdown, ElDropdownMenu, ElDropdownItem } from 'element-plus';
import { Plus, Delete, ChromeFilled, Notification, User, Timer, Help, Link, MagicStick, Lightning, Promotion, RefreshRight, CircleClose, ChatLineSquare, Collection, Operation, Coin, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, RefreshLeft } from '@element-plus/icons-vue';
import OnErrorConfig from './OnErrorConfig.vue';
import OutputVarsConfig from './OutputVarsConfig.vue';
import { resolveSourceIcon } from './nodeSourceIcons.js'
import { getDataTypeIcon, getDataTypeIconClass } from './dataTypeBadge.js'
import { useInputVariables } from './inputVariables.js'
import { useI18n } from 'vue-i18n'

const FlowNodes = inject('FlowNodes', ref([]));
const scriptLanguages = inject('scriptLanguages', ref(['lua']));
const flowDebug = inject('flowDebug', null);
const defaultLang = computed(() => scriptLanguages.value[0] || 'lua');
const { t } = useI18n()

const props = defineProps({
 selectedNode: {
 type: Object,
 default: null
 },
 flowFlowVariables: {
 type: Object,
 default: () => ({})
 },
 flowEnvironmentVariables: {
 type: Object,
 default: () => ({})
 },
 flowArguments: {
 type: Object,
 default: () => ({})
 }
});
const emit = defineEmits(['update-node-data', 'show-variable-select']);
const activeTab = ref('settings');
const loopType = ref('count');
const loopCount = ref(10);
const loopVariableName = ref('index');
const loopCondition = ref('');
// null = 未填：后端的 max_times <= 0 表示「取系统上限」，界面用空表示同一件事，
// 不要把 -1 显示进一个下限为 1 的输入框（既看不出含义，用户一编辑就变成别的数）
const maxLoopCount = ref(null);

// positiveInt 把输入框的值归一成 ≥1 的整数，与模板的 :min="1" 对齐。
// 不再用 `|| 10` / `|| -1` 兜底的原因：那会把用户填的 0 静默改成一个魔法数，
// 面板显示的值与真正存下的值对不上，事后没人能看出发生过改写。
const positiveInt = (v, fallback) => {
  const n = Math.floor(Number(v));
  return Number.isFinite(n) && n >= 1 ? n : fallback;
};
const conditionLang = ref('lua');
const conditionTimeoutEnabled = ref(false);
const conditionTimeout = ref(30);
const foreachSourceKey = ref('');
const foreachSourceRef = ref('');
const foreachSourceType = ref('');
const foreachSourceSrcType = ref('');
const foreachItemName = ref('item');
const foreachItemType = ref('string');
const localVariables = ref({});

// ── 下拉菜单动态对齐右边缘 ──
const localSectionRef = ref(null)

const handleLocalDropdownVisible = (visible) => {
  if (!visible) return
  setTimeout(() => {
    const popper = document.querySelector('.loop-local-popper')
    const section = localSectionRef.value
    if (!popper || !section) return
    const sRect = section.getBoundingClientRect()
    popper.style.setProperty('left', `${sRect.right - popper.offsetWidth - 20}px`, 'important')
    popper.style.setProperty('top', `${sRect.bottom + 4}px`, 'important')
    popper.style.setProperty('transform', 'none', 'important')
  }, 0)
}

const loopInputVariables = ref({});


// 仅在切换节点(id 变化)时回填配置。同一节点编辑(含 blur 保存)不回填，
// 避免引用替换时用旧 config 覆盖正在输入的值（循环次数/最大次数弹回的根因）。
watch(() => props.selectedNode?.id, () => {
 const node = props.selectedNode;
 if (node && node.data && node.data.config) {
 const config = node.data.config;
 const loopConfig = config.loop || {};
 const loopVars = config.loop_vars || {};
 
 loopType.value = loopConfig.type || 'count';
 // 后端语义：total_times / max_times <= 0 时取系统上限（见 references/nodes/loop.md）。
 // 界面下限是 1，所以 <=0 或缺省一律读成「默认值 / 未填」，不把 -1 之类塞进输入框。
 const totalTimes = Math.floor(Number(loopConfig.total_times));
 loopCount.value = Number.isFinite(totalTimes) && totalTimes >= 1 ? totalTimes : 10;
 loopVariableName.value = 'index';
 loopCondition.value = loopConfig.condition || '';
 const maxTimes = Math.floor(Number(loopConfig.max_times));
 maxLoopCount.value = Number.isFinite(maxTimes) && maxTimes >= 1 ? maxTimes : null;
 conditionLang.value = loopConfig.lang || defaultLang.value;
 conditionTimeoutEnabled.value = !!loopConfig.timeout?.enabled;
 conditionTimeout.value = loopConfig.timeout?.seconds || 30;
 foreachSourceKey.value = loopConfig.source_key || '';
 foreachSourceRef.value = '';
 foreachSourceType.value = '';
 foreachSourceSrcType.value = '';
 // each 数据源类型/来源从 config.inputs[source_key] 推导
 if (foreachSourceKey.value && config.inputs && config.inputs[foreachSourceKey.value]) {
   const sourceInput = config.inputs[foreachSourceKey.value];
   foreachSourceType.value = sourceInput.type || '';
   foreachSourceSrcType.value = sourceInput.source_type || '';
   foreachSourceRef.value = sourceInput.source_ref || '';
 }
 foreachItemName.value = 'item';
 
 if (loopVars[foreachItemName.value]) {
 foreachItemType.value = loopVars[foreachItemName.value].type || 'string';
 }
 
    localVariables.value = config.locals ? { ...config.locals } : {};

 // 加载 config 中的 inputs 和 targets（inputs 已统一到 config 顶层）
 loopInputVariables.value = {};
 const loopInputs = config.inputs || {};
 for (const name in loopInputs) {
   if (loopInputs.hasOwnProperty(name)) {
     loopInputVariables.value[name] = { ...loopInputs[name], name };
   }
 }
 }
}, { immediate: true });
const handleLoopTypeChange = () => {
 const newConfig = { ...props.selectedNode.data.config };
 
 if (loopType.value === 'count') {
 newConfig.loop = {
 type: 'count',
 total_times: positiveInt(loopCount.value, 10)
};
 newConfig.loop_vars = {
 [loopVariableName.value]: {
 type: 'number',
 description: t('flow.nodeFactory.loopVarIndex')
 }
 };
 } else if (loopType.value === 'each') {
    const itemType = extractItemTypeFromArrayType(foreachSourceType.value);
    foreachItemType.value = itemType;

    newConfig.loop = {
      type: 'each',
      source_key: foreachSourceKey.value || '',
      break_on_empty: true
    };
 newConfig.loop_vars = {
 [foreachItemName.value]: {
 type: itemType,
 description: t('flow.nodeFactory.loopVarItem')
 },
 [loopVariableName.value]: {
 type: 'number',
 description: t('flow.nodeFactory.loopVarIndex')
 }
 };
 } else if (loopType.value === 'while' || loopType.value === 'do_while') {
    const inputs = buildLoopInputs();
    newConfig.loop = {
      type: loopType.value,
      lang: conditionLang.value,
      condition: loopCondition.value || '',
      max_times: positiveInt(maxLoopCount.value, -1),
      timeout: buildLoopTimeout()
    };
    newConfig.inputs = inputs;
    newConfig.loop_vars = undefined;
  }
 
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
const handleLoopCountChange = () => {
 // 先把清空后的 null 归一成下限值，再写配置：否则输入框空着、配置里却是 10，
 // 用户以为「留空 = 不跑」而实际会跑 10 次
 loopCount.value = positiveInt(loopCount.value, 10);
 const newConfig = { ...props.selectedNode.data.config };
 newConfig.loop = {
 type: 'count',
 total_times: loopCount.value
};
 newConfig.loop_vars = {
 [loopVariableName.value]: {
 type: 'number',
 description: t('flow.nodeFactory.loopVarIndex')
 }
 };
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
const handleLoopVariableNameChange = () => {
 const newConfig = { ...props.selectedNode.data.config };
 if (newConfig.loop_vars) {
 // 按 key 认出「非下标那一个」，不按 description 匹配：description 是给人看的、会随界面语言变
 const itemName = Object.keys(newConfig.loop_vars).find(key => key !== loopVariableName.value);
 const itemType = itemName ? newConfig.loop_vars[itemName].type : 'string';
 
 newConfig.loop_vars = {
 [loopVariableName.value]: {
 type: 'number',
 description: t('flow.nodeFactory.loopVarIndex')
 }
 };
 
 if (itemName) {
 newConfig.loop_vars[itemName] = {
 type: itemType,
 description: t('flow.nodeFactory.loopVarItem')
 };
 }
 }
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
// 循环输入变量的快照复用输入变量那套实现（字段一致，只是 ref 名字不同）
const { snapshot: buildLoopInputs } = useInputVariables({ inputVariables: loopInputVariables });

const buildLoopTimeout = () => ({
  enabled: conditionTimeoutEnabled.value,
  seconds: Math.max(1, Math.floor(conditionTimeout.value) || 30)
});

const handleConditionKeydown = (event) => {
  if (event.key === 'Tab') {
    event.preventDefault();
    const textarea = event.target;
    const start = textarea.selectionStart;
    const end = textarea.selectionEnd;
    const spaces = '   ';
    loopCondition.value = loopCondition.value.substring(0, start) + spaces + loopCondition.value.substring(end);
    nextTick(() => {
      textarea.selectionStart = textarea.selectionEnd = start + spaces.length;
    });
  }
};

const handleConditionChange = () => {
  const inputs = buildLoopInputs();
  const newConfig = { ...props.selectedNode.data.config };
  newConfig.loop = {
    type: loopType.value,
    lang: conditionLang.value,
    condition: loopCondition.value,
    max_times: positiveInt(maxLoopCount.value, -1),
    timeout: buildLoopTimeout()
  };
  newConfig.inputs = inputs;
  newConfig.loop_vars = undefined;
  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: newConfig } });
};

const handleConditionLangChange = () => {
  handleConditionChange();
};

const handleConditionTimeoutChange = () => {
  handleConditionChange();
};

const handleMaxLoopCountChange = () => {
  const inputs = buildLoopInputs();
  const newConfig = { ...props.selectedNode.data.config };
  newConfig.loop = {
    type: loopType.value,
    lang: conditionLang.value,
    condition: loopCondition.value,
    max_times: positiveInt(maxLoopCount.value, -1),
    timeout: buildLoopTimeout()
  };
  newConfig.inputs = inputs;
  newConfig.loop_vars = undefined;
  emit('update-node-data', { nodeId: props.selectedNode.id, data: { config: newConfig } });
};
const handleForeachItemNameChange = () => {
 const newConfig = { ...props.selectedNode.data.config };
 newConfig.loop_vars = {
 [foreachItemName.value]: {
 type: foreachItemType.value,
 description: t('flow.nodeFactory.loopVarItem')
 },
 [loopVariableName.value]: {
 type: 'number',
 description: t('flow.nodeFactory.loopVarIndex')
 }
 };
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
const handleShowVariableSelectForEach = (event) => {
 const callback = (value) => {
 try {
 const data = JSON.parse(value);
 // 记录遍历变量的名字、类型与来源（source_type/ref 同步写进 inputs）
 foreachSourceKey.value = data.source_key;
 foreachSourceType.value = data.variableType || '';
 foreachSourceSrcType.value = data.source_type || '';
 foreachSourceRef.value = data.source_ref || '';

 const itemType = extractItemTypeFromArrayType(data.variableType);
 foreachItemType.value = itemType;

 const newConfig = { ...props.selectedNode.data.config };

 newConfig.loop = {
      type: 'each',
      source_key: data.source_key,
      break_on_empty: true
    };

 newConfig.inputs = {
 [data.source_key]: {
 source_type: data.source_type || 'argument',
 source_ref: data.source_ref || '',
 source_key: data.source_key,
 type: data.variableType || ''
 }
 };

 newConfig.loop_vars = {
 [foreachItemName.value]: {
 type: itemType,
 description: t('flow.nodeFactory.loopVarItem')
 },
 [loopVariableName.value]: {
 type: 'number',
 description: t('flow.nodeFactory.loopVarIndex')
 }
 };

 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
 } catch (e) {
 console.error('Failed to parse variable value:', e);
 }
 };
 emit('show-variable-select', event, 'foreachSource', callback, 'array');
};

const extractItemTypeFromArrayType = (sourceType) => {
 if (!sourceType) return 'string';
 const match = sourceType.match(/array\[(\w+)\]/);
 return match ? match[1] : 'string';
};




// 变量来源图标：实现在 nodeSourceIcons.js（12 个面板共用，避免 source 类型再漂移）
const getSourceIcon = (sourceType, sourceRef) =>
  resolveSourceIcon(sourceType, sourceRef, FlowNodes.value)
// 局部变量卡片用的类型徽标（与 getDataTypeIcon 同表，模板里用的是这个旧名字）
const getOutputTypeIcon = getDataTypeIcon
const getOutputTypeClass = getDataTypeIconClass

const handleAddLocalVariable = (type) => {
 const sanitizedType = type.replace('[', '_').replace(']', '');
 let fieldName = `${sanitizedType}_1`;
 let counter = 1;
 const inputNames = Object.keys(loopInputVariables.value);
 const outputNames = getOutputVarNames();
 const reservedNames = ['index', 'item'];
 while (localVariables.value[fieldName] || inputNames.includes(fieldName) || outputNames.includes(fieldName) || reservedNames.includes(fieldName)) {
 fieldName = `${sanitizedType}_${counter}`;
 counter++;
 }
 const newVariables = { ...localVariables.value };
 newVariables[fieldName] = {
 type,
 description: '',
 time: Date.now()
 };
 localVariables.value = newVariables;
 const localsForSave = {};
 Object.keys(newVariables).forEach(key => {
 const { time, ...rest } = newVariables[key];
 localsForSave[key] = rest;
 });
 
 const newConfig = { ...props.selectedNode.data.config };
 newConfig.locals = localsForSave;
 
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
const handleRenameLocalVariable = (oldKey, newKey) => {
 const reservedNames = ['index', 'item'];
 const trimmedKey = newKey ? newKey.trim() : '';
 
 if (!trimmedKey || reservedNames.includes(trimmedKey.toLowerCase())) {
 const inputElement = document.querySelector(`input[data-variable-key="local-${oldKey}"]`);
 if (inputElement) {
 inputElement.value = oldKey;
 }
 return;
 }
 
 if (trimmedKey === oldKey) return;
 if (localVariables.value[trimmedKey] || loopInputVariables.value[trimmedKey] || getOutputVarNames().includes(trimmedKey)) {
 localVariables.value = { ...localVariables.value };
 return;
 }
 const newVariables = { ...localVariables.value };
 newVariables[trimmedKey] = { ...newVariables[oldKey] };
 delete newVariables[oldKey];
 localVariables.value = newVariables;
 
 const newConfig = { ...props.selectedNode.data.config };
 newConfig.locals = localVariables.value;
 
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
const handleUpdateLocalVariable = (key, field, value) => {
 if (localVariables.value[key]) {
 localVariables.value[key][field] = value;
 
 const newConfig = { ...props.selectedNode.data.config };
 newConfig.locals = localVariables.value;
 
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
 }
};
const handleDeleteLocalVariable = (key) => {
 delete localVariables.value[key];
 
 const newConfig = { ...props.selectedNode.data.config };
 newConfig.locals = localVariables.value;
 
 emit('update-node-data', {
 nodeId: props.selectedNode.id,
 data: { config: newConfig }
 });
};
</script>

<style scoped>
.loop-node-props {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}

.tabs {
  display: flex;
  border-bottom: 1px solid #eee;
  flex-shrink: 0;
}

.tab-btn {
  padding: 10px;
  border: none;
  background: transparent;
  font-size: 13px;
  color: #666;
  cursor: pointer;
  position: relative;
  transition: color 0.2s;
  margin-right: 20px;
}

.tab-btn:hover {
  color: #1890ff;
}

.tab-btn.active {
  color: #1890ff;
  font-weight: 500;
}

.tab-btn.active::after {
  content: '';
  position: absolute;
  bottom: -1px;
  left: 0;
  right: 0;
  height: 1px;
  background: #1890ff;
}

.tab-content {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
}

.settings-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.props-section {
  background: #fafafa;
  border-radius: 8px;
  padding: 5px;
}

.section-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
  font-weight: 600;
  color: #333;
  padding-bottom: 10px;
  border-bottom: 1px solid #e8e8e8;
  margin-bottom: 12px;
}

.add-variable-icon-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: 4px;
  background: #1890ff;
  color: white;
  cursor: pointer;
  font-size: 14px;
  transition: background-color 0.2s;
}

.add-variable-icon-btn:hover {
  background: #40a9ff;
}

.prop-group {
  margin-bottom: 12px;
}

.prop-group-horizontal {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
}

.prop-item {
  flex: 1;
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex-wrap: nowrap;
  white-space: nowrap;
}

.prop-item.flex-expand {
  flex: 2;
}

.prop-item.flex-expand .expand-btn {
  flex: 1;
  text-align: left;
}

.prop-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
  flex-shrink: 0;
  min-width: 60px;
}

.title-as-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
}

.prop-input {
  width: 100%;
}

.prop-input.small-input {
  width: 60px;
}

.prop-select {
  width: 100%;
}

.prop-textarea {
  width: 100%;
  min-height: 80px;
  margin-top: 8px;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  font-family: 'Monaco', 'Menlo', monospace;
  line-height: 1.7;
  resize: vertical;
  outline: none;
  box-sizing: border-box;
  background-color: #eee;
}

.prop-textarea:focus {
  border-color: #91caff;
}

.prop-textarea::placeholder {
  color: #bbb;
}

.prop-select :deep(.el-select__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.prop-select :deep(.el-select__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.prop-select :deep(.el-select__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

:deep(.el-switch) {
  margin-top: 4px;
}

.prop-input :deep(.el-input__wrapper) {
  background-color: #eee;
  border: none;
  box-shadow: none;
}

.prop-input :deep(.el-input__wrapper:hover) {
  border: none;
  box-shadow: none;
}

.prop-input :deep(.el-input__wrapper.is-focus) {
  border: none;
  box-shadow: none;
}

.radio-group {
  display: flex;
  gap: 16px;
}

.input-variables-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.input-variables-list + .section-title {
  margin-top: 12px;
}

.input-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 8px;
  text-align: center;
}

.input-empty-state p {
  color: #999;
  margin: 0;
  font-size: 12px;
}

.input-variable-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.input-variable-name-input {
  flex: 1;
  min-width: 0;
  max-width: 33.33%;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
}

.input-variable-name-input:focus {
  outline: none;
}

.input-variable-name-input::placeholder {
  color: #bbb;
}

.input-variable-content {
  flex: 2;
  display: flex;
  gap: 8px;
  align-items: center;
}

.show-content-btn {
  padding: 10px 12px;
  border: 0px solid #1890ff;
  border-radius: 4px;
  background: #eee;
  color: #1890ff;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.show-content-btn:hover:not(:disabled) {
  background: #e6f7ff;
}

.show-content-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.input-delete-btn {
  padding: 8px;
  border: none;
  background: none;
  color: #999;
  cursor: pointer;
}

.input-delete-btn:hover {
  color: #f56c6c;
}

.input-variable-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  overflow: hidden;
}

.input-variable-btn .input-var-key {
  font-size: 12px;
  color: #333;
  flex-shrink: 0;
}

.input-variable-btn .input-node-label {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
  max-width: 80px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.input-variable-btn .input-node-icon {
  width: 16px;
  height: 16px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #ffffff;
  flex-shrink: 0;
}

.input-variable-btn .input-node-icon.el-icon {
  font-size: 10px;
}

.input-variable-btn .icon-source-flow,
.input-variable-btn .icon-source-env,
.input-variable-btn .icon-source-argument {
  font-size: 14px;
  color: #1890ff;
  flex-shrink: 0;
}

.input-variable-btn .icon-start { background-color: #00704A; }
.input-variable-btn .icon-branch { background-color: #00bcd4; }
.input-variable-btn .icon-wait { background-color: #00bcd4; }
.input-variable-btn .icon-code { background-color: #f56565; }
.input-variable-btn .icon-http { background-color: #409eff; }
.input-variable-btn .icon-process { background-color: #00bcd4; }
.input-variable-btn .icon-result { background-color: #67c23a; }
.input-variable-btn .icon-progress { background-color: #eea236; }
.input-variable-btn .icon-llm { background-color: #409eff; }
.input-variable-btn .icon-knowledge { background-color: #67c23a; }
.input-variable-btn .icon-agent { background-color: #f56c6c; }
.input-variable-btn .icon-subflow { background-color: #667eea; }
.input-variable-btn .icon-assign { background-color: #ff9800; }
.input-variable-btn .icon-break { background-color: #ff9800; }
.input-variable-btn .icon-continue { background-color: #4caf50; }
.input-variable-btn .icon-return { background-color: #9c27b0; }
.input-variable-btn .icon-function { background-color: #667eea; }
.input-variable-btn .icon-fork { background-color: #4bc0c0; }
.input-variable-btn .icon-call { background-color: #ffc107; }
.input-variable-btn .icon-loop { background-color: #7b1fa2; }
.input-variable-btn .icon-default { background-color: #6b7280; }

.input-variable-btn .input-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
  border-radius: 50%;
}

.input-variable-btn .input-type-badge.type-string { background: #67c23a; }
.input-variable-btn .input-type-badge.type-number { background: #409eff; }
.input-variable-btn .input-type-badge.type-boolean { background: #9c27b0; }
.input-variable-btn .input-type-badge.type-object { background: #909399; }
.input-variable-btn .input-type-badge.type-array-string { background: #f56c6c; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-number { background: #e6a23c; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-boolean { background: #9c27b0; border-radius: 4px; }
.input-variable-btn .input-type-badge.type-array-object { background: #909399; border-radius: 4px; }

.output-variables-cards {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.output-empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 8px;
  text-align: center;
}

.output-empty-state p {
  color: #999;
  margin: 0;
  font-size: 12px;
}

.output-variable-row {
  display: flex;
  align-items: center;
  gap: 8px;
  border: 0px solid #e8e8e8;
  border-radius: 6px;
  padding: 0px 0px;
}

.output-variable-header {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  max-width: 33.33%;
}

.output-variable-content {
  flex: 2;
  display: flex;
  align-items: center;
  gap: 8px;
}

.output-type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 12px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
}

.output-type-badge.type-string {
  background: #67c23a;
  border-radius: 50%;
}

.output-type-badge.type-number {
  background: #409eff;
  border-radius: 50%;
}

.output-type-badge.type-boolean {
  background: #9c27b0;
  border-radius: 50%;
}

.output-type-badge.type-object {
  background: #909399;
  border-radius: 50%;
}

.output-type-badge.type-array-string {
  background: #f56c6c;
  border-radius: 4px;
}

.output-type-badge.type-array-number {
  background: #e6a23c;
  border-radius: 4px;
}

.output-type-badge.type-array-boolean {
  background: #9c27b0;
  border-radius: 4px;
}

.output-type-badge.type-array-object {
  background: #909399;
  border-radius: 4px;
}

.output-variable-name-input {
  flex: 1;
  min-width: 0;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  font-size: 13px;
  outline: none;
  transition: border-color 0.2s;
}

.output-variable-name-input:focus {
  outline: none;
}

.output-variable-name-input::placeholder {
  color: #bbb;
}

.output-description-input {
  flex: 1;
  min-width: 0;
  padding: 12px 10px;
  border: 0px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #666;
  background-color: #eeeeee;
  outline: none;
  transition: border-color 0.2s;
}

.output-description-input:focus {
  outline: none;
}

.output-description-input::placeholder {
  color: #bbb;
}

.output-delete-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: 3px;
  font-size: 12px;
  color: #999;
  background: transparent;
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.output-delete-btn:hover {
  background: #fff5f5;
  color: #f56c6c;
}

.dropdown-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  margin-right: 6px;
  flex-shrink: 0;
}

.dropdown-icon.icon-string-inline {
  background: #67c23a;
  border-radius: 50%;
}

.dropdown-icon.icon-number-inline {
  background: #409eff;
  border-radius: 50%;
}

.dropdown-icon.icon-boolean-inline {
  background: #9c27b0;
  border-radius: 50%;
}

.dropdown-icon.icon-object-inline {
  background: #909399;
  border-radius: 50%;
}

.dropdown-icon.icon-string-array-inline {
  background: #f56c6c;
  border-radius: 4px;
}

.dropdown-icon.icon-number-array-inline {
  background: #e6a23c;
  border-radius: 4px;
}

.dropdown-icon.icon-boolean-array-inline {
  background: #9c27b0;
  border-radius: 4px;
}

.dropdown-icon.icon-object-array-inline {
  background: #909399;
  border-radius: 4px;
}

.last-run-panel {
  padding: 0;
}

/* 执行结果页的 section 单独加足内边距（不动设置页共用的 .props-section 5px） */
.last-run-panel .props-section {
  padding: 16px 12px;
}

.result-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}

.result-meta-row {
  flex-direction: row;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
}

.result-meta-row .meta-item {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.nr-empty {
  padding: 24px 0;
  text-align: center;
  color: #999;
  font-size: 13px;
}

.nr-logs {
  max-height: 200px;
  overflow: auto;
  padding: 8px 10px;
  background: #fff;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
}

.nr-log {
  font-size: 12px;
  color: #555;
  line-height: 1.6;
  word-break: break-all;
}

.nr-error {
  color: #f56565;
}

.result-label {
  font-size: 12px;
  color: #333;
  font-weight: 600;
}

.result-value {
  font-size: 13px;
  color: #333;
}

.status-success {
  color: #67c23a;
}

.status-error {
  color: #f56565;
}

.status-running {
  color: #409eff;
}

.status-default {
  color: #999;
}

.result-output {
  width: 100%;
  min-height: 100px;
  padding: 10px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 12px;
  color: #333;
  background: #fafafa;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
}

.variable-select-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  max-width: 100%;
  overflow: hidden;
}

.variable-select-btn .var-key {
  font-size: 12px;
  color: #333;
  flex-shrink: 0;
}

.variable-select-btn .node-label {
  font-size: 12px;
  color: #666;
  flex-shrink: 0;
  max-width: 80px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.variable-select-btn .node-icon {
  width: 16px;
  height: 16px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #ffffff;
  flex-shrink: 0;
}

.variable-select-btn .node-icon.el-icon {
  font-size: 10px;
}

.variable-select-btn .icon-source-flow,
.variable-select-btn .icon-source-env,
.variable-select-btn .icon-source-argument {
  font-size: 14px;
  color: #1890ff;
  flex-shrink: 0;
}

.variable-select-btn .icon-start {
  background-color: #00704A;
}

.variable-select-btn .icon-branch {
  background-color: #00bcd4;
}

.variable-select-btn .icon-wait {
  background-color: #00bcd4;
}

.variable-select-btn .icon-code {
  background-color: #f56565;
}

.variable-select-btn .icon-http {
  background-color: #409eff;
}

.variable-select-btn .icon-process {
  background-color: #00bcd4;
}

.variable-select-btn .icon-result {
  background-color: #67c23a;
}

.variable-select-btn .icon-progress {
  background-color: #eea236;
}

.variable-select-btn .icon-llm {
  background-color: #409eff;
}

.variable-select-btn .icon-knowledge {
  background-color: #67c23a;
}

.variable-select-btn .icon-agent {
  background-color: #f56c6c;
}

.variable-select-btn .icon-subflow {
  background-color: #667eea;
}

.variable-select-btn .icon-assign {
  background-color: #ff9800;
}

.variable-select-btn .icon-break {
  background-color: #ff9800;
}

.variable-select-btn .icon-continue {
  background-color: #4caf50;
}

.variable-select-btn .icon-return {
  background-color: #9c27b0;
}

.variable-select-btn .icon-function {
  background-color: #667eea;
}

.variable-select-btn .icon-fork {
  background-color: #4bc0c0;
}

.variable-select-btn .icon-call {
  background-color: #ffc107;
}

.variable-select-btn .icon-loop {
  background-color: #7b1fa2;
}

.variable-select-btn .icon-default {
  background-color: #6b7280;
}

.variable-select-btn .type-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 10px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  flex-shrink: 0;
  border-radius: 50%;
}

.variable-select-btn .type-badge.type-string {
  background: #67c23a;
}

.variable-select-btn .type-badge.type-number {
  background: #409eff;
}

.variable-select-btn .type-badge.type-boolean {
  background: #9c27b0;
}

.variable-select-btn .type-badge.type-object {
  background: #909399;
}

.variable-select-btn .type-badge.type-array-string {
  background: #f56c6c;
  border-radius: 4px;
}

.variable-select-btn .type-badge.type-array-number {
  background: #e6a23c;
  border-radius: 4px;
}

.variable-select-btn .type-badge.type-array-boolean {
  background: #9c27b0;
  border-radius: 4px;
}

.variable-select-btn .type-badge.type-array-object {
  background: #909399;
  border-radius: 4px;
}
</style>

<style>
/* 下拉菜单右侧与窗口对齐 */
.dropdown-align-right {
  margin-right: 8px !important;
}
</style>