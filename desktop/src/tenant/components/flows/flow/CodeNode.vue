<template>
  <div class="code-node" :class="{ 'selected': isSelected, 'has-concurrency': concurrencyEnabled, 'has-breakpoint': bpActive }" ref="nodeRef" @click="handleNodeClick">
    <span v-if="bpActive" class="bp-dot"></span>
    <div class="node-header">
      <div class="icon-wrapper icon-code"><el-icon><MagicStick /></el-icon></div>
      <div class="node-label">{{ data.label || t('flow.node.defaultLabel.code') }}</div>
      <NodeFindingsBadge :id="id" />
      <el-dropdown trigger="hover" placement="right">
        <span class="more-icon-wrapper" @click.stop @mousedown.stop @pointerdown.stop><el-icon class="more-icon"><MoreFilled /></el-icon></span>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item @click="deleteNode">
              <el-icon><Delete /></el-icon>
              <span>{{ t('flow.node.action.delete') }}</span>
            </el-dropdown-item>
            <el-dropdown-item @click="toggleBreakpoint">
              <el-icon><Flag /></el-icon>
              <span>{{ bpActive ? t('flow.node.breakpoint.unset') : t('flow.node.breakpoint.set') }}</span>
            </el-dropdown-item>
            <el-dropdown-item @click="toggleConcurrency">
              <el-icon><component :is="concurrencyEnabled ? 'Close' : 'CircleCheck'" /></el-icon>
              <span>{{ concurrencyEnabled ? t('flow.node.concurrency.disable') : t('flow.node.concurrency.enable') }}</span>
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>
    <div
      class="concurrency-row"
      :class="{ 'always-show': concurrencyEnabled }"
      @click.stop
      @mousedown.stop
      @pointerdown.stop
    >
      <span class="concurrency-label">{{ t('flow.node.concurrency.title') }}</span>
      <el-select v-model="concurrencyGroup" class="concurrency-select" :disabled="!concurrencyEnabled">
        <el-option :label="t('flow.node.concurrency.mono')" value="mono" />
        <el-option-group v-if="forkNodes.length" :label="t('flow.node.concurrency.forkGroup')">
          <el-option
            v-for="node in forkNodes"
            :key="node.id"
            :label="node.data?.label || node.id"
            :value="node.id"
          />
        </el-option-group>
      </el-select>
      <button
        v-if="concurrencyEnabled"
        class="concurrency-cancel-btn"
        :title="t('flow.node.concurrency.disable')"
        @click="clearConcurrency"
      >
        ✕
      </button>
    </div>
    <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
    <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
    <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle custom-handle-bottom custom-handle-source" />
    <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle custom-handle-right custom-handle-source" />    
  </div>
</template>

<script setup>
import { ref, computed, watch, inject } from 'vue'
import { MagicStick, MoreFilled, Delete, Close, CircleCheck, Flag } from '@element-plus/icons-vue'
import { Handle } from '@vue-flow/core'
import { readConcurrencyGroup, buildConcurrencyConfig } from './concurrencyConfig.js'
import { useI18n } from 'vue-i18n'
import NodeFindingsBadge from './NodeFindingsBadge.vue'

const { t } = useI18n()

const props = defineProps({
  data: {
    type: Object,
    default: () => ({})
  },
  id: {
    type: String,
    required: true
  },
  getNodeById: {
    type: Function,
    default: null
  }
})

const emit = defineEmits(['handleClick', 'delete', 'updateData'])
const injectedSelectedNode = inject('selectedNode', null)
const FlowNodes = inject('FlowNodes', ref([]))
const FlowEdges = inject('FlowEdges', ref([]))
const flowDebug = inject('flowDebug', null)
const bpActive = computed(() => !!flowDebug && flowDebug.isBreakpoint(props.id))
const nodeRef = ref(null)
const isSelected = ref(false)
const concurrencyEnabled = ref(readConcurrencyGroup(props.data.config) !== 'none')
const concurrencyGroup = ref(readConcurrencyGroup(props.data.config))

// 沿入边向上查找可用的 fork 节点（排除已被其他 wait 节点占用的）
const forkNodes = computed(() => {
  const allNodes = FlowNodes.value
  const edges = FlowEdges.value
  const currentId = props.id
  const nodeMap = new Map(allNodes.map(n => [n.id, n]))

  const foundForks = []
  const foundWaits = []
  const visited = new Set()
  visited.add(currentId)

  const queue = [currentId]

  while (queue.length > 0) {
    const nodeId = queue.shift()
    const incomingEdges = edges.filter(e => String(e.target) === String(nodeId))

    if (incomingEdges.length === 0) {
      const node = nodeMap.get(nodeId)
      if (node?.parentNode && !visited.has(node.parentNode)) {
        visited.add(node.parentNode)
        const parentNode = nodeMap.get(node.parentNode)
        if (parentNode) {
          queue.push(parentNode.id)
        }
      }
      continue
    }

    for (const edge of incomingEdges) {
      const sourceId = edge.source
      if (visited.has(sourceId)) continue
      visited.add(sourceId)

      const sourceNode = nodeMap.get(sourceId)
      if (!sourceNode) continue

      if (sourceNode.type === 'fork') {
        foundForks.push(sourceNode)
      } else if (sourceNode.type === 'wait') {
        foundWaits.push(sourceNode)
      }
      queue.push(sourceId)
    }
  }

  const usedForkIDs = new Set()
  for (const n of foundWaits) {
    const forkID = n.data?.config?.fork_node_id
    if (forkID) usedForkIDs.add(String(forkID))
  }

  return foundForks.filter(n => !usedForkIDs.has(String(n.id)))
})

if (injectedSelectedNode) {
  watch(() => injectedSelectedNode.value, (newSelectedNode) => {
    isSelected.value = newSelectedNode?.id === props.id
  }, { immediate: true, deep: true })
}

watch(concurrencyEnabled, (enabled) => {
  if (enabled) {
    if (!concurrencyGroup.value || concurrencyGroup.value === 'none') {
      concurrencyGroup.value = 'mono'
    }
    emit('updateData', { nodeId: props.id, data: { config: { concurrency: buildConcurrencyConfig(concurrencyGroup.value) } } })
  } else {
    concurrencyGroup.value = 'none'
    emit('updateData', { nodeId: props.id, data: { config: { concurrency: undefined } } })
  }
})

watch(concurrencyGroup, (newValue) => {
  if (newValue && newValue !== 'none') {
    emit('updateData', { nodeId: props.id, data: { config: { concurrency: buildConcurrencyConfig(newValue) } } })
  }
})

const handleNodeClick = (event) => {
  const handleElement = event.target.closest('.custom-handle')
  if (handleElement) {
    event.stopPropagation()    
    const handleId = handleElement.getAttribute('data-handleid') || handleElement.id
    if (handleId && handleId.includes('source')) {
      emit('handleClick', props.id, handleId, event)
    }
  }
}

const toggleConcurrency = () => {
  concurrencyEnabled.value = !concurrencyEnabled.value
}

const clearConcurrency = () => {
  concurrencyEnabled.value = false
}

const deleteNode = () => {
  emit('delete', props.id)
}

const toggleBreakpoint = () => {
  flowDebug?.toggleBreakpoint(props.id)
}
</script>

<style scoped>
.code-node {
  width: auto;
  min-width: 100px;
  max-width: 300px;
  height: 32px;
  border: 1px solid #bbb;
  border-radius: 16px;
  background-color: #fff;
  display: flex;
  flex-direction: row;
  align-items: center;
  justify-content: flex-start;
  cursor: pointer;
  padding: 0 10px;
  position: relative;
  white-space: nowrap;
}

.code-node:hover {
  box-shadow: 0 2px 12px rgba(245, 101, 101, 0.3);
}

.code-node.selected {
  box-shadow: 0 0 0 1px #409eff;
}

.code-node.selected:hover {
  box-shadow: 0 0 0 1px #409eff, 0 2px 12px rgba(64, 158, 255, 0.3);
}

.node-header {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex: 1;
  padding-right: 5px;
}

.more-icon-wrapper {
  margin-left: auto;
  opacity: 0;
  transition: opacity 0.2s;
  cursor: pointer;
}

.code-node:hover .more-icon-wrapper {
  opacity: 1;
}

.more-icon {
  font-size: 16px;
  color: #999;
}

.node-label {
  font-size: 11px;
  font-weight: 700;
  color: #333;
  white-space: nowrap;
}

.icon-wrapper {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 8px;
  margin-right: 4px;
}

.icon-code { background-color: #f56565; }

.icon-code .el-icon {
  font-size: 16px;
  color: #ffffff;
}

.custom-handle {
  width: 16px !important;
  height: 16px !important;
  background-color: #00704A !important;
  border-radius: 50% !important;
  opacity: 0 !important;
  position: absolute !important;
}

.custom-handle::before {
  content: '+';
  color: white;
  font-size: 12px;
  font-weight: bold;
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.code-node:hover .custom-handle {
  opacity: 1 !important;
}

.custom-handle-top {
  top: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

.custom-handle-left {
  left: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

.custom-handle-bottom {
  bottom: -8px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

.custom-handle-right {
  right: -8px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

.custom-handle-target {
  background-color: #003366 !important;
}

.custom-handle-target::before {
  content: '-';
  color: white;
  font-size: 16px;
  font-weight: bold;
  position: absolute;
  top: 45%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.handle-hidden {
  display: none;
}

.concurrency-row {
  display: none;
  flex-direction: row;
  align-items: center;
  gap: 2px;
  margin-top: 0px;
  padding-top: 0px;
  padding-bottom: 0px;
  padding-left: 3px;
  border-top: 1px solid #eee;
  width: 100%;
  box-sizing: border-box;
}

.concurrency-row.always-show {
  display: flex;
}

.code-node.has-concurrency {
  height: auto !important;
  flex-direction: column !important;
  align-items: stretch !important;
  padding: 4px 8px;
}

.code-node.has-concurrency .concurrency-row {
  width: 100%;
  min-width: 120px;
  margin-top: 2px;
}

.concurrency-label {
  font-size: 10px;
  color: #666;
  white-space: nowrap;
  font-weight: 600;
}

.concurrency-cancel-btn {
  width: 18px;
  height: 18px;
  flex-shrink: 0;
  border: 1px solid #ddd;
  border-radius: 3px;
  background: #fff;
  color: #999;
  font-size: 10px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  transition: all 0.2s;
}
.concurrency-cancel-btn:hover {
  border-color: #f56565;
  color: #f56565;
  background: #fff5f5;
}

.concurrency-select {
  flex: 1;
  font-size: 10px;
  padding: 0px 0px;
  min-width: 60px;
  width: 100%;
  display: flex;
  align-items: center;
  margin-top: 3px;
}

.concurrency-select :deep(.el-select__wrapper) {
  font-size: 10px;
  width: 100%;
  min-width: 120px;
  padding: 0px 12px;
  border: none !important;
  box-shadow: none !important;
  display: flex;
  align-items: center;
  line-height: 14px;
}

.concurrency-select :deep(.el-select__trigger) {
  white-space: nowrap;
  padding: 0px 0px;
  display: flex;
  align-items: center;
  line-height: 14px;
}

.concurrency-select :deep(.el-select__input) {
  font-size: 10px;
  width: 100%;
  min-width: 60px;
  line-height: 14px;
  height: 14px;
}

.concurrency-select :deep(.el-select__tags) {
  width: 100%;
  min-width: 60px;
}
</style>