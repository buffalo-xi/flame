import { ref } from 'vue'
import apiConfig from '../../config/api.js'
import { getToken } from '../../utils/auth.js'

/**
 * 租户用户列表：用户管理与「服务凭证」共用同一份数据。
 *
 * 用 module 级单例（项目没装 pinia）：两个 tab 各自拉一次会不一致 ——
 * 新建 / 停用用户后，另一个 tab 的下拉里还是旧的。
 */
const users = ref([])
const usersLoading = ref(false)
// 绑定下拉用的用户选项；loadUsers 会一并写回，保证两边同源
// 既由 useUsers() 返回、也单独导出：AgentHistory / RunHistory 不在 settings 的组件树里，直接按下标引用即可。
export const userOptions = ref([])
// 租户级的默认配额（用户行没配覆盖时生效），用于显示「跟随租户（N）」
// First-paint only: loadUsers overwrites this with the backend defaults. The initial values mirror the code fallbacks in
// shared/quota (200 sessions per owner type, 1 GiB per session workspace).
const userQuotaDefaults = ref({ sessions: 200, session_storage_bytes: 1 << 30 })

/** 卡片主显用户名（name），不显显示名 */
export const userLabel = (id) => {
  const u = userOptions.value.find((x) => x.id === id)
  if (!u) return id || '-'
  return u.name || u.email || id
}

export const userOptionLabel = (u) => {
  const base = u.name || u.email || u.id
  return u.display_name && u.display_name !== base ? `${base}（${u.display_name}）` : base
}

/** 卡片上的显示名（有则附在用户名后面） */
export const userDisplayName = (id) => {
  const u = userOptions.value.find((x) => x.id === id)
  return (u && u.display_name) || ''
}

/** 悬停提示：用户名（显示名） */
export const userCardTitle = (id) => {
  const u = userOptions.value.find((x) => x.id === id)
  if (!u) return id || '-'
  const base = u.name || u.email || id
  return u.display_name ? `${base}（${u.display_name}）` : base
}

export const loadUsers = async () => {
  usersLoading.value = true
  try {
    const token = getToken()
    const resp = await fetch(apiConfig.admin.users.list, { headers: { Authorization: `Bearer ${token}` } })
    if (!resp.ok) return
    const data = await resp.json()
    if (data.code === 200) {
      users.value = data.data.items || []
      userOptions.value = users.value
      if (data.data.defaults) userQuotaDefaults.value = data.data.defaults
    }
  } catch (e) {
    console.error('failed to load the user list:', e)
  } finally {
    usersLoading.value = false
  }
}

/** 只在还没数据时拉一次（进绑定 tab 时用，避免每次切换都发请求） */
export const ensureUserOptions = async () => {
  if (userOptions.value.length > 0) return
  await loadUsers()
}

export function useUsers() {
  return {
    users,
    usersLoading,
    userOptions,
    userQuotaDefaults,
    loadUsers,
    ensureUserOptions,
    userLabel,
    userOptionLabel,
    userDisplayName,
    userCardTitle
  }
}
