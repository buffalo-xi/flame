<template>
  <div v-if="visible" class="node-panel" :style="{ ...panelStyle, left: left }" @click.stop>
    <div class="panel-header">
      <span
        class="panel-tab"
        :class="{ active: activeTab === 'node' }"
        @click="activeTab = 'node'"
      >
        {{ t('flow.nodePanel.tabNode') }}
      </span>
      <span
        class="panel-tab"
        :class="{ active: activeTab === 'tool' }"
        @click="activeTab = 'tool'"
      >
        {{ t('flow.nodePanel.tabTool') }}
      </span>
    </div>
    <div class="panel-content">
      <div v-show="activeTab === 'node'" class="panel-section">
        <div class="node-group">
          <div class="group-title">{{ t('flow.nodePanel.groupCommon') }}</div>
          <div class="group-items">
            <div class="node-card" @click="handleNodeClick('llm')">
              <span class="icon-wrapper icon-llm"><el-icon><ChatLineSquare /></el-icon></span>
              <span class="node-label">{{ nodeLabel('llm') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('memory')">
              <span class="icon-wrapper icon-memory"><el-icon><Clock /></el-icon></span>
              <span class="node-label">{{ nodeLabel('memory') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('code')">
              <span class="icon-wrapper icon-code"><el-icon><MagicStick /></el-icon></span>
              <span class="node-label">{{ nodeLabel('code') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('assign')">
              <span class="icon-wrapper icon-assign"><el-icon><Coin /></el-icon></span>
              <span class="node-label">{{ nodeLabel('assign') }}</span>
            </div>            
            <div class="node-card" @click="handleNodeClick('http')">
              <span class="icon-wrapper icon-http"><el-icon><Lightning /></el-icon></span>
              <span class="node-label">{{ nodeLabel('http') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('mcp')">
              <span class="icon-wrapper icon-mcp"><el-icon><Tools /></el-icon></span>
              <span class="node-label">{{ nodeLabel('mcp') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('rpc')">
              <span class="icon-wrapper icon-rpc"><el-icon><Mouse /></el-icon></span>
              <span class="node-label">{{ nodeLabel('rpc') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('result')">
              <span class="icon-wrapper icon-result"><el-icon><Promotion /></el-icon></span>    
              <span class="node-label">{{ nodeLabel('result') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('progress')">
              <span class="icon-wrapper icon-progress"><el-icon><RefreshRight /></el-icon></span>
              <span class="node-label">{{ nodeLabel('progress') }}</span>
            </div>
            
          </div>
        </div>
        <div class="node-group">
          <div class="group-title">{{ t('flow.nodePanel.groupLogic') }}</div>
          <div class="group-items">
            <div class="node-card" @click="handleNodeClick('branch')">
              <span class="icon-wrapper icon-branch"><el-icon><Connection /></el-icon></span>
              <span class="node-label">{{ nodeLabel('branch') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('intent')">
              <span class="icon-wrapper icon-intent"><el-icon><Aim /></el-icon></span>
              <span class="node-label">{{ nodeLabel('intent') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('break')">
              <span class="icon-wrapper icon-break"><el-icon><Minus /></el-icon></span>
              <span class="node-label">{{ nodeLabel('break') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('continue')">
              <span class="icon-wrapper icon-continue"><el-icon><Upload /></el-icon></span>
              <span class="node-label">{{ nodeLabel('continue') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('return')">
              <span class="icon-wrapper icon-return"><el-icon><CircleCheck /></el-icon></span>
              <span class="node-label">{{ nodeLabel('return') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('exit')">
              <span class="icon-wrapper icon-exit"><el-icon><CircleClose /></el-icon></span>
              <span class="node-label">{{ nodeLabel('exit') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('process')">
              <span class="icon-wrapper icon-process"><el-icon><Help /></el-icon></span>
              <span class="node-label">{{ nodeLabel('process') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('function')">
              <span class="icon-wrapper icon-function"><el-icon><ScaleToOriginal /></el-icon></span>
              <span class="node-label">{{ nodeLabel('function') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('fork')">
              <span class="icon-wrapper icon-fork"><el-icon><Monitor /></el-icon></span>
              <span class="node-label">{{ nodeLabel('fork') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('wait')">
              <span class="icon-wrapper icon-wait"><el-icon><Link /></el-icon></span>
              <span class="node-label">{{ nodeLabel('wait') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('call')">
              <span class="icon-wrapper icon-call"><el-icon><Mouse /></el-icon></span>
              <span class="node-label">{{ nodeLabel('call') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('loop')">
              <span class="icon-wrapper icon-loop"><el-icon><RefreshLeft /></el-icon></span>
              <span class="node-label">{{ nodeLabel('loop') }}</span>
            </div>
          </div>
        </div>
        <div class="node-group">
          <div class="group-title">{{ t('flow.nodePanel.groupOther') }}</div>
          <div class="group-items">            
            <div class="node-card" @click="handleNodeClick('layout')">
              <span class="icon-wrapper icon-layout"><el-icon><CircleClose /></el-icon></span>
              <span class="node-label">{{ nodeLabel('layout') }}</span>
            </div>
            <div class="node-card" @click="handleNodeClick('comment')">
              <span class="icon-wrapper icon-comment"><el-icon><ChatLineSquare /></el-icon></span>
              <span class="node-label">{{ nodeLabel('comment') }}</span>
            </div>
          </div>
        </div>
      </div>
      <div v-show="activeTab === 'tool'" class="panel-section">
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChatLineSquare, Clock, Promotion, RefreshRight, Connection, Link, Help, MagicStick, Lightning, CircleClose, Minus, Upload, CircleCheck, ScaleToOriginal, Monitor, Mouse, Coin, RefreshLeft, Tools, Aim } from '@element-plus/icons-vue'
import { useEscClose } from '../../../composables/useEscClose'

const { t } = useI18n()

// 节点类型的显示名统一走语言包：键与 nodeIcons.js 的类型表一致
const nodeLabel = (type) => t(`flow.nodeType.${type}`)

const props = defineProps({
  visible: {
    type: Boolean,
    default: false
  },
  left: {
    type: String,
    default: '60px'
  }
})

const emit = defineEmits(['node-select', 'close'])

// 选择节点窗口没有关闭按钮（靠点外部收起），ESC 补一个同义出口
useEscClose(() => props.visible, () => emit('close'))

const activeTab = ref('node')

const panelStyle = {
  top: '2%',
  bottom: '2%',
  width: '300px'
}

const handleNodeClick = (nodeType) => {
  emit('node-select', nodeType)
}
</script>

<style scoped>
.node-panel {
  position: absolute;
  left: 60px;
  background-color: #ffffff;
  border-radius: 8px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  z-index: 1000;
}

.panel-header {
  display: flex;
  gap: 8px;
  padding: 1px 12px 0 12px;
  background-color: #f5f5f5;
}

.panel-tab {
  padding: 8px 16px;
  text-align: center;
  font-size: 14px;
  color: #666;
  cursor: pointer;
  transition: all 0.2s;
  border-radius: 12px 12px 0 0;
}

.panel-tab:hover {
  background-color: rgba(255, 255, 255, 0.5);
}

.panel-tab.active {
  background-color: #ffffff;
  color: #1890ff;
  font-weight: 600;
  box-shadow: 0 -2px 8px rgba(0, 0, 0, 0.08);
}

.panel-content {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 12px;
}

.panel-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.node-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.group-title {
  font-size: 12px;
  font-weight: 600;
  color: #aaa;
  padding-left: 3px;
}

.group-items {
  display: grid !important;
  grid-template-columns: repeat(5, 1fr) !important;
  grid-auto-flow: row !important;
  gap: 6px !important;
  width: 100% !important;
}

.node-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0px;
  padding: 6px 2px;
  background-color: #fafafa;
  border-radius: 10px;
  cursor: pointer;
  transition: all 0.2s;
  border: 1px solid transparent;
}

.node-card:hover {
  background-color: #e6f0f8;
  border-color: transparent;
  transform: translateY(-2px);
}

.icon-wrapper {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 8px;
}

.icon-wrapper .el-icon {
  font-size: 16px;
}

.node-label {
  font-size: 11px;
  color: #333;
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
  margin-top: 3px;
}

.node-card:hover .node-label {
  color: #1890ff;
  font-weight: 500;
}

.icon-llm { color: #ffffff; background-color: #409eff; }
.icon-memory { color: #ffffff; background-color: #673ab7; }
.icon-result { color: #ffffff; background-color: #67c23a; }
.icon-progress { color: #ffffff; background-color: #eea236; }
.icon-agent { color: #ffffff; background-color: #f56c6c; }
.icon-branch { color: #ffffff; background-color: #00bcd4; }
.icon-intent { color: #ffffff; background-color: #9c27b0; }
.icon-wait { color: #ffffff; background-color: #00bcd4; }
.icon-break { color: #ffffff; background-color: #ff9800; }
.icon-continue { color: #ffffff; background-color: #4caf50; }
.icon-return { color: #ffffff; background-color: #9c27b0; }
.icon-exit { color: #ffffff; background-color: #f56565; }
.icon-process { color: #ffffff; background-color: #00bcd4; }
.icon-function { color: #ffffff; background-color: #667eea; }
.icon-fork { color: #ffffff; background-color: #4bc0c0; }
.icon-call { color: #ffffff; background-color: #ffc107; }
.icon-assign { color: #ffffff; background-color: #ff9800; }
.icon-loop { color: #ffffff; background-color: #7b1fa2; }
.icon-subflow { color: #ffffff; background-color: #667eea; }
.icon-code { color: #ffffff; background-color: #f56565; }
.icon-http { color: #ffffff; background-color: #409eff; }
.icon-mcp { color: #ffffff; background-color: #e6a23c; }
.icon-rpc { color: #ffffff; background-color: #1890ff; }
.icon-layout { color: #ffffff; background-color: #909399; }
.icon-comment { color: #ffffff; background-color: #409eff; }
</style>

<!-- 与 PropsPanel 同一套口径：滚动条常驻占位但透明，鼠标进面板才显形（非 scoped，
     因为滚动容器就是本组件的 .panel-content，这里保持一致以免两处规则漂移） -->
<style>
.node-panel * {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.node-panel *:hover {
  scrollbar-color: #c1c1c1 transparent;
}
.node-panel *::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.node-panel *::-webkit-scrollbar-track {
  background: transparent;
}
.node-panel *::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.node-panel *:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}
</style>