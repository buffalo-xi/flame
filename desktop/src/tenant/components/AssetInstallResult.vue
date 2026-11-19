<template>
  <!-- 装完之后的结果页：市场安装与上传导入共用。
       刻意不自动关窗 —— warnings / 跳过文件必须让人看完再走，一闪而过的提示等于没有。 -->
  <div class="install-result">
    <el-alert type="success" :closable="false" show-icon :title="doneTitle" />
    <div v-if="published" class="result-sub">{{ t('assetResult.published', { version: published }) }}</div>

    <div v-if="warnings.length" class="result-block">
      <div class="result-title">{{ t('assetResult.warnings', { n: warnings.length }) }}</div>
      <ul class="result-list">
        <li v-for="(w, i) in warnings" :key="i">{{ w }}</li>
      </ul>
    </div>

    <div v-if="skipped.length" class="result-block">
      <div class="result-title">{{ t('assetResult.skipped', { n: skipped.length }) }}</div>
      <ul class="result-list">
        <li v-for="(f, i) in skipped" :key="i">{{ f }}</li>
      </ul>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps({
  name: { type: String, default: '' },
  overwritten: { type: Boolean, default: false },
  warnings: { type: Array, default: () => [] },
  skipped: { type: Array, default: () => [] },
  published: { type: [String, Number], default: null },
  // 动作：import / install —— 结果行按动作分句，中英文各自组织句式
  verb: { type: String, default: 'install' }
})

const { t } = useI18n()

const doneTitle = computed(() => {
  if (props.overwritten) return t('assetResult.overwritten', { name: props.name })
  const key = props.verb === 'import' ? 'imported' : 'installed'
  return t(`assetResult.${key}`, { name: props.name })
})
</script>

<style scoped>
.install-result {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 340px;
  overflow-y: auto;
}
.result-sub {
  font-size: 12px;
  color: #67c23a;
}
.result-title {
  font-size: 12px;
  font-weight: 600;
  color: #606266;
  margin-bottom: 4px;
}
.result-list {
  margin: 0;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.7;
  color: #8a6d3b;
}
.result-block {
  background: #fdf6ec;
  border-radius: 4px;
  padding: 6px 8px;
}
</style>
