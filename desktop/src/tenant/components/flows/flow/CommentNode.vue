<template>
  <div class="comment-node" ref="nodeRef">
    <div class="comment-content">
      <textarea
        class="comment-textarea"
        v-model="commentText"
        placeholder="输入注释内容..."
        @input="handleTextChange"
      ></textarea>
    </div>
    <el-dropdown trigger="hover" placement="right-start">
      <div class="icon-wrapper icon-comment"><el-icon><ChatLineSquare /></el-icon></div>
      <template #dropdown>
        <el-dropdown-menu>
          <!-- 断点菜单不给：comment 没有执行器、永远不会被跑到 -->
          <el-dropdown-item @click="deleteNode">
            <el-icon><Delete /></el-icon>
            <span>删除节点</span>
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <div class="resize-indicator" @mousedown.stop="startResize"></div>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { ChatLineSquare, Delete } from '@element-plus/icons-vue'
import { useVueFlow } from '@vue-flow/core'

const props = defineProps({
  data: {
    type: Object,
    default: () => ({ label: '注释' })
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

const nodeRef = ref(null)
const emit = defineEmits(['delete', 'updateData'])
const commentText = ref(props.data.comment || '')

const { updateNodeInternals, getViewport } = useVueFlow()

const deleteNode = () => {
  emit('delete', props.id)
}

const handleTextChange = () => {
  emit('updateData', { nodeId: props.id, data: { comment: commentText.value } })
}

const startResize = (event) => {
  if (!nodeRef.value) return

  const startX = event.clientX
  const startY = event.clientY
  const viewport = getViewport()
  const scale = viewport?.zoom || 1

  const startWidth = parseFloat(nodeRef.value.style.width) || nodeRef.value.offsetWidth / scale
  const startHeight = parseFloat(nodeRef.value.style.height) || nodeRef.value.offsetHeight / scale

  const onMove = (e) => {
    const newWidth = Math.max(120, startWidth + (e.clientX - startX) / scale)
    const newHeight = Math.max(60, startHeight + (e.clientY - startY) / scale)
    if (nodeRef.value) {
      nodeRef.value.style.width = newWidth + 'px'
      nodeRef.value.style.height = newHeight + 'px'
    }
    nextTick(() => {
      if (props.id) {
        updateNodeInternals(props.id)
      }
    })
  }

  const onUp = () => {
    document.removeEventListener('mousemove', onMove)
    document.removeEventListener('mouseup', onUp)
    document.body.style.cursor = 'default'

    if (nodeRef.value) {
      const size = { width: nodeRef.value.style.width, height: nodeRef.value.style.height }
      emit('updateData', { nodeId: props.id, data: { config: { size } } })
    }
  }

  document.addEventListener('mousemove', onMove)
  document.addEventListener('mouseup', onUp)
  document.body.style.cursor = 'se-resize'
}

onMounted(() => {
  const size = props.data?.config?.size
  if (nodeRef.value && size) {
    if (size.width) nodeRef.value.style.width = size.width
    if (size.height) nodeRef.value.style.height = size.height
  }
})
</script>

<style scoped>
.comment-node {
  width: 200px;
  height: 100px;
  border: 1px dashed #ccc;
  background-color: transparent;
  cursor: pointer;
  position: relative;
  border-radius: 8px;
}

.comment-node:hover {
  border-color: #409eff;
  box-shadow: 0 0 12px rgba(64, 158, 255, 0.2);
}

.comment-content {
  width: 100%;
  height: 100%;
  padding: 0px;
  box-sizing: border-box;  
  background-color: white;
  border-radius: 8px;
}

.comment-textarea {
  width: 100%;
  height: 100%;
  padding: 5px;
  border: none;
  font-size: 12px;
  line-height: 1.5;
  color: #666;
  resize: none;
  outline: none;
  box-sizing: border-box;
  border-radius: 8px;
}

.comment-textarea::placeholder {
  color: #444;
}

.icon-wrapper {
  position: absolute;
  bottom: 8px;
  left: 8px;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background-color: #409eff;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.2s;
}

.comment-node:hover .icon-wrapper {
  opacity: 0.7;
}

.icon-comment .el-icon {
  font-size: 14px;
  color: #ffffff;
}

.resize-indicator {
  position: absolute;
  right: -8px;
  bottom: -8px;
  width: 22px;
  height: 22px;
  cursor: se-resize;
  z-index: 100;
  opacity: 0;
  transition: opacity 0.2s;
}

.resize-indicator:after {
  content: '';
  position: absolute;
  right: 5px;
  bottom: 5px;
  width: 12px;
  height: 12px;
  border-right: 2px solid #bbb;
  border-bottom: 2px solid #bbb;
  border-radius: 0 0 6px 0;
}

.comment-node:hover .resize-indicator {
  opacity: 1;
}
</style>