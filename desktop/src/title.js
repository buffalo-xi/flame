// 页面标题：`flame Desk - <后缀>`。
//
// 独立成中立模块（与 mode.js 同理）：租户侧（tenant/utils/auth.js）与平台侧
// （platform/auth.js）各自维护会话、互不 import，标题口径由这里统一。
//
// 后缀：控制台 → 租户 schema（如 t0）；管理台 → "平台管理"；未登录 → 空（回到 index.html 的默认值）。

const APP_TITLE = 'flame Desk'

// setTitle 设置标题后缀；传空（或非字符串）即回到默认标题。
export const setTitle = (suffix) => {
  const s = typeof suffix === 'string' ? suffix.trim() : ''
  document.title = s ? `${APP_TITLE} - ${s}` : APP_TITLE
}

// resetTitle 清掉后缀（退出登录时用）。
export const resetTitle = () => setTitle('')
