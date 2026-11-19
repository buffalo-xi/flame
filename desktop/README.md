# desktop

flame 的管理台前端：Vue 3 + Vite + Element Plus，租户端与平台端共用同一套构建产物。

## 开发

```bash
npm install
npm run dev      # http://localhost:5173
```

开发服务器会把 `/nexus` 代理到 `http://localhost:27006`（见 `vite.config.js`），
所以本地要先把 nexus 起起来。

环境变量：复制 `.env.example` 为 `.env`，按需改 `VITE_API_BASE`。

## 构建

```bash
npm run build          # 输出到 dist/
npm run preview        # 本地预览构建产物
npm run check:locales  # 语言包自检（build 前自动跑，见下）
```

生产构建不产出 sourcemap（`vite.config.js` 的 `build.sourcemap: false`）——
`dist/` 是整包发布的，带 `.map` 等于把源码一起发出去。

`npm run build` 会先执行 `prebuild` → `check:locales`（`scripts/check-locales.mjs`）：
以 `zh-CN` 为基准，逐语言比对文件集合、键集、`{占位符}` 与空值。
vue-i18n 缺键是静默回退到 `en` 的，不设这道闸就只能靠人眼发现「英文界面里冒出中文」。

## 目录

```
src/
  tenant/        租户端：智能体 / 技能 / 工具 / 工作流 / 运行 / 端点 / 设置 / 资产市场
  platform/      平台端：租户管理 / 用户 / 市场 / 系统
  title.js       标题（登录后带租户标识）
```

## 约定

- 接口地址统一走 `src/tenant/config/admin.js`，不在组件里手拼 base。
- 鉴权头用 `src/tenant/utils/auth.js` 的 `getAuthHeader()`。
- 状态轮询一律带 `silent` 参数，避免定时刷新把 loading 遮罩打到用户脸上。
- 时间显示走 `src/utils/datetime.js` 的 `formatDateTime()`，不要直接 `toLocaleString()`：
  无参调用跟随「系统」语言，界面切成英文后日期还是系统格式。
- 判断后端错误看 `error` 编码（`formatError()` / `body.error === 'xxx'`），不要比对 `msg` 文案。
- 语言切换入口统一用 `src/LocaleMenu.vue`（三处外壳共用），按钮显示 `currentLocaleShort()`；
  加语言只需改 `src/locales/index.js` 的 `LOCALES` 与语言目录。
