<template>
  <div class="section">
    <!-- 整个页面就是两张卡：左 = 列表，右 = 详情（工具条收在左卡里面） -->
    <div class="split">
      <div class="list-pane">
        <!-- 计数 / 筛选 / 刷新 / 新建：列表卡内的第一行，不是独立卡头。
             控件尺寸与租户列表保持一致（small + 固定宽度 + 搜索带图标） -->
        <div class="lp-bar">
          <span class="lp-count">共 {{ total }} 个平台用户</span>
          <el-input
            v-model="filters.q"
            class="f-search"
            size="small"
            placeholder="搜索账号 / 显示名，回车搜索"
            clearable
            @keyup.enter="reload"
            @clear="reload"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
          <el-select v-model="filters.role" class="f-select" size="small" placeholder="全部角色" clearable @change="reload">
            <el-option label="管理员" value="admin" />
            <el-option label="用户" value="user" />
          </el-select>
          <el-select v-model="filters.status" class="f-select" size="small" placeholder="全部状态" clearable @change="reload">
            <el-option label="正常" value="active" />
            <el-option label="已停用" value="disabled" />
            <el-option label="已锁定" value="locked" />
          </el-select>
          <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>

          <div class="lp-bar-right">
            <el-button size="small" type="primary" :icon="Plus" @click="openCreate">新建用户</el-button>
          </div>
        </div>

        <!-- 卡片而不是表格：左栏被右侧 560px 详情压着，五列里「账号 + 标记」最容易挤；
             压成三行后「我 / 根账号」这类标记也不用再靠宽度硬撑 -->
        <div v-loading="loading" class="cards">
          <div
            v-for="row in items"
            :key="row.id"
            class="user-card"
            :class="{ 'is-active': selectedUser && selectedUser.id === row.id }"
            @click="select(row)"
          >
            <div class="uc-top">
              <span class="uc-name" :title="row.display_name || row.name">
                {{ row.display_name || row.name }}
              </span>
              <el-tag size="small" :type="row.role === 'admin' ? 'primary' : 'success'" effect="light">
                {{ roleText(row.role) }}
              </el-tag>
            </div>
            <div class="uc-sub">
              <span class="mono">{{ row.name }}</span>
              <el-tag v-if="row.id === meId" size="small" type="info">我</el-tag>
              <!-- 受保护管理员（固定 plat）：后端保护，不可修改 / 不可删除 -->
              <el-tag
                v-if="row.protected"
                size="small"
                type="warning"
                effect="plain"
                title="平台受保护管理员账号：不可修改、不可删除"
              >根账号</el-tag>
            </div>
            <div class="uc-meta">
              <el-tag size="small" :type="statusType(row.status)" effect="light">
                {{ statusText(row.status) }}
              </el-tag>
              <span class="uc-dot">·</span>
              <span>{{ row.role === 'user' ? `名下 ${row.tenant_count ?? 0} 个租户` : '不持有租户' }}</span>
            </div>
          </div>
          <el-empty v-if="!items.length && !loading" description="没有匹配的用户" />
        </div>

        <div class="pager">
          <el-pagination
            v-model:current-page="page"
            v-model:page-size="pageSize"
            :total="total"
            :page-sizes="[10, 20, 50, 100]"
            layout="total, sizes, prev, pager, next"
            background
            small
            @current-change="onPageChange"
            @size-change="onSizeChange"
          />
        </div>
      </div>

      <section class="detail-pane">
        <el-empty v-if="!selectedUser" description="左侧选择一个平台用户" />

        <template v-else>
          <!-- 头部：身份 + 启停（写操作，不混进「保存资料」） -->
          <div class="d-head">
            <div class="d-title">
              <span class="d-name">{{ selectedUser.display_name || selectedUser.name }}</span>
              <span class="mono d-sub">{{ selectedUser.name }}</span>
              <el-tag size="small" :type="selectedUser.role === 'admin' ? 'primary' : 'success'">
                {{ roleText(selectedUser.role) }}
              </el-tag>
              <el-tag size="small" :type="statusType(selectedUser.status)">{{ statusText(selectedUser.status) }}</el-tag>
            </div>
            <!-- 自己那一行后端必拒（会把自己踢下线），这里先禁用 -->
            <el-button
              size="small"
              :type="selectedUser.status === 'active' ? 'danger' : 'success'"
              :disabled="isSelf(selectedUser) || selectedUser.protected"
              :title="statusTitle(selectedUser)"
              @click="toggleStatus(selectedUser)"
            >
              {{ selectedUser.status === 'active' ? '停用' : '启用' }}
            </el-button>
          </div>

          <div class="meta-grid">
            <div class="meta-cell">
              <div class="meta-label">名下租户</div>
              <div class="meta-value">
                <template v-if="selectedUser.role === 'user'">{{ selectedUser.tenant_count ?? 0 }} 个</template>
                <template v-else>管理员管全部</template>
              </div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">创建时间</div>
              <div class="meta-value">{{ fmtTime(selectedUser.created_at) }}</div>
            </div>
            <div class="meta-cell">
              <div class="meta-label">更新时间</div>
              <div class="meta-value">{{ fmtTime(selectedUser.updated_at) }}</div>
            </div>
          </div>

          <!-- 可折叠卡片①：账号资料（取代原「编辑」弹窗） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.profile = !open.profile">
              <span class="blk-title">账号资料</span>
              <span class="blk-hint">账号不可改</span>
              <el-icon class="blk-arrow" :class="{ open: open.profile }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.profile" class="blk-body">
              <div class="form-grid">
                <label class="f-label">显示名</label>
                <el-input v-model="profileForm.display_name" size="small" placeholder="可留空（留空则显示账号名）" />

                <label class="f-label">账号</label>
                <el-input :model-value="selectedUser.name" size="small" disabled />

                <label class="f-label">角色</label>
                <el-radio-group v-model="profileForm.role" size="small">
                  <el-radio value="admin">管理员（管全部租户）</el-radio>
                  <el-radio value="user">用户（只管名下租户）</el-radio>
                </el-radio-group>

                <label class="f-label">状态</label>
                <el-select v-model="profileForm.status" size="small" style="width: 100%">
                  <el-option label="正常" value="active" />
                  <el-option label="停用" value="disabled" />
                  <el-option label="锁定" value="locked" />
                </el-select>
              </div>

              <div class="blk-foot">
                <span class="tip">{{ profileTip }}</span>
                <el-button
                  size="small"
                  type="primary"
                  :loading="savingProfile"
                  :disabled="selectedUser.protected"
                  @click="submitProfile"
                >保存资料</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片②：重置密码（取代原「重置密码」弹窗） -->
          <div class="blk">
            <button type="button" class="blk-head" @click="open.password = !open.password">
              <span class="blk-title">重置密码</span>
              <span class="blk-hint">管理员代为重置</span>
              <el-icon class="blk-arrow" :class="{ open: open.password }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.password" class="blk-body">
              <div class="form-grid">
                <label class="f-label">新密码</label>
                <el-input v-model="pwdForm.password" size="small" type="password" show-password placeholder="至少 8 位" />

                <label class="f-label">确认</label>
                <el-input
                  v-model="pwdForm.confirm"
                  size="small"
                  type="password"
                  show-password
                  placeholder="再输一次"
                  @keyup.enter="submitPassword"
                />
              </div>

              <div class="blk-foot">
                <span class="tip">
                  重置后该用户需用新密码登录；已签发的 token 不会失效，要立即踢下线请停用该账号。
                </span>
                <el-button size="small" type="primary" :loading="savingPwd" @click="submitPassword">保存密码</el-button>
              </div>
            </div>
          </div>

          <!-- 可折叠卡片③：名下租户（只读；管理员不绑定租户，不出这块） -->
          <div v-if="selectedUser.role === 'user'" class="blk">
            <button type="button" class="blk-head" @click="open.tenants = !open.tenants">
              <span class="blk-title">名下租户</span>
              <span class="blk-hint">{{ selectedUser.tenant_count ?? userTenants.length }} 个</span>
              <el-icon class="blk-arrow" :class="{ open: open.tenants }"><ArrowRight /></el-icon>
            </button>
            <div v-show="open.tenants" class="blk-body">
              <div v-loading="tenantsLoading" class="tn-list">
                <div v-if="!tenantsLoading && !userTenants.length" class="tip">无（管理员代管）</div>
                <div v-for="t in userTenants" :key="t.schema" class="tn-row">
                  <span class="mono tn-schema">{{ t.schema }}</span>
                  <span class="tn-name">{{ t.display_name || t.schema }}</span>
                  <el-tag size="small" :type="tenantStatusType(t.status)">{{ tenantStatusText(t.status) }}</el-tag>
                </div>
              </div>
              <div class="blk-foot">
                <span class="tip">只读：要转移归属，请到「租户管理」里改该租户的负责人。</span>
              </div>
            </div>
          </div>
        </template>
      </section>
    </div>

    <!-- 新建平台用户 -->
    <el-dialog v-model="createVisible" title="新建平台用户" width="460px">
      <el-form :model="createForm" label-width="86px">
        <el-form-item label="账号">
          <el-input v-model="createForm.name" placeholder="字母/数字/下划线/横线，1-64 位" />
        </el-form-item>
        <el-form-item label="显示名">
          <el-input v-model="createForm.display_name" placeholder="可选（留空则显示账号名）" />
        </el-form-item>
        <el-form-item label="角色">
          <el-radio-group v-model="createForm.role">
            <el-radio value="admin">管理员（管所有租户）</el-radio>
            <el-radio value="user">用户（只管自己名下租户）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="初始密码">
          <el-input v-model="createForm.password" type="password" show-password placeholder="至少 8 位" />
          <div class="tip">缺省 {{ DEFAULT_PASSWORD }}，可改；建议用户首次登录后立即修改</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowRight, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../api'
import { getUser } from '../auth'

const emit = defineEmits(['count', 'session-expired'])

const items = ref([])
const loading = ref(false)
const saving = ref(false)
const savingProfile = ref(false)
const savingPwd = ref(false)
const filters = reactive({ q: '', role: '', status: '' })

// 后端真分页：page 从 1 起，page_size 由分页器给，total 用接口回的总数（不是 items.length）
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

// 选中项存整行快照：翻页后它可能不在当前页，不能再从 items 里 find
const selectedUser = ref(null)

// 详情卡里三张折叠卡片的展开状态（重置密码默认收起，避免手滑）
const open = reactive({ profile: true, password: false, tenants: true })

// 当前登录账号 id：用于「不能停用自己」的前端禁用（后端还会拦一道）
const meId = getUser()?.id || ''

const createVisible = ref(false)
const createForm = reactive({ name: '', display_name: '', role: 'user', password: '' })
// 新建用户时初始密码的缺省值：仅前端预填，操作者仍可改；用户首次登录后建议立即改密
const DEFAULT_PASSWORD = '12345678'

// 详情卡的两份编辑态：资料（显示名/角色/状态）与重置密码
const profileForm = reactive({ display_name: '', role: 'user', status: 'active' })
const pwdForm = reactive({ password: '', confirm: '' })

const userTenants = ref([])
const tenantsLoading = ref(false)

const ROLE_TEXT = { admin: '管理员', user: '用户' }
const roleText = (r) => ROLE_TEXT[r] || r
const statusText = (s) => ({ active: '正常', disabled: '已停用', locked: '已锁定' }[s] || s)
const statusType = (s) => ({ active: 'success', disabled: 'info', locked: 'warning' }[s] || 'info')
const tenantStatusText = (s) => ({ active: '正常', suspended: '已停用', deleted: '已删除', pending: '待完成' }[s] || s)
const tenantStatusType = (s) => ({ active: 'success', suspended: 'warning', deleted: 'info', pending: 'info' }[s] || 'info')

const isSelf = (u) => !!u && !!meId && u.id === meId

// 卡头「停用/启用」按钮不可用的原因（空串 = 可用）：
// 自己（后端必拒，会把自己踢下线）/ 受保护管理员（protected，后端同样拒）
const statusTitle = (u) => {
  if (!u) return ''
  if (isSelf(u)) return '不能停用当前登录账号'
  if (u.protected) return '平台受保护管理员账号，不可修改'
  return ''
}

const fmtTime = (s) => {
  if (!s) return '—'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return String(s).replace('T', ' ').slice(0, 16)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function handleError(err) {
  if (err?.status === 401) {
    emit('session-expired')
    return
  }
  ElMessage.error(err?.message || '请求失败')
}

// 把行数据灌进资料编辑态（重置密码的输入框不在这里：保存后就清空，不跟随选中项）
function syncForm(u) {
  if (!u) return
  profileForm.display_name = u.display_name || ''
  profileForm.role = u.role || 'user'
  profileForm.status = u.status || 'active'
}

// 点表格行：选中（存快照，翻页后卡片不会变空）+ 填充编辑态
function select(row) {
  if (!row || row.id === selectedUser.value?.id) return
  selectedUser.value = row
  syncForm(row)
}

// 选中项一变就拉它的名下租户（只对 role=user 有意义）
watch(
  () => selectedUser.value?.id,
  (id) => {
    if (id) loadUserTenants(id)
  }
)

// 筛选条件变了要回到第 1 页
function reload() {
  page.value = 1
  load()
}

function onPageChange(p) {
  page.value = p
  load()
}

function onSizeChange() {
  page.value = 1
  load()
}

async function load() {
  loading.value = true
  try {
    const data = await api.listUsers({
      q: filters.q,
      role: filters.role,
      status: filters.status,
      page: page.value,
      page_size: pageSize.value
    })
    items.value = data?.items || []
    total.value = data?.total ?? items.value.length

    // 选中项还在本页就同步成新数据；本页没有它则保留快照（卡片不闪空）
    const hit = items.value.find((u) => u.id === selectedUser.value?.id)
    if (hit) {
      selectedUser.value = hit
      syncForm(hit)
    } else if (!selectedUser.value) {
      select(items.value[0])
    }

    // 顶栏徽标用总数，不是本页条数
    emit('count', total.value)
  } catch (err) {
    handleError(err)
  } finally {
    loading.value = false
  }
}

// 某用户名下的租户（只读；admin 不绑定租户，后端会回空列表）
async function loadUserTenants(id) {
  tenantsLoading.value = true
  try {
    const data = await api.getUserTenants(id)
    userTenants.value = data?.items || []
  } catch (err) {
    userTenants.value = []
    handleError(err)
  } finally {
    tenantsLoading.value = false
  }
}

function openCreate() {
  createForm.name = ''
  createForm.display_name = ''
  createForm.role = 'user'
  createForm.password = DEFAULT_PASSWORD
  createVisible.value = true
}

async function submitCreate() {
  if (!createForm.name || !createForm.password) {
    ElMessage.error('账号与初始密码必填')
    return
  }
  saving.value = true
  try {
    await api.createUser({
      name: createForm.name.trim(),
      display_name: createForm.display_name.trim(),
      role: createForm.role,
      password: createForm.password
    })
    ElMessage.success('已创建')
    createVisible.value = false
    await load()
  } catch (err) {
    handleError(err)
  } finally {
    saving.value = false
  }
}

// 资料区的说明：受保护账号只读；其余把两条边界讲清 ——
// 降级/停用的连带影响，以及「锁定」和「停用」到底差在哪（登录侧效果一样，只是标签不同）。
const profileTip = computed(() =>
  selectedUser.value?.protected
    ? '平台受保护管理员账号：不可修改（资料 / 角色 / 状态）。'
    : '不能停用或降级自己；也不能停用或降级最后一个「正常」的管理员（后端拦）。' +
      '角色决定权限范围，改动立即生效：管理员能管全部租户，用户只能管名下租户；' +
      '降级后其名下租户不会自动转让，需要你手动改负责人。' +
      '「锁定」与「停用」对登录的拦截相同（都拒登），只是标签不同；卡头的「启用」对两者都直接回到正常。'
)

// 保存资料：显示名 / 角色 / 状态一起提交（与卡头「停用/启用」走同一个接口）
async function submitProfile() {
  const u = selectedUser.value
  if (!u) return
  if (u.protected) return ElMessage.warning('平台受保护管理员账号，不可修改')
  // 角色一改就是权限范围的变化，且不等下次登录 —— 降级前先说清名下租户怎么办
  if (u.role !== profileForm.role) {
    const to = profileForm.role === 'admin' ? '管理员（能管全部租户）' : '用户（只能管名下租户）'
    try {
      await ElMessageBox.confirm(
        `把「${u.name}」从「${roleText(u.role)}」改为「${to}」？权限立即变化。` +
          (profileForm.role === 'user' ? '其名下租户不会自动转让，降级后需要你手动改负责人。' : ''),
        '修改角色',
        { type: 'warning', confirmButtonText: '确认修改', cancelButtonText: '取消' }
      )
    } catch {
      return
    }
  }
  savingProfile.value = true
  try {
    const data = await api.updateUser(u.id, {
      display_name: profileForm.display_name.trim(),
      role: profileForm.role,
      status: profileForm.status
    })
    // 停用"名下有租户"的用户时后端会带回提示（不级联、不自动转移）
    if (data?.warning) {
      ElMessage({ message: data.warning, type: 'warning', duration: 2000 })
    } else {
      ElMessage.success('资料已保存')
    }
    await load()
  } catch (err) {
    handleError(err)
  } finally {
    savingProfile.value = false
  }
}

async function submitPassword() {
  const u = selectedUser.value
  if (!u) return
  const pwd = pwdForm.password
  if (!pwd || pwd.length < 8) {
    ElMessage.error('新密码至少 8 位')
    return
  }
  if (pwd !== pwdForm.confirm) {
    ElMessage.error('两次输入的新密码不一致')
    return
  }
  savingPwd.value = true
  try {
    await api.resetUserPassword(u.id, pwd)
    ElMessage.success('密码已重置，该用户需用新密码重新登录')
    pwdForm.password = ''
    pwdForm.confirm = ''
  } catch (err) {
    handleError(err)
  } finally {
    savingPwd.value = false
  }
}

// 卡头快捷开关：只在 正常 ⇄ 停用 之间切（locked 视为"非正常"，点启用直接回 active）
async function toggleStatus(u) {
  if (!u) return
  if (u.protected) return ElMessage.warning('平台受保护管理员账号，不可修改')
  const next = u.status === 'active' ? 'disabled' : 'active'
  const action = next === 'disabled' ? '停用' : '启用'
  try {
    await ElMessageBox.confirm(
      next === 'disabled'
        ? `停用 ${u.name} 后将无法登录平台管理台。其名下租户不会自动转让，也不会随之停用 —— ` +
          '但该账号再也进不去，等于没人能自助管理，需要你事后到「租户管理」里改负责人。'
        : `启用 ${u.name}？锁定与停用都会直接回到「正常」。`,
      `${action}确认`,
      { type: next === 'disabled' ? 'warning' : 'info' }
    )
  } catch {
    return
  }
  try {
    const data = await api.updateUser(u.id, {
      display_name: u.display_name || '',
      role: u.role,
      status: next
    })
    if (data?.warning) {
      ElMessage({ message: data.warning, type: 'warning', duration: 2000 })
    } else {
      ElMessage.success(`已${action}`)
    }
    await load()
  } catch (err) {
    handleError(err)
  }
}

onMounted(load)
</script>

<style scoped>
/* ===== 三层灰阶（与租户管理页同款）=====
   大背景：#f0f2f5（与平台外壳同色，整页一块浅灰）
   两张灰卡：.list-pane / .detail-pane（#fafafa）
   内容卡：.user-card / .blk 白底 + 边框 + 投影 */
.section {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  box-sizing: border-box;
  overflow: hidden;
  background: #f0f2f5;
  border-radius: 12px;
}

/* 列表卡内的控制行：计数 / 筛选 / 刷新 / 新建（不是独立卡头，跟表格同处一张卡） */
.lp-bar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding-bottom: 10px;
}

.lp-count {
  font-size: 13px;
  color: #909399;
  white-space: nowrap;
}

/* 筛选控件宽固定：选项文案长短不会把这行挤变形（与租户列表同款） */
.f-select {
  width: 96px;
  flex-shrink: 0;
}

.f-search {
  width: 130px;
  flex-shrink: 0;
}

.lp-bar-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 10px;
}

/* ===== 主从布局：左右各一张灰卡（与租户管理同款：上下 6px、左右贴边、卡间 6px） ===== */
.split {
  flex: 1;
  min-height: 0;
  display: flex;
  gap: 6px;
  padding: 6px 0;
}

/* 左：列表灰卡（自由宽，含分页器）。比大背景提亮一档，白卡再浮在它上面 */
.list-pane {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  padding: 8px 8px 4px;
  box-sizing: border-box;
  overflow: hidden;
  background: #fafafa;
  border-radius: 12px;
}

/* 卡片队列：撑满剩余高度自己滚；间距对齐租户管理（14px）。
   底色交给灰卡，滚动条位置常驻、拇指透明 → 悬浮才上色 */
.cards {
  flex: 1;
  min-height: 0;
  overflow: auto;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  align-content: start;
  gap: 14px;
  padding: 0;
  scrollbar-gutter: stable;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.cards::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.cards::-webkit-scrollbar-track {
  background: transparent;
}

.cards::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 3px;
}

.cards:hover {
  scrollbar-color: #c1c1c1 transparent;
}

.cards:hover::-webkit-scrollbar-thumb {
  background: #c1c1c1;
}

.cards :deep(.el-empty) {
  grid-column: 1 / -1;
}

/* 一张卡三行：显示名 + 角色 / 账号 + 标记 / 状态 + 名下租户。
   盒模型对齐租户卡片：白底、灰边、12px 圆角、常驻投影 */
.user-card {
  padding: 16px;
  background: #fff;
  border: 1px solid #e8e8e8;
  border-radius: 12px;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
  cursor: pointer;
  width: 100%;
  min-width: 200px;
  max-width: 250px;
  transition: all 0.3s ease;
}

.user-card:hover {
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
}

/* 卡片没有选中态样式可借，这里保留蓝边 + 浅蓝底，否则在灰底上看不出选的是哪张 */
.user-card.is-active {
  border-color: #409eff;
  background: #ecf5ff;
  box-shadow: 0 2px 8px rgba(64, 158, 255, 0.18);
}

.uc-top {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.uc-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 字号 / 颜色与租户卡片的第二行对齐：12px + #909399，账号用等宽但同样淡色 */
.uc-sub {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
  min-width: 0;
}

.uc-sub .mono {
  min-width: 0;
  color: inherit;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.uc-meta {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 6px;
  font-size: 12px;
  color: #909399;
  min-width: 0;
}

.uc-meta > span:not(.uc-dot) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.uc-dot {
  flex-shrink: 0;
  color: #dcdfe6;
}

.pager {
  flex-shrink: 0;
  display: flex;
  justify-content: flex-end;
  padding-top: 10px;
}

/* 右：详情灰卡（固定宽，与租户管理同宽 —— 两页布局保持一致，切换时不会晃）。
   灰卡上的 .blk 是白块，层次与租户管理详情栏一致 */
.detail-pane {
  width: 560px;
  flex-shrink: 0;
  overflow: auto;
  padding: 10px 12px;
  box-sizing: border-box;
  background: #fafafa;
  border-radius: 12px;
}

/* 卡片滚动条：位置常驻（一直是 6px），只是拇指透明 → 悬浮才上色。
   这样悬浮时不会因为"条突然出现"而让内容横向跳动。 */
.detail-pane {
  scrollbar-width: thin; /* 常驻细条：占位固定 */
  scrollbar-color: transparent transparent; /* 不悬浮：看不见，但占位 */
}

.detail-pane::-webkit-scrollbar {
  width: 6px;
  height: 6px;
}

.detail-pane::-webkit-scrollbar-track {
  background: transparent;
}

.detail-pane::-webkit-scrollbar-thumb {
  background: transparent; /* 不悬浮：看不见，但占位 */
  border-radius: 3px;
}

.detail-pane:hover {
  scrollbar-color: rgba(144, 147, 153, 0.4) transparent;
}

.detail-pane:hover::-webkit-scrollbar-thumb {
  background: rgba(144, 147, 153, 0.4);
}

.d-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.d-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
}

.d-name {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.d-sub {
  font-size: 12px;
  color: #909399;
}

/* 三格信息：两列网格，日期放得下（租户页是两格 flex，这里格子多） */
.meta-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px 16px;
  margin-top: 14px;
  padding: 12px 0;
  border-top: 1px solid #f0f2f5;
  border-bottom: 1px solid #f0f2f5;
}

.meta-label {
  font-size: 12px;
  color: #909399;
  margin-bottom: 4px;
}

.meta-value {
  font-size: 14px;
  color: #303133;
}

/* ===== 可折叠卡片：一个区块一张卡（灰底上要靠白底立起来） ===== */
.blk {
  margin-top: 12px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  overflow: hidden;
}

.blk-head {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  background: #fafafa;
  border: none;
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s;
}

.blk-head:hover {
  background: #f5f7fa;
}

.blk-title {
  font-size: 13px;
  font-weight: 500;
  color: #303133;
}

.blk-hint {
  font-size: 12px;
  color: #a8abb2;
}

.blk-arrow {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
  transition: transform 0.2s;
}

.blk-arrow.open {
  transform: rotate(90deg);
}

.blk-body {
  padding: 12px;
}

/* 卡片底部：tip 靠左、按钮靠右 */
.blk-foot {
  display: flex;
  align-items: center;
  gap: 10px;
  justify-content: flex-end;
  margin-top: 12px;
}

.blk-foot .tip {
  margin-right: auto;
}

/* 资料 / 重置密码：左标签 + 右控件 */
.form-grid {
  display: grid;
  grid-template-columns: 56px 1fr;
  align-items: center;
  gap: 10px;
}

.f-label {
  font-size: 13px;
  color: #606266;
}

/* 名下租户：只读列表 */
.tn-list {
  min-height: 24px;
}

.tn-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
}

.tn-row + .tn-row {
  border-top: 1px solid #f0f2f5;
}

.tn-schema {
  font-size: 12px;
  color: #909399;
}

.tn-name {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mono {
  font-family: Consolas, Monaco, monospace;
  color: #303133;
}

.muted {
  color: #a8abb2;
}

.tip {
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}
</style>
