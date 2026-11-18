<template>
  <div class="tree-item">
    <div
      class="tree-node"
      :style="{ paddingLeft: level * 16 + 'px' }"
      @click="handleClick"
    >
      <span class="expand-icon" v-if="item.children.length > 0">
        <component :is="icon" />
      </span>
      <span class="expand-placeholder" v-else></span>
      <el-checkbox
        :model-value="isChecked"
        @change="handleToggleRequired"
        class="node-checkbox"
      />
      <span class="node-icon" :class="getIconClass(item)">
        <span v-if="item.type === 'number'" class="icon-text">N</span>
        <span v-else-if="item.type === 'string'" class="icon-text">S</span>
        <span v-else-if="item.type === 'boolean'" class="icon-text">B</span>
        <span v-else-if="item.type === 'object'" class="icon-text">O</span>
        <span v-else-if="item.type === 'array' && item.items?.type === 'number'" class="icon-text">N</span>
        <span v-else-if="item.type === 'array' && item.items?.type === 'string'" class="icon-text">S</span>
        <span v-else-if="item.type === 'array' && item.items?.type === 'boolean'" class="icon-text">B</span>
        <span v-else-if="item.type === 'array' && item.items?.type === 'object'" class="icon-text">O</span>
      </span>
      <div class="node-text-wrapper">
        <template v-if="isEditing">
          <input
            ref="labelInputRef"
            v-model="editLabel"
            class="node-label-input"
            @blur="handleLabelBlur"
            @keydown.enter="handleLabelConfirm"
            @keydown.esc="cancelRename"
          />
        </template>
        <span v-else class="node-label" @click.stop="handleLabelClick">{{ item.label }}</span>
        <template v-if="isEditingDescription">
          <input
            ref="descriptionInputRef"
            v-model="editDescription"
            class="node-description-input"
            placeholder="添加描述"
            @blur="handleDescriptionBlur"
            @keydown.enter="handleDescriptionConfirm"
            @keydown.esc="cancelDescription"
          />
        </template>
        <span 
          v-else 
          class="node-description" 
          :class="{ 'node-description-placeholder': !item.description }"
          @click.stop="handleDescriptionClick"
        >{{ item.description || '添加描述' }}</span>
      </div>
      <el-dropdown v-if="!item.isLeaf" @command="handleAddChild" trigger="click" class="add-child-dropdown">
        <el-icon class="add-child-icon">
          <Plus />
        </el-icon>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="number">
              <span class="dropdown-icon icon-number-inline">N</span>数字
            </el-dropdown-item>
            <el-dropdown-item command="string">
              <span class="dropdown-icon icon-string-inline">S</span>字符串
            </el-dropdown-item>
            <el-dropdown-item command="boolean">
              <span class="dropdown-icon icon-boolean-inline">B</span>布尔值
            </el-dropdown-item>
            <el-dropdown-item command="object">
              <span class="dropdown-icon icon-object-inline">O</span>对象
            </el-dropdown-item>
            <el-dropdown-item command="numberArray">
              <span class="dropdown-icon icon-number-array-inline">N</span>数字数组
            </el-dropdown-item>
            <el-dropdown-item command="stringArray">
              <span class="dropdown-icon icon-string-array-inline">S</span>字符串数组
            </el-dropdown-item>
            <el-dropdown-item command="booleanArray">
              <span class="dropdown-icon icon-boolean-array-inline">B</span>布尔数组
            </el-dropdown-item>
            <el-dropdown-item command="objectArray">
              <span class="dropdown-icon icon-object-array-inline">O</span>对象数组
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-icon class="delete-icon" @click.stop="handleDelete">
        <Delete />
      </el-icon>
    </div>
    <div v-if="item.children.length > 0 && isExpanded" class="tree-children">
      <StartNodePropsTreeItem
        v-for="child in item.children"
        :key="child.key"
        :item="child"
        :level="level + 1"
        :required-keys-map="requiredKeysMap"
        @toggle-expand="$emit('toggle-expand', $event)"
        @add-child="(type, parentKey) => $emit('add-child', type, parentKey)"
        @toggle-required="(key) => $emit('toggle-required', key)"
        @delete-node="(key) => $emit('delete-node', key)"
        @rename-node="(oldKey, newKey) => $emit('rename-node', oldKey, newKey)"
        @update-description="(key, description) => $emit('update-description', key, description)"
      />
    </div>
  </div>
</template>

<script setup>
import { ref, computed, inject, markRaw, nextTick } from 'vue'
import { ArrowDown, ArrowUp, Minus, Plus, Delete } from '@element-plus/icons-vue'

const props = defineProps({
  item: {
    type: Object,
    required: true
  },
  level: {
    type: Number,
    default: 0
  },
  requiredKeysMap: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits(['toggle-expand', 'add-child', 'toggle-required', 'delete-node', 'rename-node', 'update-description'])

const expandedKeys = inject('expandedKeys')

const isEditing = ref(false)
const editLabel = ref('')
const labelInputRef = ref(null)

const isEditingDescription = ref(false)
const editDescription = ref('')
const descriptionInputRef = ref(null)

const isExpanded = computed(() => expandedKeys.value.has(props.item.key))

const isObjectType = (item) => {
  if (item.type === 'object') return true
  if (item.type === 'array' && item.items?.type === 'object') return true
  return false
}

const icon = computed(() => {
  if (isObjectType(props.item)) {
    if (props.item.children.length === 0) {
      return markRaw(Minus)
    }
    return isExpanded.value ? markRaw(ArrowUp) : markRaw(ArrowDown)
  }
  if (props.item.children.length === 0) {
    return markRaw(Minus)
  }
  return isExpanded.value ? markRaw(ArrowUp) : markRaw(ArrowDown)
})

const isChecked = computed(() => {
  const parentKey = getParentKey(props.item.key)
  const parentRequiredKeys = props.requiredKeysMap[parentKey] || []
  return parentRequiredKeys.includes(props.item.label)
})

const getParentKey = (key) => {
  const lastDotIndex = key.lastIndexOf('.')
  return lastDotIndex === -1 ? '' : key.substring(0, lastDotIndex)
}

const getIconClass = (item) => {
  if (item.type === 'number') return 'icon-number'
  if (item.type === 'string') return 'icon-string'
  if (item.type === 'boolean') return 'icon-boolean'
  if (item.type === 'object') return 'icon-object'
  if (item.type === 'array') {
    if (item.items?.type === 'number') return 'icon-number-array'
    if (item.items?.type === 'string') return 'icon-string-array'
    if (item.items?.type === 'boolean') return 'icon-boolean-array'
    if (item.items?.type === 'object') return 'icon-object-array'
  }
  return 'icon-object'
}

const handleClick = () => {
  if (!isEditing.value && !isEditingDescription.value) {
    emit('toggle-expand', props.item.key)
  }
}

const handleLabelClick = () => {
  isEditing.value = true
  editLabel.value = props.item.label
  nextTick(() => {
    labelInputRef.value?.focus()
    labelInputRef.value?.select()
  })
}

const handleLabelBlur = () => {
  confirmRename()
}

const handleLabelConfirm = () => {
  confirmRename()
}

const confirmRename = () => {
  const newLabel = editLabel.value.trim()
  if (newLabel && newLabel !== props.item.label) {
    const oldKey = props.item.key
    const parentKey = getParentKey(oldKey)
    const newKey = parentKey ? `${parentKey}.${newLabel}` : newLabel
    emit('rename-node', oldKey, newKey)
  }
  isEditing.value = false
}

const cancelRename = () => {
  isEditing.value = false
}

const handleDescriptionClick = () => {
  isEditingDescription.value = true
  editDescription.value = props.item.description || ''
  nextTick(() => {
    descriptionInputRef.value?.focus()
    descriptionInputRef.value?.select()
  })
}

const handleDescriptionBlur = () => {
  confirmDescription()
}

const handleDescriptionConfirm = () => {
  confirmDescription()
}

const confirmDescription = () => {
  const newDescription = editDescription.value.trim()
  emit('update-description', props.item.key, newDescription)
  isEditingDescription.value = false
}

const cancelDescription = () => {
  isEditingDescription.value = false
}

const handleAddChild = (type) => {
  emit('add-child', type, props.item.key)
}

const handleToggleRequired = () => {
  emit('toggle-required', props.item.key)
}

const handleDelete = () => {
  emit('delete-node', props.item.key)
}
</script>

<style scoped>
.tree-item {
  user-select: none;
}

.tree-node {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 0;
  cursor: pointer;
  border-radius: 4px;
  transition: background-color 0.2s;
}

.tree-node:hover {
  background-color: #f5f7fa;
}

.expand-icon {
  width: 14px;
  height: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  color: #8b0000;
  flex-shrink: 0;
}

.expand-icon svg {
  width: 100%;
  height: 100%;
}

.expand-placeholder {
  width: 14px;
  height: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.expand-placeholder::after {
  content: '';
  display: block;
  width: 10px;
  height: 1px;
  background: #dbb7b7;
  border-radius: 1px;
}

.node-checkbox {
  flex-shrink: 0;
}

.node-icon {
  width: 16px;
  height: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  flex-shrink: 0;
}

.node-icon.icon-number {
  width: 16px;
  height: 16px;
  background: #409eff;
  border-radius: 50%;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-string {
  width: 16px;
  height: 16px;
  background: #67c23a;
  border-radius: 50%;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-boolean {
  width: 16px;
  height: 16px;
  background: #9c27b0;
  border-radius: 50%;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-number-array {
  width: 16px;
  height: 16px;
  background: #e6a23c;
  border-radius: 4px;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-string-array {
  width: 16px;
  height: 16px;
  background: #f56c6c;
  border-radius: 4px;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-boolean-array {
  width: 16px;
  height: 16px;
  background: #9c27b0;
  border-radius: 4px;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-object {
  width: 16px;
  height: 16px;
  background: #909399;
  border-radius: 50%;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.node-icon.icon-object-array {
  width: 16px;
  height: 16px;
  background: #909399;
  border-radius: 4px;
  color: #fff;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
}

.icon-text {
  line-height: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.node-text-wrapper {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.node-label {
  font-size: 14px;
  color: #333;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: text;
  flex-shrink: 0;
}

.node-label:hover {
  color: #1890ff;
}

.node-label-input {
  font-size: 13px;
  font-weight: 500;
  color: #333;
  padding: 4px 8px;
  border: 0px solid #d9d9d9;
  background-color: #eeeeee;
  border-radius: 4px;
  outline: none;
  width: 120px;
  flex-shrink: 0;
  box-sizing: border-box;
}

.node-description {
  font-size: 12px;
  color: #999;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: text;
  flex: 1;
  min-width: 0;
}

.node-description:hover {
  color: #1890ff;
}

.node-description-placeholder {
  font-style: italic;
  color: #8b0000;
}

.node-description-input {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: #666;
  padding: 4px 8px;
  border: 0px solid #d9d9d9;
  border-radius: 4px;
  background-color: #eeeeee;
  outline: none;
  box-sizing: border-box;
}

.node-description-input :deep(.el-input__inner)::placeholder,
.node-description-input :deep(.el-textarea__inner)::placeholder {
  color: #8b0000;
  font-style: italic;
}

.add-child-dropdown {
  margin-left: auto;
}

.add-child-icon {
  font-size: 14px;
  color: #1890ff;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.2s;
}

.tree-node:hover .add-child-icon {
  opacity: 1;
}

.add-child-icon:hover {
  color: #40a9ff;
}

.delete-icon {
  font-size: 14px;
  color: #f56c6c;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.2s;
  flex-shrink: 0;
  margin-left: 4px;
}

.tree-node:hover .delete-icon {
  opacity: 1;
}

.delete-icon:hover {
  color: #c45656;
}

.tree-children {
  overflow: hidden;
}

/* 下拉菜单图标样式 */
.dropdown-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 11px;
  font-weight: bold;
  font-family: Arial, sans-serif;
  color: #fff;
  margin-right: 8px;
  flex-shrink: 0;
}

.dropdown-icon.icon-number-inline {
  background: #409eff;
  border-radius: 50%;
}

.dropdown-icon.icon-string-inline {
  background: #67c23a;
  border-radius: 50%;
}

.dropdown-icon.icon-boolean-inline {
  background: #9c27b0;
  border-radius: 50%;
}

.dropdown-icon.icon-object-inline {
  background: #909399;
  border-radius: 50%;
}

.dropdown-icon.icon-number-array-inline {
  background: #e6a23c;
  border-radius: 4px;
}

.dropdown-icon.icon-string-array-inline {
  background: #f56c6c;
  border-radius: 4px;
}

.dropdown-icon.icon-boolean-array-inline {
  background: #9c27b0;
  border-radius: 4px;
}

.dropdown-icon.icon-object-array-inline {
  background: #909399;
  border-radius: 4px;
}
</style>