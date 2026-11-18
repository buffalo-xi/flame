import { ref } from 'vue'
import apiConfig from '../../config/api.js'
import { getToken } from '../../utils/auth.js'

/**
 * 服务凭证列表（svc_auth_bindings，API Key 与 OAuth 共用接口）。
 *
 * module 级单例：绑定只能在本 tab 里创建/刷新，切换 tab 时统一走这一份数据，
 * 避免各 tab 各自持有一份互相看不到的列表。
 */
const apikeyBindings = ref([])
const oauthBindings = ref([])
const bindingLoading = ref(false)

/** 拉两组绑定；showSecret 是纯前端状态，刷新后默认隐藏 */
export const loadBindings = async () => {
  bindingLoading.value = true
  try {
    const token = getToken()
    const headers = { Authorization: `Bearer ${token}` }
    const [akResp, oaResp] = await Promise.all([
      fetch(apiConfig.admin.svcAuthBindings.list('apikey'), { headers }),
      fetch(apiConfig.admin.svcAuthBindings.list('oauth'), { headers })
    ])
    if (akResp.ok) {
      const d = await akResp.json()
      if (d.code === 200) apikeyBindings.value = (d.data.items || []).map((b) => ({ ...b, showSecret: false }))
    }
    if (oaResp.ok) {
      const d = await oaResp.json()
      if (d.code === 200) oauthBindings.value = d.data.items || []
    }
  } catch (e) {
    console.error('获取绑定失败:', e)
  } finally {
    bindingLoading.value = false
  }
}

export function useBindings() {
  return { apikeyBindings, oauthBindings, bindingLoading, loadBindings }
}
