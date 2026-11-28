<template>
  <el-tooltip v-if="isMember" placement="top" :show-after="150">
    <template #content>{{ t('flow.nodeProps.concurrentMember') }}</template>
    <svg class="concurrent-badge" viewBox="0 0 16 16" aria-hidden="true"
         @mousedown.stop @pointerdown.stop>
      <g fill="none" stroke-width="1.6" stroke-linecap="round">
        <path class="bar-strong" d="M1.5 4.5h13" />
        <path class="bar-soft" d="M1.5 11.5h13" />
      </g>
      <circle class="dot" cx="5.5" cy="8" r="1.2" />
      <circle class="dot" cx="10.5" cy="8" r="1.2" />
    </svg>
  </el-tooltip>
</template>

<script setup>
import { computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps({
  id: {
    type: String,
    required: true
  }
})

const { t } = useI18n()

// Membership is a node structure attribute (data.concurrent), so read it from the canvas node list instead of threading a
// prop through every node component: the call site stays <NodeBadges :id="id" />.
const flowNodes = inject('FlowNodes', null)
const isMember = computed(() => {
  const node = (flowNodes?.value || []).find(n => n.id === props.id)
  return !!node?.data?.concurrent
})
</script>

<style scoped>
/* Two long parallel bars with two grey dots between them: a single blue hue stays inside the language (hue means node type
   on this canvas - blue is http / rpc), the second tone keeps the pair from reading as an equals sign, and the neutral dots
   read as "two of something" without adding a third accent colour. The icon is nudged down 1px because the header centers it
   against a bold 11px label, which leaves it optically high. */
.concurrent-badge {
  flex: none;
  position: relative;
  top: 1px;
  width: 12px;
  height: 12px;
  cursor: default;
}

.bar-strong {
  stroke: #409eff;
}

.bar-soft {
  stroke: #66b1ff;
}

.dot {
  fill: #909399;
  stroke: none;
}
</style>
