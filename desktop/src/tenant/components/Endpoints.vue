<template>
  <div class="endpoints">
    <!-- 端点 = 我们对外提供的服务（agent / flow 暴露出去的 MCP / RPC 入口）。
         与「工具」页分开：那边是 agent 能用的入向能力，这边是我们给出的出向能力。 -->
    <EndpointsTab @open="$emit('open', $event)" />
  </div>
</template>

<script setup>
import EndpointsTab from './capabilities/EndpointsTab.vue'

// 转发「点端点 → 进对应 agent / flow 编辑页」的跳转请求给 MainWindow
defineEmits(['open'])
</script>

<style scoped>
/* 页面留白与工具目录页（Capabilities）、智能体页（.agents）同一套：padding 20px + #fafafa */
.endpoints {
  position: relative;
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding: 20px;
  background: #fafafa;
  overflow: hidden;
}
</style>
