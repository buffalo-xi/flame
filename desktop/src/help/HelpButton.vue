<template>
  <!-- Per-screen help entry: the "?" that sits next to a screen title and opens that screen's topic.
       Self-contained on purpose — the page does not need to own a ref or a drawer instance, it only
       names a topic. The help center (HelpIndex.vue) reads the same topics registry, so the two
       entry points can never disagree about what a topic says. -->
  <el-icon
    class="hp-dot"
    role="button"
    tabindex="0"
    :title="t('help.open')"
    :aria-label="t('help.open')"
    @click="visible = true"
    @keydown.enter.prevent="visible = true"
    @keydown.space.prevent="visible = true"
  >
    <QuestionFilled />
  </el-icon>

  <HelpDrawer :visible="visible" :title="title" :anchor-el="anchorEl" @close="visible = false">
    <div v-if="body" class="hp-doc" v-html="body" />
    <p v-else class="hp-missing">{{ t('help.missing') }}</p>
  </HelpDrawer>
</template>

<script setup>
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { QuestionFilled } from '@element-plus/icons-vue'
import HelpDrawer from './HelpDrawer.vue'
import { helpBody, helpTitle } from './topics.js'
import { renderMarkdown } from '../utils/markdown.js'

const props = defineProps({
  // Topic slug = the Markdown file name under src/help/<locale>/
  topic: { type: String, required: true },
  // Container the drawer docks to; null docks to the viewport
  anchorEl: { type: Object, default: null },
})

const { t, locale } = useI18n()

const visible = ref(false)
const raw = computed(() => helpBody(locale.value, props.topic))
const body = computed(() => renderMarkdown(raw.value))
const title = computed(() => helpTitle(locale.value, props.topic))
</script>

<style scoped>
.hp-dot {
  font-size: 14px;
  color: #909399;
  cursor: pointer;
  flex-shrink: 0;
  vertical-align: middle;
}

.hp-dot:hover,
.hp-dot:focus-visible {
  color: #1890ff;
  outline: none;
}

.hp-missing {
  margin: 0;
  font-size: 13px;
  color: #909399;
}
</style>
