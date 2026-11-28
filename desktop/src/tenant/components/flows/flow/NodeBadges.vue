<template>
  <NodeConcurrentBadge :id="id" />
  <NodeFindingsBadge :id="id" />
</template>

<script setup>
import NodeConcurrentBadge from './NodeConcurrentBadge.vue'
import NodeFindingsBadge from './NodeFindingsBadge.vue'

defineProps({
  id: {
    type: String,
    required: true
  }
})
</script>
