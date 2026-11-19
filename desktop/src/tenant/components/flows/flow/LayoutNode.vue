<template>
  <div class="layout-node" ref="nodeRef" @click="handleNodeClick">
    <div class="node-content">
      <el-dropdown trigger="hover" placement="right">
        <div class="icon-wrapper icon-layout"><el-icon><Remove /></el-icon></div>
        <template #dropdown>
          <el-dropdown-menu>
            <!-- 断点菜单不给：layout 运行时被跳过，设了也不会停 -->
            <el-dropdown-item @click="deleteNode">
              <el-icon><Delete /></el-icon>
              <span>{{ t('flow.node.action.delete') }}</span>
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>
    
    <!-- 名字平时不显示（节点只是个 32px 圆点），悬停时浮在圆点上方。
         这里不用 el-tooltip：图标上挂着 el-dropdown，悬停图标会同时弹出两个 popper。 -->
    <span class="node-name-tip">{{ data.label || t('flow.node.fallbackLabel.layout') }}</span>

    <Handle type="target" position="top" :id="'target' + id + 'top'" class="custom-handle custom-handle-top custom-handle-target" />
    <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle custom-handle-bottom custom-handle-source" />
    <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle custom-handle-right custom-handle-source" />
    <Handle type="target" position="left" :id="'target' + id + 'left'" class="custom-handle custom-handle-left custom-handle-target" />
    
  </div>
</template>

<script setup>
import { ref, watch, inject } from 'vue'
import { Remove, Delete } from '@element-plus/icons-vue'
import { Handle } from '@vue-flow/core'
import { useI18n } from 'vue-i18n'

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

const emit = defineEmits(['handleClick', 'delete'])
const nodeRef = ref(null)

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

const deleteNode = () => {
  emit('delete', props.id)
}
</script>

<style scoped>
.layout-node {
  width: 32px;
  height: 32px;
  border: 2px dashed #ccc;
  border-radius: 50%;
  background-color: rgba(255, 255, 255, 0.8);
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  position: relative;
  transition: all 0.2s;
}

.layout-node:hover {
  border-color: #409eff;
  box-shadow: 0 0 12px rgba(64, 158, 255, 0.3);
}

.layout-node.selected {
  border-color: #409eff;
  box-shadow: 0 0 0 2px rgba(64, 158, 255, 0.3);
}

.node-content {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.icon-wrapper {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background-color: #fff
}

.icon-layout .el-icon {
  font-size: 15px;
  color: #222;
}

.node-label {
  font-size: 12px;
  font-weight: 500;
  color: #666;
  white-space: nowrap;
}

/* 悬停显示的名字：绝对定位，不参与布局 —— 否则 hover 时节点变宽会把四个 handle 挤走 */
.node-name-tip {
  display: none;
  position: absolute;
  left: 50%;
  bottom: calc(100% + 10px);
  transform: translateX(-50%);
  padding: 2px 6px;
  border-radius: 4px;
  background-color: rgba(0, 0, 0, 0.75);
  color: #fff;
  font-size: 11px;
  line-height: 16px;
  white-space: nowrap;
  pointer-events: none;
  z-index: 20;
}

.layout-node:hover .node-name-tip {
  display: block;
}

.custom-handle {
  width: 10px !important;
  height: 10px !important;
  background-color: #00704A !important;
  border-radius: 50% !important;
  opacity: 0 !important;
  position: absolute !important;
  border: 1px solid #fff !important;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
  z-index: 100 !important;
}

.custom-handle-top {
  top: -5px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

.custom-handle-bottom {
  bottom: -5px !important;
  left: 50% !important;
  transform: translateX(-50%);
}

.custom-handle-left {
  left: -5px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

.custom-handle-right {
  right: -5px !important;
  top: 50% !important;
  transform: translateY(-50%);
}

.custom-handle::before {
  content: '+';
  color: white;
  font-size: 8px;
  font-weight: bold;
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.layout-node:hover .custom-handle {
  opacity: 1 !important;
}



.custom-handle-target {
  background-color: #003366 !important;
}

.custom-handle-target::before {
  content: '-';
  color: white;
  font-size: 10px;
  font-weight: bold;
  position: absolute;
  top: 45%;
  left: 50%;
  transform: translate(-50%, -50%);
}
</style>