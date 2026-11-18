<template>
  <!-- 变量清单：每行一个「名字 + 值」，对象/数组缩进 JSON，字符串原样（不多包一层引号）。
       节点「执行结果」的输入 / 输出变量、历史运行的输入共用这一套，样式只留一份。
       rows 里的 text 已经格式化好，组件只负责排版，不解析值 -->
  <div class="ov-list">
    <div v-for="row in rows" :key="row.name" class="ov-row" :class="{ 'ov-row--empty': row.empty }">
      <div class="ov-head">
        <span class="ov-name">{{ row.name }}</span>
        <span v-if="row.type" class="ov-type">{{ row.type }}</span>
        <span v-if="row.desc" class="ov-desc" :title="row.desc">{{ row.desc }}</span>
      </div>
      <pre class="ov-value">{{ row.text }}</pre>
    </div>
  </div>
</template>

<script setup>
defineProps({
  // [{ name, text, type?, desc?, empty? }]
  rows: { type: Array, default: () => [] }
})
</script>

<style scoped>
.ov-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ov-row {
  padding: 8px 10px;
  background: #fff;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
}
/* 声明了却没赋值：虚线 + 灰字，与"有值"在视觉上分开 */
.ov-row--empty {
  background: #fafafa;
  border-style: dashed;
}
.ov-head {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  margin-bottom: 4px;
}
.ov-name {
  font-family: 'SFMono-Regular', Consolas, monospace;
  font-size: 12px;
  font-weight: 600;
  color: #303133;
}
.ov-type {
  padding: 0 5px;
  font-size: 11px;
  color: #909399;
  background: #f2f3f5;
  border-radius: 3px;
  flex-shrink: 0;
}
.ov-desc {
  font-size: 11px;
  color: #a8abb2;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ov-value {
  margin: 0;
  max-height: 200px;
  overflow: auto;
  font-size: 12px;
  line-height: 1.5;
  color: #303133;
  white-space: pre-wrap;
  word-break: break-all;
}
.ov-row--empty .ov-value {
  color: #c0c4cc;
  font-style: italic;
}
</style>
