# src/platform —— 平台管理台（前端）

中文 | [English](README.md)

与控制台（`src/tenant/**`）**互不依赖**的两棵子树之一，与控制台共用同一个 Vite 工程、
同一个部署产物，靠登录页的「租户登录 / 平台登录」切换。

## 目录

```
src/
├─ mode.js                   中立开关：当前模式 + 两套会话存在性（两侧都可读）
├─ AuthGate.vue              登录薄壳：[租户登录] ↔ [平台登录]
├─ App.vue                   外壳：按 mode + 登录态渲染 AuthGate / MainWindow / PlatformApp
└─ platform/                 ★ 本子树（可整棵搬走）
    ├─ README.md             本文件
    ├─ auth.js               平台会话（app_flame_mgmt_token / app_flame_mgmt_user）
    ├─ api.js                /platform/* 请求层（统一解包 {code,msg,data}）
    ├─ PlatformApp.vue       管理台 shell（顶栏同控制台主界面：logo + 图标药丸导航 + 头像/退出；
    │                        内容区灰底白卡；按 role 分流到 AdminConsole / MyTenantsView）
    ├─ assets/pic/flame.png  顶栏 logo（控制台那份的副本，避免跨子树 import）
    └─ views/
        ├─ PlatformLogin.vue 平台登录（账号 + 密码，无租户字段）
        ├─ AdminConsole.vue  管理员页容器（按顶栏 tab 渲染对应视图，自己没有 tab）
        ├─ AdminTenantsView.vue  租户管理（左列表 + 右详情卡）
        ├─ AdminAssetsView.vue   本地市场（左「全部资产」+ 右裁决卡；另有上架策略 / 对外接入两个 tab）
        ├─ AdminMarketView.vue   跨服务器市场（左对端站点 + 右对端目录；只管「我去拉别人」）
        ├─ AdminPluginsView.vue  插件与能力（左导航 + 右详情）
        ├─ AdminUsersView.vue    平台用户管理
        ├─ AdminSystemView.vue   系统设置（左域导航 + 右域内容）
        ├─ system/               系统设置的"域"组件：一个域一个文件
        │   ├─ SysQuotaCard.vue        配额上限（系统行）
        │   ├─ SysFlowConfigsCard.vue  工作流运行参数（系统行）
        │   └─ SysLlmPresetsCard.vue   LLM 服务商预设（全局共享，完整 CRUD）
        └─ MyTenantsView.vue 「用户」身份：我的租户（只读：左租户卡片 + 右详情）
```

## 边界规则（改动本子树时必须遵守）

**允许 import**：`vue`、`element-plus`、同目录内文件、`src/mode.js`，以及两棵子树共用的中性根模块 —— `src/locales/**`、
`src/help/**`（帮助抽屉与主题注册表；它内部引用 `src/utils/markdown.js` 与 `src/composables/useEscClose.js`，两者同理中性）

**禁止 import**

| 禁止 | 替代 |
|---|---|
| `src/tenant/components/**`（控制台页面与 Login.vue） | 登录页在 `views/PlatformLogin.vue` 自带一份 |
| `src/tenant/utils/auth.js` | `auth.js`（本目录），键名走 `mode.js` 的 `SESSION_KEYS` |
| `src/tenant/config/**` | `api.js` 自己解析 apiBase（运行时真源同为 `window.APP_CONFIG.apiBase` / `VITE_API_BASE`） |
| `src/tenant/components/MainWindow.vue` 的顶栏/内容区样式 | 复制进 `PlatformApp.vue`（`.top-nav` / `.nav-menu` / `.content-panel`），改观感时两边一起改 |
| `src/tenant/assets/pic/flame.png` | 副本在 `assets/pic/flame.png` |

反向也成立：`src/tenant/**` 不得 import `src/platform/**`（唯一例外是外壳三件套
`App.vue` / `AuthGate.vue` / `mode.js`）。

上面三条是"整棵子树可搬走"的前提 —— 搬走时只带走 `src/platform/**` + `src/mode.js` 即可。

## 身份与数据面

| 登录 token 的 role | 界面 | 接口 |
|---|---|---|
| `admin` | 管理员页（租户管理 + 本地市场 + 插件与能力 + 平台用户 + 系统设置） | `/platform/tenants*`、`/platform/assets*`、`/platform/plugins`、`/platform/users*`、`/platform/system/*` |
| `user` | 我的租户（自助启停 + 工作流运行参数 + 根账号密码） | `/platform/me/tenants`（读）+ `/me/tenants/:schema/status`（启停）+ `/me/tenants/:schema/flowconfigs`（工作流运行参数）+ `/me/tenants/:schema/primary/password`（重置 primary 密码）；后端一律按 `owner_user_id = 自己` 校验 |

两者都会先调 `/platform/me`：账号被停用/删除时立即失效（后端每次回查数据库）。

## 界面结构（与控制台主界面同款）

- **顶栏**（`PlatformApp.vue`）：logo 图 + 斜体 `flame`；中间为图标药丸导航
  （管理员：租户管理 / 平台用户 / 插件与能力 / 系统设置 / 本地市场 / 远端市场；`user` 身份：我的租户）；右侧圆形头像（角色 / 显示名 / 账号名 / 名下租户数
  在 `title` tip 里，**点击即复制账号名**）+「返回控制台」（浏览器里还有控制台会话时才显示）+「退出登录」。
  另外还有「修改密码」（`POST /platform/me/password`，本人凭旧密码更换，管理员与用户都能用）。
- **内容区**：灰底 `#f0f2f5` 由 `PlatformApp` 的 `.main-content` 提供；`.content-panel` 只负责四边 10px 内缩，
  **白卡由各页自己给**（与主界面的左对话面板 / 右内容面板同款，圆角 12px）：
  租户管理、平台用户管理、我的租户（`user` 身份）都是**左右两张卡**（`.list-pane` / `.detail-pane`，右卡固定 400px）：
  管理页左卡是表格；我的租户左卡是**租户卡片网格**（`.t-grid`：`repeat(auto-fill, minmax(200px, 1fr))`，
  一行多张、每张约 200~250px 随行宽浮动，整块可点、选中高亮），右卡只读 + 三处自助写操作
  （卡头「停用/启用」、折叠卡「根账号密码」、「工作流运行参数」），都只作用于自己名下的租户。
  **页面级工具条已取消**：计数 / 搜索 / 筛选 / 刷新 / 新建 全在列表卡内的 `.lp-bar` 一行里
  （卡上没有独立的头部条 —— 页面就是两张卡，不留横跨两卡的控件行）。
  详情卡内「租户资料 / 配额 / 工作流运行参数」与「账号资料 / 重置密码 / 名下租户」各自又是一张
  **可折叠卡片**（`.blk` + `.blk-head` / `.blk-body`，展开状态在 `open` 里）；
  **我的租户页的四块默认全部收起**（先展示身份与信息行，要看哪块再展开）。
  卡片滚动条统一「**位置常驻、悬浮才上色**」（`.detail-pane` / `.t-list` / `.sys-nav` / `.sys-body`）：
  条宽固定 6px（`::-webkit-scrollbar: 6px` + `scrollbar-width: thin`），**不悬浮时拇指透明**，
  悬浮才给色 —— 这样平时看不见条，又不会因"条突然出现 / 消失"把卡片内容挤动。
  > **别改回 `width: 0` / `scrollbar-width: none`**：那样条是不占位的，悬浮瞬间会横向回流 6px。
  同样**不动 `overflow`**，滚动能力（滚轮 / 触控板 / 键盘）不变；
  列表卡里的表格走 `el-table` 自带滚动条，本来就是悬浮才显，不用管。
- **系统设置页**（`AdminSystemView.vue`）：左域导航（220px 白卡）+ 右侧**所有域卡片纵向堆叠**
  （复用 `.blk` 折叠壳，默认都展开）。点左侧某项 = 选中高亮 → 收起则展开 → `scrollIntoView` 平滑滚到该卡。
  卡片头只有「域名 + 说明 + 箭头」，**保存按钮在各域卡片内部**（`.blk-foot`：说明占左、按钮靠右）。
  域组件自成一卡：自带加载、校验、保存与按钮，父级不转发任何调用。**新增一个域 = 后端加一对
  `/platform/system/*` 接口 + 写一个域组件 + 在 `DOMAINS` 里加一行（带 `component`）**，互不影响。
  域组件里是三列表格「配置项 / 兜底值 / 系统级设置」，保存前用确认框逐项列出"旧 → 新"。
- **导航标签上的数量**：被渲染的视图 `emit('count', n)` →（管理员页经 `AdminConsole`，我的租户直接）→ `PlatformApp`
  的 `onCount(key, n)`；`null` = 该页尚未加载过（标签不显示括号，避免闪 0），加载过之后每次增删都会刷新。

## 与后端接口的约定

- 成功：`{code:200, msg, data}` → `request()` 直接返回 `data`
- 业务失败：`{code:400, msg}` → 抛 `ApiError(msg)`
- 未授权 / 权限不足：HTTP `401` / `403` + `{error, message}` → 抛 `ApiError(message, status)`
  - `401` 会顺手清本地会话，外壳切回登录页
- 冲突：HTTP `409` + `{error:"conflict", message}` → 抛 `ApiError(message, 409)`
  （新建租户时 schema 被抢占走这条：提示 + 重新探索，**弹窗不关**，等操作者确认再开通）
- 请求前缀：`${apiBase}/platform`（dev 下 `apiBase=/nexus`，由 vite 代理到后端）

## 待接入

- 暂无。管理台已支持：租户管理（**左表格 + 右详情卡**：搜索 / 状态筛选 /
  开通向导（**schema 自动探索、不可编辑**，`GET /platform/tenant-schemas/next`；首个租户管理员登录名固定
  `primary`、初始密码预填 `12345678`（可改）；落库前二次校验，被占用回 409 → 重新探索 + 等操作者确认）/ 资料内联编辑（含转移负责人）/
  三类配额内联调整（用量 vs 上限 + 进度）/ 工作流运行参数内联调整（租户行，系统级只读对照）/ 启停；
  列表走后端真分页 `page`/`page_size`，顶栏徽标用接口回的 `total`）；
  本地市场（**左「全部资产」+ 右裁决卡**：按状态分块 + 类型/来源筛选 + 搜索（`GET /platform/assets` 不分页，天然按「待审优先 + 更新时间倒序」）；
  详情展示内容（入口/文件数/体积）、依赖声明（skills / private_skills 不随行 / tools+grant_kind / requires / net.egress）、
  装过几家、上一轮审核意见；裁决按钮按状态机置灰：`pending` 才能 通过（意见可空）/ 驳回（**原因必填**，就是租户侧
  「我的资产」能看到的那句话），`listed` 才能 下架；可见范围 all ↔ allowlist（全量替换，租户下拉取 `/platform/tenants` 全量）；
  顶栏徽标是**待审数**而不是总条数，且只在未按状态筛选时刷新，避免徽标随筛选跳变；
  详情卡「导出包」把已审核快照打成 zip（`GET /platform/assets/:id/package`），工具栏「导入资产包」上传
  另一台机器导出的 zip（`POST /platform/asset-imports`，multipart `file`/`origin`/`force`），
  导入一律 pending 并在结果里列 warnings —— 跨服务器搬运见 `nexus/docs/asset-sharing.zh.md` §12）；
  另有「上架策略」tab（租户上架要不要过平台审核）与「对外接入」tab（匿名访问开关 `market.open`
  + 出口令牌：别人拿令牌读本机已审核资产，明文只显示一次，可启停/删除 —— 这两件事都是「允许谁拉我」，
  与远端市场「我去拉别人」方向相反，所以放在本地市场）；
  远端市场（**左对端站点 + 右对端目录**：对端 = 我去拉的那一端（地址 + 对方给我的 `flmt_…` 令牌 + 是否自动通过），
  选中后「拉取目录」（`GET /platform/peers/:id/catalog` 代理对端，标注已导入/本地版本），逐条「导入 / 覆盖导入」
  （`POST /platform/peers/:id/import`，force 覆盖则本地版本 +1）；
  平台用户管理（**左表格 + 右详情卡**：搜索 / 角色 / 状态筛选 / 新建弹窗（显示名可空，留空则显示账号名；
  初始密码预填 `12345678`，可改）；右卡三块折叠卡 = 账号资料（显示名 / 角色 / 状态内联保存，账号只读）+
  重置密码（内联，取代原弹窗）+ 名下租户（只读，`GET /platform/users/:id/tenants`，`admin` 不显示这块）；
  卡头「停用/启用」与「保存资料」对**自己**和**受保护管理员**（后端回带的 `protected`，即固定 `plat`）禁用 ——
  后端同样拦；列表同样走后端真分页 `page`/`page_size`）；
  我的租户（`user` 身份：左 = **租户卡片网格**（一行多张；显示名 + schema 同一行 / 状态 tag / 套餐 /
  用量摘要（超限标红）/ 备注（最多两行；没写备注时显示兜底文案「创建者很懒，什么都没写！」）；
  本地搜索 + 状态筛选，无分页 —— `/platform/me/tenants` 一次给全量）+ 右 = 详情卡四块折叠卡 =
  用量与配额（只读进度条）+ **根账号密码**（新密码 + 确认，重置 primary 的密码，不动角色与状态）+
  **工作流运行参数**（可改：配置项 / 系统级只读对照 / 租户值，前端先按"不超过系统级"拦一道）+ 租户信息（键值只读）；
  四块**默认全部收起**；
  **自助写操作三处**：卡头「停用/启用」（`PUT /platform/me/tenants/:schema/status`）、工作流运行参数保存
  （`PUT /platform/me/tenants/:schema/flowconfigs`）、根账号密码（`POST /platform/me/tenants/:schema/primary/password`），
  都只能作用于自己名下的租户（否则 403）、都带确认/校验且立即生效；租户数同样上抛给顶栏标签）；
  系统设置（配额上限 / 工作流运行参数 / 智能体运行参数 / LLM 服务商预设四个域——
  前三个写系统行 `tenant_id = ''`，第四个是 `platform.llm_provider_presets` 的完整 CRUD）。
  配额卡绑的是**原始行**（`GET /tenants/:schema/quota`），行内展示的是**生效值** `min(本租户值, 系统顶)`，
  原始值高于系统顶时给橙色提示——列表接口回的是生效值，**不能拿来当编辑框**。
