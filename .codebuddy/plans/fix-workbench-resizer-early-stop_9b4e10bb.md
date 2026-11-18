---
name: fix-workbench-resizer-early-stop
overview: 修复「对话区↔工作台」分割条在内容区折叠（面板占满整行）时提前拖不动的问题：重分配总宽改为现场量取 .chat-main 实际宽度，而非使用过期的 chatWidth 记录值。
todos:
  - id: verify-resizer-fix
    content: 核对并落地 MainWindow.vue 中 chatPanelRef 与 startWbResize 实测宽度逻辑
    status: completed
  - id: harden-clamp
    content: 为重分配结果加最小宽度兜底，确保非重分配分支行为不变
    status: completed
    dependencies:
      - verify-resizer-fix
  - id: validate
    content: 用 [skill:lsp-code-analysis] 校验引用完整性并运行 lint 与 build 验证
    status: completed
    dependencies:
      - harden-clamp
---

## 需求概述
修复租户主窗口 `MainWindow.vue` 中「对话区 ↔ 工作台」分割条在特定布局下提前拖不动的问题。

## 问题现象
当内容区被折叠、对话面板占满整行（`chatPanelFull`）时，向左拖动该分割条（工作台应变宽、对话区应变窄），对话区还剩很宽就到达上限、无法继续拖动。

## 期望行为
- 折叠占满整行状态下，向左拖：工作台持续变宽，直到对话区收缩到最小宽度 `CHAT_MIN_WIDTH`（280px）为止。
- 向右拖：工作台持续变窄，直到 `WB_MIN_WIDTH`（320px）为止。
- 非折叠状态、以及会话关闭（分割条在右侧、工作台直接对内容区伸缩）的既有行为保持不变。

## 约束
- 不改变 `CHAT_MIN_WIDTH = 280` 等既有最小宽度常量。
- 仅调整宽度重分配计算方式，不改变整体布局架构与交互形式。


## 技术栈
沿用现有项目技术栈：Vue 3（组合式 API）+ Vite，无新增依赖。

## 实现方案

### 根因
`startWbResize` 重分配分支用 `sum = chatWidth.value + wbWidth.value` 作为「对话区 + 工作台」的恒定总宽。但内容区折叠导致面板占满整行时，Chat 面板宽度由 `.chat-panel--full { flex: 1 1 auto; width: auto }` 的 flex 撑开，实际宽度远大于 `chatWidth` 记录值（该值仅由非折叠态的内联 `width` 驱动）。于是 `maxWb = sum - CHAT_MIN_WIDTH` 偏小，工作台提前到顶，对话区仍保留「实际宽度 − chatWidth」的差值空间，表现为拖不动。

### 修复策略
拖拽开始时**现场量取** `.chat-main` 的实际 `clientWidth`，用它加工作台宽作为恒定总宽 `sum`，从而在任何布局状态下都得到正确的重分配上限：

```js
const redistrib = side === 'left' && chatVisible.value
// 内容区折叠时面板被 flex 撑满整行，比 chatWidth 记录值更宽，故实测 .chat-main
const chatMainEl = redistrib ? chatPanelRef.value?.$el?.querySelector('.chat-main') : null
const mainW = chatMainEl && chatMainEl.clientWidth > 0 ? chatMainEl.clientWidth : chatWidth.value
const sum = mainW + startWb
```

配套：模板 `<Chat ref="chatPanelRef" ... />` 挂引用；`const chatPanelRef = ref(null)`；`chatWidth.value = Math.max(CHAT_MIN_WIDTH, sum - wb)` 加最小宽度兜底。

### 关键决策与取舍
- **用 DOM 实测而非新增响应式同步**：若改为在每次布局变化时把面板实际宽度同步回 `chatWidth`，会引入额外 watch 与写回，易与内联 `style` 相互干扰；实测只在 mousedown 时执行一次，最小侵入。
- **保持「恒定总宽」语义**：折叠状态下内部布局是 `sessionPanel + chat-main(flex) + resizer + workbench`，工作台宽度只影响 chat-main，故测量一次 `sum` 即可在全拖拽过程中成立。
- **回退保护**：非重分配分支（`side === 'right'` 或会话隐藏）与量不到元素时，退回 `chatWidth.value`，保证零行为变更。

### 性能
测量仅在 `mousedown` 时发生一次，**不在 `mousemove` 中查询/测量 DOM**，避免每帧强制回流（reflow）。拖拽过程仅做常数级数值运算并写回 `wbWidth`/`chatWidth`（复杂度 O(1)）。

### 架构
不新增模式、不改数据流；仅修改 `MainWindow.startWbResize` 的内部实现，并给 `Chat` 增加一个模板 ref。无复杂组件关系，不需要架构图。

## 涉及文件
- `d:/proj/flame/nexus_desk/src/tenant/components/MainWindow.vue`
  - 模板（约 91–102 行）：`<Chat>` 增加 `ref="chatPanelRef"`。
  - 脚本 `startWbResize`（约 451–490 行）：总宽改为实测；`chatWidth` 写回加最小宽度兜底。
  - 新增 ref 声明（约 326 行附近）。
- `d:/proj/flame/nexus_desk/src/tenant/components/Chat.vue`：无需修改，仅作为被测量对象（`.chat-main` 已存在）。

## 执行注意
- 保持非重分配分支（`side === 'right'` / 会话隐藏）逻辑原样，避免影响「工作台 ↔ 内容区」拖拽与 `clampLayout`。
- `mousemove` 回调内不得再做 DOM 查询，防止拖拽卡顿。
- 修改后验证：`read_lints` 无诊断，`npm --prefix d:/proj/flame/nexus_desk run build` 成功。


## Agent Extensions
### Skill
- **lsp-code-analysis**
  - Purpose: 核对 `chatPanelRef`、`startWbResize` 的定义与引用是否完整、无遗留调用点。
  - Expected outcome: 确认 `chatPanelRef` 仅在模板与新逻辑中使用、`startWbResize` 的调用来源（Chat.vue 的 `wb-resize` 事件）未受影响。
