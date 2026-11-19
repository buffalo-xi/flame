<template>
  <el-dropdown-item @click="toggle">
    <el-icon><Flag /></el-icon>
    <span>{{ bpActive ? t('flow.node.breakpoint.unset') : t('flow.node.breakpoint.set') }}</span>
  </el-dropdown-item>
</template>

<script setup>
import { computed, inject } from 'vue'
import { Flag } from '@element-plus/icons-vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps({
  id: { type: String, required: true }
})

const flowDebug = inject('flowDebug', null)
const bpActive = computed(() => !!flowDebug && flowDebug.isBreakpoint(props.id))

const toggle = () => {
  flowDebug?.toggleBreakpoint(props.id)
}
</script>
