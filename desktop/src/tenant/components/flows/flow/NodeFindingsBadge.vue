<template>
  <el-tooltip v-if="hasFindings" placement="top" :show-after="150">
    <template #content>
      <div class="finding-tip">
        <div v-for="(f, i) in findings" :key="i" class="finding-tip-row">
          <span class="finding-tip-sev" :class="f.severity">{{ t('flow.check.severity.' + f.severity) }}</span>
          <span class="finding-tip-text">
            {{ textOf(f) }}
            <span v-if="f.path" class="finding-tip-path">{{ f.path }}</span>
          </span>
        </div>
        <div class="finding-tip-foot">{{ t('flow.check.badge.focus') }}</div>
      </div>
    </template>
    <span class="finding-badge" :class="hasError ? 'is-error' : 'is-warning'"
          @click.stop="focus" @mousedown.stop @pointerdown.stop>!</span>
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

const { t, te } = useI18n()

// Both are injected by Flow.vue: the findings map is keyed by node id, and focusing means "select it and bring it into
// view" - the same move the session jump uses.
const flowFindings = inject('flowFindings', null)
const focusNode = inject('focusNode', null)

const bucket = computed(() => (flowFindings?.value || {})[props.id] || null)
// Errors first: the worst thing about a node is what the tooltip should lead with.
const findings = computed(() => (bucket.value ? [...bucket.value.error, ...bucket.value.warning] : []))
const hasFindings = computed(() => findings.value.length > 0)
const hasError = computed(() => !!bucket.value?.error.length)

// A code this bundle does not know yet (a newer backend) still has to say something rather than render a raw key.
const textOf = (f) => (te(`flow.check.code.${f.code}`)
  ? t(`flow.check.code.${f.code}`)
  : t('flow.check.code.unknown'))

const focus = () => {
  if (focusNode) focusNode(props.id)
}
</script>

<style scoped>
.finding-badge {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 11px;
  height: 11px;
  border-radius: 50%;
  color: #fff;
  font-size: 8px;
  font-weight: 700;
  line-height: 1;
  cursor: pointer;
}

.finding-badge.is-error {
  background: #f56c6c;
}

.finding-badge.is-warning {
  background: #e6a23c;
}

.finding-tip {
  max-width: 280px;
  line-height: 1.5;
}

.finding-tip-row {
  display: flex;
  gap: 6px;
  margin-top: 4px;
  font-size: 12px;
}

.finding-tip-sev {
  flex: none;
  color: #e6a23c;
}

.finding-tip-sev.error {
  color: #ff7875;
}

.finding-tip-path {
  display: block;
  margin-top: 2px;
  font-size: 11px;
  color: #c0c4cc;
}

.finding-tip-foot {
  margin-top: 8px;
  font-size: 11px;
  color: #c0c4cc;
}
</style>
