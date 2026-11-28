<template>
  <!-- Shared nudge for a list that is empty or holds nothing of the tenant's own: one definition line, then the
       numbered ways to get one. The number on each card comes from the index, so no locale has to carry "1" / "2",
       and the caller's button is rendered inside the last card — "click the button below" and the button itself can
       never drift apart. -->
  <div class="eg">
    <div class="eg-title">{{ title }}</div>
    <div class="eg-lead">{{ lead }}</div>
    <div v-for="(step, i) in steps" :key="i" class="eg-step" :data-step="i + 1">
      {{ step }}
      <div v-if="i === steps.length - 1" class="eg-actions">
        <slot name="action" />
      </div>
    </div>
  </div>
</template>

<script setup>
defineProps({
  title: { type: String, required: true },
  lead: { type: String, required: true },
  // One sentence per card, in reading order.
  steps: { type: Array, default: () => [] }
})
</script>

<style scoped>
.eg {
  text-align: left;
}

.eg-title {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 8px;
}

/* The definition sits one step darker and a size up: it answers "what is this", the cards answer "what do I do" */
.eg-lead {
  margin-bottom: 16px;
  font-size: 13px;
  line-height: 1.8;
  color: #606266;
}

.eg-step {
  position: relative;
  padding: 12px 14px 12px 42px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #fff;
  box-shadow: 0 1px 2px rgba(15, 23, 42, 0.04);
  text-align: left;
  font-size: 12px;
  line-height: 1.8;
  color: #909399;
}

.eg-step + .eg-step {
  margin-top: 10px;
}

.eg-step::before {
  content: attr(data-step);
  position: absolute;
  top: 12px;
  left: 14px;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: #ecf5ff;
  color: #409eff;
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
  text-align: center;
}

.eg-actions {
  margin-top: 10px;
}
</style>
