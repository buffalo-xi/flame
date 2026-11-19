<template>
  <div class="result-view">
    <!-- 只有内容"看起来像 Markdown"时才给切换：纯文本 / JSON 结果多一个无意义的开关只是噪音 -->
    <div v-if="markdownCapable" class="result-bar">
      <span class="result-switch">
        <span :class="{ active: mode === 'md' }" :title="t('flow.result.beautifyTitle')" @click="mode = 'md'">{{ t('flow.result.beautify') }}</span>
        <span :class="{ active: mode === 'raw' }" :title="t('flow.result.rawTitle')" @click="mode = 'raw'">{{ t('flow.result.raw') }}</span>
      </span>
    </div>
    <pre v-if="!markdownCapable || mode === 'raw'" class="result-pre" :class="variant">{{ plain }}</pre>
    <div v-else class="result-md" :class="variant" v-html="html"></div>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { renderMarkdown } from '@/tenant/utils/markdown.js'

const { t } = useI18n()

const props = defineProps({
  // 不用 String 限定：结果可能是对象（历史记录里未强类型化），统一在组件内转成文本
  text: { type: [String, Number, Object, Boolean], default: '' },
  // panel：运行/调试卡片里的小块（对齐原 .run-part-* 的观感）
  // detail：历史详情里的大块（对齐原 .hd-section pre 的观感）
  variant: { type: String, default: 'panel' }
})

// 缺省美化：结果里 agent 回复的 Markdown 占多数，默认直接渲染更省一次点击；
// 「原始」留给需要核对源码（表格对齐、转义字符）的场景。
const mode = ref('md')
const plain = computed(() => (props.text == null ? '' : String(props.text)))

// 是否像 Markdown：标题 / 列表 / 引用 / 表格 / 代码块或行内代码 / 加粗 / 链接。
// 不像 Markdown 的内容（纯文本 / JSON）始终保持原文：美化只会把它们包成段落，没有意义。
const MD_HINT = /(^|\n)[ \t]{0,3}(#{1,6}[ \t]|[-*+][ \t]|\d+\.[ \t]|>[ \t]?|\|.*\|)|```|`[^`\n]+`|\*\*[^*\n]+\*\*|\[[^\]\n]+\]\([^)\n]+\)/
const markdownCapable = computed(() => MD_HINT.test(plain.value))

const html = computed(() => renderMarkdown(plain.value))
</script>

<style scoped>
.result-view {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.result-bar {
  display: flex;
  justify-content: flex-end;
}
.result-switch {
  display: inline-flex;
  align-items: center;
  border: 1px solid #e4e7ed;
  border-radius: 3px;
  overflow: hidden;
}
.result-switch span {
  padding: 0 6px;
  font-size: 11px;
  line-height: 16px;
  color: #909399;
  background: #fff;
  cursor: pointer;
  user-select: none;
}
.result-switch span + span {
  border-left: 1px solid #e4e7ed;
}
.result-switch span.active {
  background: #ecf5ff;
  color: #1890ff;
}

.result-pre,
.result-md {
  margin: 0;
  border-radius: 4px;
  font-size: 12px;
  line-height: 1.5;
  color: #1f2329;
  overflow: auto;
  /* 默认铺开（约 300 行）：常见结果不该出现块内滚动条；
     超过这个量才在块内滚，避免一条超长输出把外层面板撑到没法用。 */
  max-height: calc(1.5em * 300);
}
.result-pre {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  white-space: pre-wrap;
  word-break: break-all;
}

/* 滚动条：位置常驻、拇指透明，鼠标进入本块才上色（与试运行面板同一口径） */
.result-pre,
.result-md {
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}
.result-pre::-webkit-scrollbar,
.result-md::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}
.result-pre::-webkit-scrollbar-track,
.result-md::-webkit-scrollbar-track {
  background: transparent;
}
.result-pre::-webkit-scrollbar-thumb,
.result-md::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 4px;
}
.result-view:hover .result-pre,
.result-view:hover .result-md {
  scrollbar-color: rgba(144, 147, 153, 0.45) transparent;
}
.result-view:hover .result-pre::-webkit-scrollbar-thumb,
.result-view:hover .result-md::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.45);
}

/* 运行/调试卡片：观感与原 .run-part-final / .run-part-result 一致 */
.result-pre.panel {
  background: #fff;
  padding: 6px 8px;
}
.result-md.panel {
  background: #fff;
  padding: 6px 8px;
  white-space: normal;
  word-break: break-word;
}

/* 历史详情：观感与原 .hd-section pre 一致 */
.result-pre.detail {
  background: #fafbfc;
  border: 1px solid #e6e8eb;
  padding: 10px;
}
.result-md.detail {
  background: #fafbfc;
  border: 1px solid #e6e8eb;
  padding: 10px;
  white-space: normal;
  word-break: break-word;
}

/* Markdown 正文：v-html 注入的元素没有 scoped 属性，子元素样式必须走 :deep() */
.result-md :deep(p) {
  margin: 0 0 8px;
}
.result-md :deep(p:last-child) {
  margin-bottom: 0;
}
.result-md :deep(h1),
.result-md :deep(h2),
.result-md :deep(h3),
.result-md :deep(h4),
.result-md :deep(h5),
.result-md :deep(h6) {
  margin: 12px 0 8px;
  line-height: 1.35;
  font-weight: 600;
}
.result-md :deep(h1) { font-size: 18px; }
.result-md :deep(h2) { font-size: 16px; }
.result-md :deep(h3) { font-size: 14px; }
.result-md :deep(h4),
.result-md :deep(h5),
.result-md :deep(h6) { font-size: 13px; }
.result-md :deep(ul),
.result-md :deep(ol) {
  margin: 0 0 8px;
  padding-left: 20px;
}
.result-md :deep(li) { margin: 2px 0; }
.result-md :deep(a) { color: #1890ff; text-decoration: none; }
.result-md :deep(a:hover) { text-decoration: underline; }
.result-md :deep(code) {
  font-family: Consolas, Monaco, 'Courier New', monospace;
  font-size: 11.5px;
  background: #f2f3f5;
  border-radius: 3px;
  padding: 1px 4px;
}
.result-md :deep(pre) {
  margin: 0 0 8px;
  padding: 8px 10px;
  background: #f6f7f9;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  overflow-x: auto;
}
.result-md :deep(pre code) {
  background: transparent;
  padding: 0;
}
.result-md :deep(blockquote) {
  margin: 0 0 8px;
  padding: 2px 0 2px 8px;
  border-left: 3px solid #dcdfe6;
  color: #606266;
}
.result-md :deep(table) {
  border-collapse: collapse;
  margin: 0 0 8px;
  font-size: 12px;
}
.result-md :deep(th),
.result-md :deep(td) {
  border: 1px solid #e4e7ed;
  padding: 3px 6px;
}
.result-md :deep(th) {
  background: #fafafa;
  font-weight: 600;
}
.result-md :deep(hr) {
  border: none;
  border-top: 1px solid #e8e8e8;
  margin: 10px 0;
}
.result-md :deep(img) {
  max-width: 100%;
  border-radius: 4px;
}
</style>
