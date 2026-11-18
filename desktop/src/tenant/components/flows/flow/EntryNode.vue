<template>
  <!-- 入口节点只画一个图标，名字（「入口」/ 撞名后的「入口-2」）平时看不见，
       悬停时用 tooltip 补上：多容器并存时靠它区分是哪个入口 -->
  <el-tooltip :content="data.label || '入口'" placement="top">
    <div class="entry-node" :class="{ 'child-node': parentNode }" ref="nodeRef" @click="handleNodeClick">
      <div class="node-content">
        <div class="icon-wrapper icon-entry"><el-icon><Aim /></el-icon></div>      
      </div>
      <Handle type="source" position="right" :id="'source' + id + 'right'" class="custom-handle" />
      <Handle type="source" position="bottom" :id="'source' + id + 'bottom'" class="custom-handle" />
      <Handle type="source" position="left" :id="'source' + id + 'left'" class="custom-handle" />
    </div>
  </el-tooltip>
</template>

<script setup>
import { ref, computed, nextTick, onMounted } from 'vue'
import { Aim } from '@element-plus/icons-vue'
import { Handle } from '@vue-flow/core'

const props = defineProps({
  data: {
    type: Object,
    default: () => ({ label: '入口' })
  },
  id: {
    type: String,
    required: true
  },
  getNodeById: {
    type: Function,
    default: null
  }
})

const emit = defineEmits(['handleClick'])

const nodeRef = ref(null)

const parentNode = computed(() => {
  if (!props.getNodeById) return null
  const node = props.getNodeById(props.id)
  return node?.parentNode || null
})

const handleNodeClick = (event) => {
  const handleElement = event.target.closest('.custom-handle')
  if (handleElement) {
    event.stopPropagation()    
    const handleId = handleElement.getAttribute('data-handleid') || handleElement.id
    emit('handleClick', props.id, handleId, event)
  }
}

</script>

<style scoped>
.entry-node {
  width: auto;
 
  height: 32px;
  border: 0px solid #00bcd4;
  border-radius: 16px;
  background-color: #fff;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  cursor: pointer;
  padding: 0 10px;
  position: relative;
}

.entry-node:hover {
  box-shadow: 0 2px 12px rgba(0, 188, 212, 0.3);
}

.entry-node.child-node {
  border-style: dotted;
  border-color: rgb(52, 51, 51);
}

.node-content {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 8px;
  flex: 1;
  padding-right: 0px;
}

.node-label {
  font-size: 11px;
  font-weight: 500;
  color: #333;
  white-space: nowrap;
}

.icon-wrapper {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 17px;
  height: 17px;
  border-radius: 8px;
  margin-right: 0px;
}

.icon-entry { background-color: #00704A; }

.icon-entry .el-icon {
  font-size: 16px;
  color: #ffffff;
}

.custom-handle {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background-color: #00bcd4;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  font-size: 12px;
  font-weight: bold;
  opacity: 0;
  z-index: 10;
  cursor: pointer;
}

.entry-node:hover .custom-handle {
  opacity: 1;
}

.custom-handle::before {
  content: '+';
}

.handle-hidden {
  display: none;
}
</style>