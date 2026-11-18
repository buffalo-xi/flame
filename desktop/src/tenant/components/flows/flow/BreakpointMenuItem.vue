<template>
  <el-dropdown-item @click="toggle">
    <el-icon><Flag /></el-icon>
    <span>{{ bpActive ? '取消断点' : '设置断点' }}</span>
  </el-dropdown-item>
</template>

<script setup>
import { computed, inject } from 'vue'
import { Flag } from '@element-plus/icons-vue'

const props = defineProps({
  id: { type: String, required: true }
})

const flowDebug = inject('flowDebug', null)
const bpActive = computed(() => !!flowDebug && flowDebug.isBreakpoint(props.id))

const toggle = () => {
  flowDebug?.toggleBreakpoint(props.id)
}
</script>
