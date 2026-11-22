<template>
  <!-- 系统设置：左 = 域导航（220px 白卡，点一下跳到右边对应卡片并展开）；
       右 = 所有域的卡片纵向排列，每张都能收起 -->
  <div class="section">
    <div class="split">
      <aside class="sys-nav">
        <!-- 左卡头：这一页没有页内标题，给它一行「系统设置」+ 页面级帮助。
             单独一个 ? 会占掉一整行且没有落点，和「插件与能力」左卡的 caption 同款才不突兀 -->
        <div class="sys-caption">
          <span>{{ t('platform.app.navSystem') }}</span>
          <HelpButton topic="page-platform-system" />
        </div>
        <div
          v-for="d in DOMAINS"
          :key="d.key"
          class="nav-item"
          :class="{ active: activeDomain === d.key }"
          @click="goto(d.key)"
        >
          <div class="nav-title">{{ d.label }}</div>
          <div class="nav-desc">{{ d.desc }}</div>
        </div>
      </aside>

      <section class="sys-body">
        <div v-for="d in DOMAINS" :key="d.key" :ref="(el) => (cardEls[d.key] = el)" class="blk">
          <div class="blk-head">
            <button type="button" class="blk-toggle" @click="toggle(d.key)">
              <span class="blk-title">{{ d.label }}</span>
              <span class="blk-hint">{{ d.desc }}</span>
              <el-icon class="blk-arrow" :class="{ open: openDomain[d.key] }"><ArrowRight /></el-icon>
            </button>
          </div>

          <div v-show="openDomain[d.key]" class="blk-body">
            <component :is="d.component" />
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRight } from '@element-plus/icons-vue'
import SysFlowConfigsCard from './system/SysFlowConfigsCard.vue'
import SysAgentConfigsCard from './system/SysAgentConfigsCard.vue'
import SysQuotaCard from './system/SysQuotaCard.vue'
import SysLlmPresetsCard from './system/SysLlmPresetsCard.vue'
import HelpButton from '../../help/HelpButton.vue'

const { t } = useI18n()

// 域清单：新增一个域 = 这里加一行（带组件）+ 写一个域组件（后端加一对 /platform/system/* 接口），互不影响。
// 域组件自带加载/校验/保存，卡片头只提供折叠与跳转。
// desc 会同时出现在左导航和卡片头，是这张卡唯一的「它管什么」标识 ——
// 三张硬顶卡写同一句「所有租户的硬顶与兜底」等于没有标识，所以各写各的对象。
// 键名单独列出：activeDomain / openDomain 的初值不该跟着语言包重新计算。
const DOMAIN_KEYS = ['quota', 'flowconfigs', 'agentconfigs', 'llm-presets']
const DOMAINS = computed(() => [
  { key: 'quota', label: t('platform.system.nav.quota'), desc: t('platform.system.nav.quotaDesc'), component: SysQuotaCard },
  { key: 'flowconfigs', label: t('platform.system.nav.flowConfigs'), desc: t('platform.system.nav.flowConfigsDesc'), component: SysFlowConfigsCard },
  { key: 'agentconfigs', label: t('platform.system.nav.agentConfigs'), desc: t('platform.system.nav.agentConfigsDesc'), component: SysAgentConfigsCard },
  { key: 'llm-presets', label: t('platform.system.nav.llmPresets'), desc: t('platform.system.nav.llmPresetsDesc'), component: SysLlmPresetsCard }
])

const activeDomain = ref(DOMAIN_KEYS[0])

// 每张折叠卡的展开状态（默认都展开）
const openDomain = ref(Object.fromEntries(DOMAIN_KEYS.map((k) => [k, true])))

// 卡片 DOM：左导航点击后滚过去用（保存按钮在各域卡片内部，不做 ref 转发）
const cardEls = {}

function toggle(key) {
  openDomain.value[key] = !openDomain.value[key]
}

// 左导航点击：选中高亮 → 收起则展开 → 平滑滚到该卡
function goto(key) {
  activeDomain.value = key
  openDomain.value[key] = true
  nextTick(() => {
    const el = cardEls[key]
    if (el && el.scrollIntoView) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
  })
}
</script>

<style scoped>
/* ===== 三层灰阶（与租户管理 / 平台用户同款）=====
   大背景：#f0f2f5（与平台外壳同色，整页一块浅灰）
   两张灰卡：.sys-nav / .sys-body（#fafafa）
   域卡片：.blk 白底 + 边框 */
.section {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  box-sizing: border-box;
  overflow: hidden;
  background: #f0f2f5;
  border-radius: 12px;
}

.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* 宽度与「插件与能力」的左卡对齐（260px），两页切换时左栏不跳 */
.sys-nav {
  width: 260px;
  flex-shrink: 0;
  padding: 8px;
  box-sizing: border-box;
  overflow: auto;
  background: #fafafa;
  border-radius: 12px;
}

/* 卡片滚动条：位置常驻（一直是 6px），只是拇指透明 → 悬浮才上色。
   这样悬浮时不会因为"条突然出现"而让内容横向跳动。 */
.sys-nav {
  scrollbar-width: thin; /* 常驻细条：占位固定 */
  scrollbar-color: transparent transparent; /* 不悬浮：看不见，但占位 */
}

.sys-nav::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.sys-nav::-webkit-scrollbar-track {
  background: transparent;
}

.sys-nav::-webkit-scrollbar-thumb {
  background: transparent; /* 不悬浮：看不见，但占位 */
  border-radius: 3px;
}

.sys-nav:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.sys-nav:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

/* 左卡头：域导航的标题行，右侧挂页面级帮助（同「插件与能力」的 .nav-caption-row）。
   标题用顶栏那个 tab 名，避免再编一个新词 */
.sys-caption {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 2px 6px 6px;
  font-size: 12px;
  color: #909399;
}

.nav-item {
  padding: 10px 12px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s;
}

/* 灰卡上的悬停底色要压深一档，否则和卡底色 (#fafafa) 几乎看不出来 */
.nav-item:hover {
  background: #f0f2f5;
}

.nav-item.active {
  background: #ecf5ff;
}

.nav-title {
  font-size: 14px;
  color: #303133;
}

.nav-item.active .nav-title {
  color: #1890ff;
  font-weight: 600;
}

.nav-desc {
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
}

.sys-body {
  flex: 1;
  min-width: 0;
  overflow: auto;
  padding: 10px 12px;
  box-sizing: border-box;
  background: #fafafa;
  border-radius: 12px;
}

/* 卡片滚动条：位置常驻（一直是 6px），只是拇指透明 → 悬浮才上色。
   这样悬浮时不会因为"条突然出现"而让内容横向跳动。 */
.sys-body {
  scrollbar-width: thin; /* 常驻细条：占位固定 */
  scrollbar-color: transparent transparent; /* 不悬浮：看不见，但占位 */
}

.sys-body::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.sys-body::-webkit-scrollbar-track {
  background: transparent;
}

.sys-body::-webkit-scrollbar-thumb {
  background: transparent; /* 不悬浮：看不见，但占位 */
  border-radius: 3px;
}

.sys-body:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.sys-body:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

/* ===== 域卡片（与租户详情卡同款折叠壳，灰卡上要靠白底立起来） ===== */
.blk {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  overflow: hidden;
}

.blk + .blk {
  margin-top: 12px;
}

.blk-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px 6px 0;
  background: #fafafa;
}

/* 整条标题栏都可点（只有箭头与文字，没有别的东西会误触） */
.blk-toggle {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0 6px 12px;
  background: none;
  border: none;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}

/* 雅黑只有 400/700 两档，中间值取不到：标题靠"深墨色 + 大一号"立层次，不靠字重 */
.blk-title {
  font-size: 14px;
  font-weight: 600;
  color: #505762;
}

.blk-hint {
  font-size: 12px;
  color: #a8abb2;
}

.blk-arrow {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
  transition: transform 0.2s;
}

.blk-arrow.open {
  transform: rotate(90deg);
}

.blk-body {
  padding: 12px;
  border-top: 1px solid #f0f2f5;
}
</style>
