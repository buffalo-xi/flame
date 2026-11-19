<template>
  <div class="capabilities">
    <!-- 头部：三个主 tab（工具目录 = 入向能力 / 插件 / 自定义工具）。
         端点已独立成页（Endpoints.vue）——那是我们对外提供的服务，与「agent 能用什么」不是一回事。 -->
    <div class="cap-header">
      <div class="cap-tabs">
        <button class="cap-tab" :class="{ active: tab === 'tools' }" @click="tab = 'tools'">
          <el-icon><Tools /></el-icon>
          {{ t('capabilities.tabTools') }}
          <span class="cap-count">({{ toolsCount }})</span>
        </button>
        <button class="cap-tab" :class="{ active: tab === 'plugins' }" @click="tab = 'plugins'">
          <el-icon><Grid /></el-icon>
          {{ t('capabilities.tabPlugins') }}
          <span class="cap-count">({{ pluginsCount }})</span>
        </button>
        <button class="cap-tab" :class="{ active: tab === 'custom' }" @click="tab = 'custom'">
          <el-icon><Document /></el-icon>
          {{ t('capabilities.tabCustom') }}
          <span class="cap-count">({{ customCount }})</span>
        </button>
      </div>
      <div class="cap-hint">
        <strong>{{ t('capabilities.hintTools') }}</strong>{{ t('capabilities.hintToolsDesc') }}
        <strong>{{ t('capabilities.hintPlugins') }}</strong>{{ t('capabilities.hintPluginsDesc') }}
        <strong>{{ t('capabilities.hintCustom') }}</strong>{{ t('capabilities.hintCustomDesc') }}
      </div>
    </div>

    <div class="cap-body">
      <ToolsTab v-show="tab === 'tools'" @count="toolsCount = $event" @nav="$emit('nav', $event)" />
      <PluginsTab v-show="tab === 'plugins'" @count="pluginsCount = $event" />
      <CustomToolsTab v-show="tab === 'custom'" :focus="customFocus" @count="customCount = $event" />
    </div>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Tools, Grid, Document } from '@element-plus/icons-vue'
import ToolsTab from './capabilities/ToolsTab.vue'
import PluginsTab from './capabilities/PluginsTab.vue'
import CustomToolsTab from './capabilities/CustomToolsTab.vue'

// 转发子页面的跳转请求给 MainWindow：
//   nav 字符串（如 'settings'）→ 只切页
// 端点页（Endpoints.vue）自带 open 事件，不再经过这里。
defineEmits(['nav'])

const { t } = useI18n()

// 会话里建 / 改了自定义工具：切到「自定义工具」页签，并把定位目标透传给子组件。
// 转成新对象（含 ts）再传：同一工具重复点也能让子组件的 watch 触发。
const props = defineProps({ focus: { type: Object, default: null } })
const customFocus = ref(null)
watch(() => props.focus, (f) => {
  if (!f || f.nav !== 'capabilities' || !(f.name || f.id)) return
  tab.value = 'custom'
  customFocus.value = { nav: f.nav, id: f.id || '', name: f.name || '', ts: f.ts || Date.now() }
})

const tab = ref('tools')
// 计数由子组件上报（加载完成前显示 —）
const toolsCount = ref('—')
const pluginsCount = ref('—')
const customCount = ref('—')
</script>

<style scoped>
.capabilities {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  /* 与智能体页（.agents）保持一致：页面级 padding 20px，头部再留 20px 下边距 */
  padding: 20px;
  background: #fafafa;
  overflow: hidden;
}

/* 头部：仿照智能体列表页头部（.agents-header）—— 无底色、无分隔线、无自身留白
   （留白由页面 .capabilities 的 padding 20px 提供），下方留 20px 与内容分隔。
   margin-top 4px 是视觉微调：两边几何逐项核对过（页面 padding 20px、头部 padding 0 /
   margin-bottom 20px、药丸 32px + line-height 1 + font-weight 500 与 EP 按钮一致），
   但并排看这一条仍略靠上，补 4px 压齐；改 0 即回到纯几何对齐。 */
.cap-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 2px;
  margin-bottom: 20px;
  padding: 0;
  box-sizing: border-box;
  background: transparent;
  flex-shrink: 0;
}

.cap-tabs {
  display: flex;
  align-items: center;
  gap: 4px; /* 与智能体 .status-filters 一致：按钮紧挨成组，间距 4px */
}

/* tab = 智能体头部的筛选按钮（全部 / 启用 / 禁止）同款药丸：
   10px 圆角、2px 白色实边、字色 #666，hover #e6e6e6、选中 #e0e0e0；
   字体/高度也对齐 Element Plus 默认按钮（14px / 500 / 定高 32px） */
.cap-tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 6px 16px;
  font-size: 14px;
  font-weight: 500; /* EP --el-font-weight-primary */
  line-height: 1;
  white-space: nowrap;
  color: #666;
  background: none;
  border: 2px solid #fff;
  border-radius: 10px;
  cursor: pointer;
  transition: all 0.3s ease;
}

.cap-tab:hover {
  background: #e6e6e6;
  color: #333;
}

.cap-tab.active {
  background: #e0e0e0;
  color: #333;
}

.cap-count {
  /* 与智能体头部一样：计数跟按钮同字号（智能体是「全部 (3)」内联在同一个 span 里） */
  font-size: inherit;
  opacity: 0.75;
}

.cap-hint {
  font-size: 12px;
  color: #909399;
  min-width: 0; /* 允许在 flex 行内收缩 */
  /* 宽度不足时最多占两行，多余隐藏（避免把头部撑高） */
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  overflow: hidden;
}

/* 各 tab 自己决定滚动方式：工具 tab 整列滚；端点 tab 固定表头 + 表格内部滚动，
   横向滚动条因此常驻在表格底部（不会被页面级滚动推到屏幕外） */
.cap-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  padding: 0; /* 留白统一由 .capabilities 的 padding 提供（同智能体内容区） */
}
</style>
