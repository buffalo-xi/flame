# 为可写变量增加新的变量选择模式

## Context

当前 VariableSelect 组件只有一种选择模式，展示：上游节点变量、流程变量、环境变量、参数变量。
可写变量（writes）需要一种不同的选择方式：
- **保留**：流程变量
- **隐藏**：环境变量、参数变量
- **替换**：上游节点变量 → 改为展示**父节点链上的 locals 变量**（沿 `parentNode` 向上遍历直到根节点）

## 方案：给 VariableSelect 增加 `selectMode` prop

### 修改文件

#### 1. `VariableSelect.vue`

**新增 prop：**
```js
selectMode: {
  type: String,
  default: 'all',
  validator: (value) => ['all', 'write'].includes(value)
}
```

**新增 computed `parentNodesWithLocals`：**
- 从当前节点出发，沿 `node.parentNode` 向上遍历
- 收集每个父节点 `node.data.config.locals` 中的变量
- 格式：`[{ node, locals: { key: { type, ... } } }]`

**模板改造：**
- 上游节点区块：`v-if="selectMode !== 'write'"` 隐藏
- 流程变量区块：保持不变（两种模式都显示）
- 环境变量区块：`v-if="selectMode !== 'write'"` 隐藏
- 参数变量区块：`v-if="selectMode !== 'write'"` 隐藏
- 新增父节点 locals 区块：`v-if="selectMode === 'write'"` 显示
  - 每个有 locals 的父节点一组，显示节点图标+名称+变量列表
  - 变量选择时 `sourceType` 使用 `'node'`，`sourceRef` 使用父节点 ID

**展开状态：**
- `expandedGroups` 中增加 parent 节点的展开 key

#### 2. `PropsPanel.vue`

**新增 prop 传递给 VariableSelect：**
```html
<select-mode="currentSelectMode"
```

**新增状态：**
```js
const currentSelectMode = ref('all')
```

**修改 `handleShowVariableSelect`：**
- 增加第 5 个参数 `selectMode = 'all'`
- 存储到 `currentSelectMode.value`

#### 3. `CodeNodeProps.vue`

**修改 `handleShowWriteVariableSelect`：**
- emit 时传递 `selectMode = 'write'`：
```js
emit('show-variable-select', event, name, callback, 'all', 'write')
```

### 父节点 locals 数据结构

来自 LoopNodeProps 的存储方式：`node.data.config.locals`
```js
{
  "varName": { type: "string", description: "..." },
  ...
}
```

### 数据流

```
CodeNodeProps (handleShowWriteVariableSelect)
  → emit('show-variable-select', event, name, callback, 'all', 'write')
PropsPanel (handleShowVariableSelect)
  → currentSelectMode = 'write'
  → <VariableSelect select-mode="write" />
VariableSelect
  → 隐藏环境变量/参数变量/上游节点
  → 显示父节点链 locals + 流程变量
```
