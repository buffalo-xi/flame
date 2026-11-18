---
name: publish-manage-drawer
overview: 将 Agent/WF 的上架/下架改造为"发布管理"模式：菜单统一为"发布"，抽屉展示该 target 所有已发布端点（含协议标签），支持在抽屉内新增端点协议和单独删除端点。
todos:
  - id: redesign-agent-drawer
    content: 改造 AgentInstances.vue 发布抽屉：合并菜单为单一"发布"项，抽屉展示端点列表+新增表单+全部下架按钮，实现列表查询、单个新增、单个删除、全部下架逻辑。协议以彩色标签(MCP绿/HTTP蓝/LLM橙)展示。
    status: completed
  - id: redesign-workflow-drawer
    content: 改造 WFInstances.vue 发布抽屉：同 AgentInstances 改造方案，合并菜单、端点列表展示、新增协议发布、单个删除、全部下架。协议彩色标签一致。
    status: completed
---

## 产品概述
将"上架/下架"发布流程改造为"发布管理"模式。同一个智能体或工作流可按 MCP、HTTP、LLM 三种协议分别独立发布和取消发布，所有已发布端点在同一抽屉中集中管理。

## 核心功能
- **菜单统一**：将「上架」和「下架」两个菜单项合并为单一的「发布」项，始终可见
- **端点列表展示**：抽屉内展示该目标所有已发布端点，每行显示协议彩色标签（MCP 绿/HTTP 蓝/LLM 橙）、名称、描述、配置、创建时间及删除按钮
- **新增协议发布**：抽屉底部提供「新增协议」表单，选择协议后填写名称(沿用目标名)、描述和配置
- **单独删除端点**：每行端点旁有删除按钮，支持单独取消某一协议的发布
- **一键全部下架**：保留「全部下架」按钮，用于一次性取消所有协议的发布

## 技术选型
- 前端框架：Vue 3 + Element Plus（沿用现有技术栈）
- 后端：无需改动，所有 API 已就绪

## 实现方案

### 架构设计

```
抽屉数据流（以 Agent 为例）：

打开「发布」 → ① POST /svcendpoints/ {target_type:"agent", target_id:xxx}
                └→ 获取已发布端点列表
                └→ 渲染列表（协议标签 + 详情 + 删除按钮）
             → ② 用户点击「新增」→ 填写表单 → POST /svcendpoints/create
                └→ 成功后重新 ① 刷新列表 + fetchAgentInstances
             → ③ 用户点击某条「删除」→ DELETE /svcendpoints/:id
                └→ 成功后重新 ① 刷新列表 + fetchAgentInstances
             → ④ 用户点击「全部下架」→ POST /svcendpoints/unpublish
                └→ 成功后关闭抽屉 + fetchAgentInstances
```

### 数据流
1. **打开抽屉**：`POST /svcendpoints/` 携带 `{target_type, target_id}` 查询端点列表
2. **新增端点**：`POST /svcendpoints/create` 携带 `{name, description, target_id, target_type, protocol, config}`
3. **删除端点**：`DELETE /svcendpoints/:id`
4. **全部下架**：`POST /svcendpoints/unpublish` 携带 `{target_type, target_id}`

### 端点列表响应数据结构
```json
{
  "code": 200,
  "data": {
    "items": [{
      "id": "uuid",
      "name": "agent-name",
      "description": "...",
      "protocol": "mcp",
      "target_type": "agent",
      "target_id": "xxx",
      "config": {...},
      "created_at": "2026-01-01T00:00:00Z"
    }],
    "total": 1
  }
}
```

## 实现细节

### 关键代码结构

**抽屉模板结构**（需替换现有 214-253 行区域）：
- 上半部分：端点列表（v-for 遍历 publishedEndpoints）
  - 每行：协议彩色标签 + 名称 + 描述 + 创建时间 + 删除按钮
  - 协议标签颜色映射：{ mcp: '#52c41a', http: '#1890ff', llm: '#fa8c16' }
- 分隔线
- 下半部分：「新增协议发布」表单
  - protocol 下拉选择
  - name(disabled，沿用目标名)
  - description 输入
  - config 输入
  - 确认添加按钮
- 底部操作栏：「全部下架」按钮（红色） + 「关闭」按钮

**新增状态变量**：
- `publishedEndpoints`：当前目标的端点列表数组
- `newEndpointForm`：新增协议表单数据 `{ protocol: 'mcp', name: '', description: '', config: '{}' }`
- `isLoadingEndpoints`：加载状态

**新增方法**：
- `fetchPublishedEndpoints()`：POST /svcendpoints/ 查询列表
- `handleDeleteEndpoint(endpointId)`：确认后 DELETE /svcendpoints/:id
- `handleAddEndpoint()`：POST /svcendpoints/create
- `handleUnpublishAll()`：保留原有批量下架逻辑

### 协议标签颜色方案
| 协议 | 颜色 | 色值 |
|------|------|------|
| MCP  | 绿色 | #52c41a |
| HTTP | 蓝色 | #1890ff |
| LLM  | 橙色 | #fa8c16 |
