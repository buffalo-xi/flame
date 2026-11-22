<template>
  <!-- Top-bar help entry: opens the help center (topic list), then one topic in place.
       Staying in one drawer instead of stacking a second one keeps ESC and click-outside simple:
       the list is the drawer's root, a topic replaces the list body and a "back" link returns.

       The entry cannot borrow the host's button class (.logout-button / .top-btn): this component has
       two root nodes (the button and the teleported drawer), and Vue does not apply a parent's scoped
       attribute to a fragment, so those rules would silently never match — which is exactly how the
       default UA button styling (border, padding) ended up on screen. Style it here instead. -->
  <button
    type="button"
    class="hp-top-btn"
    :title="t('help.indexTitle')"
    :aria-label="t('help.indexTitle')"
    @click="visible = true"
  >
    <el-icon><QuestionFilled /></el-icon>
  </button>

  <HelpDrawer
    :visible="visible"
    :title="drawerTitle"
    :anchor-el="anchorEl"
    :back="!!topic"
    :back-label="t('help.back')"
    @back="topic = ''"
    @close="close"
  >
    <div v-if="topic" :key="topic" class="hp-doc" v-html="body" />

    <div v-else class="hp-index">
      <el-input
        v-model="q"
        size="small"
        clearable
        :placeholder="t('help.searchPlaceholder')"
        class="hp-search"
      />
      <div v-for="g in groups" :key="g.key" class="hp-group">
        <div class="hp-group-title">{{ g.label }}</div>
        <ul>
          <li v-for="item in g.items" :key="item.topic" @click="topic = item.topic">
            {{ item.title }}
          </li>
        </ul>
      </div>
      <p v-if="!groups.length" class="hp-muted">{{ t('help.empty') }}</p>
    </div>
  </HelpDrawer>
</template>

<script setup>
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { QuestionFilled } from '@element-plus/icons-vue'
import HelpDrawer from './HelpDrawer.vue'
import { groupsFor, helpBody, helpTitle } from './topics.js'
import { renderMarkdown } from '../utils/markdown.js'

const props = defineProps({
  // Which console is asking: 'tenant' or 'platform'. Only that side's screens are listed — a tenant
  // cannot act on "Platform users", so showing it would just be noise. Concept groups appear in both.
  scope: { type: String, default: 'tenant' },
  // Container the drawer docks to; null docks to the viewport
  anchorEl: { type: Object, default: null },
})

const { t, locale } = useI18n()

const visible = ref(false)
// '' = the list; otherwise the slug of the topic being read
const topic = ref('')
const q = ref('')

const body = computed(() => renderMarkdown(helpBody(locale.value, topic.value)))
const drawerTitle = computed(() => (topic.value ? helpTitle(locale.value, topic.value) : t('help.indexTitle')))

// Titles are filtered on the client: the whole catalog is a dozen topics, so an index lookup would
// cost more than it buys.
const groups = computed(() => {
  const needle = q.value.trim().toLowerCase()
  return groupsFor(props.scope).map((g) => ({
    key: g.key,
    label: t(g.label),
    items: g.topics
      .map((tp) => ({ topic: tp, title: helpTitle(locale.value, tp) }))
      .filter((it) => !needle || it.title.toLowerCase().includes(needle)),
  })).filter((g) => g.items.length)
})

// Closing resets to the list: reopening should show the catalog, not the last topic left behind
const close = () => {
  visible.value = false
  topic.value = ''
  q.value = ''
}
</script>

<style scoped>
/* Same geometry and colours as the top-bar buttons of both shells (.logout-button / .top-btn):
   colour #666, padding 6px, radius 4px, hover #1890ff on #e6f0f8. line-height 1.5 keeps the icon-only
   button exactly as tall as its neighbours, whose text line box is what sets their height. */
.hp-top-btn {
  background: none;
  border: none;
  border-radius: 4px;
  padding: 6px;
  color: #666;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  line-height: 1.5;
  transition: all 0.3s ease;
}

.hp-top-btn .el-icon {
  font-size: 14px;
  display: flex;
  align-items: center;
}

.hp-top-btn:hover,
.hp-top-btn:focus-visible {
  color: #1890ff;
  background-color: #e6f0f8;
  outline: none;
}

.hp-index {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.hp-search {
  margin-bottom: 2px;
}

.hp-group-title {
  font-size: 12px;
  font-weight: 600;
  color: #909399;
  letter-spacing: 0.04em;
  margin-bottom: 4px;
}

.hp-group ul {
  margin: 0;
  padding: 0;
  list-style: none;
}

.hp-group li {
  padding: 5px 8px;
  border-radius: 4px;
  font-size: 13px;
  color: #303133;
  cursor: pointer;
  line-height: 1.5;
}

.hp-group li:hover {
  background: #f5f7fa;
  color: #1890ff;
}

.hp-muted {
  margin: 0;
  font-size: 13px;
  color: #909399;
}
</style>
