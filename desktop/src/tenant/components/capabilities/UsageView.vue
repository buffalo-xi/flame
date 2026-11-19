<template>
  <!-- 引用明细弹窗：谁在引用这个 ref（agent 授权 / skill 依赖 / 自定义组成员）。
       用法：父组件 ref 到这里后调用 open(ref, prefix) -->
  <el-dialog v-model="visible" :title="title" width="560px" append-to-body>
    <div v-if="loading" class="u-tip">{{ t('common.loading') }}</div>
    <template v-else>
      <div v-if="!data || !data.total" class="u-tip">{{ t('refUsage.empty') }}</div>
      <template v-else>
        <div class="u-summary">{{ t('refUsage.summary', { n: data.total }) }}</div>

        <div v-if="(data.agents || []).length" class="u-section">
          <div class="u-title">{{ t('refUsage.agents', { n: data.agents.length }) }}</div>
          <div v-for="a in data.agents" :key="a.id + '|' + a.grant_kind + '|' + a.ref" class="u-row">
            <span class="u-name">{{ a.name }}</span>
            <span class="u-tag" :class="'kind-' + a.grant_kind">{{ grantKindLabel(a.grant_kind) }}</span>
            <span class="u-ref">{{ a.ref }}</span>
          </div>
        </div>

        <div v-if="(data.skills || []).length" class="u-section">
          <div class="u-title">{{ t('refUsage.skills', { n: data.skills.length }) }}</div>
          <div v-for="s in data.skills" :key="s.id + '|' + s.ref" class="u-row">
            <span class="u-name">{{ s.name }}</span>
            <span class="u-tag">{{ t('refUsage.tagSkill') }}</span>
            <span class="u-ref">{{ s.ref }}</span>
          </div>
        </div>

        <div v-if="(data.groups || []).length" class="u-section">
          <div class="u-title">{{ t('refUsage.groups', { n: data.groups.length }) }}</div>
          <div v-for="g in data.groups" :key="g.name + '|' + g.ref" class="u-row">
            <span class="u-name">{{ g.name }}</span>
            <span class="u-tag">{{ t('refUsage.tagGroup') }}</span>
            <span class="u-ref">{{ g.ref }}</span>
          </div>
        </div>
      </template>
    </template>
    <template #footer>
      <el-button @click="visible = false">{{ t('common.close') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import adminApi from '../../config/admin.js'
import { getToken } from '../../utils/auth.js'
import { formatError } from '../../../locales'

const { t } = useI18n()

const visible = ref(false)
const loading = ref(false)
const data = ref(null)
const currentRef = ref('')

const title = computed(() => (currentRef.value
  ? t('refUsage.titleNamed', { ref: currentRef.value })
  : t('refUsage.title')))

const GRANT_KIND_KEYS = { required: 'grantRequired', allow: 'grantAllow', deny: 'grantDeny' }
const grantKindLabel = (k) => (GRANT_KIND_KEYS[k] ? t(`refUsage.${GRANT_KIND_KEYS[k]}`) : k)

// 打开并加载某 ref 的引用明细（prefix=true 时连带子项，如 mcp:fs 也匹配 mcp:fs:read_file）
const open = async (refValue, prefix = false) => {
  currentRef.value = refValue
  visible.value = true
  loading.value = true
  data.value = null
  try {
    const res = await fetch(adminApi.toolGrants.usages(refValue, prefix), {
      headers: { Authorization: `Bearer ${getToken()}` }
    })
    const body = await res.json().catch(() => ({}))
    if (body.code !== 200) throw new Error(formatError(body) || t('refUsage.loadFailed'))
    data.value = body.data || null
  } catch (e) {
    ElMessage.error(e.message || t('refUsage.loadFailed'))
  } finally {
    loading.value = false
  }
}

defineExpose({ open })
</script>

<style scoped>
.u-tip {
  font-size: 13px;
  color: #909399;
  padding: 8px 0;
}

.u-summary {
  font-size: 13px;
  color: #303133;
  margin-bottom: 10px;
}

.u-section {
  margin-top: 14px;
}

.u-title {
  font-size: 13px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 6px;
}

.u-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 4px;
  font-size: 13px;
  color: #303133;
}

.u-row:hover {
  background: #f5f7fa;
}

.u-name {
  font-weight: 500;
}

.u-tag {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 3px;
  background: #f4f4f5;
  color: #909399;
}

.u-tag.kind-required {
  background: #ecf5ff;
  color: #409eff;
}

.u-tag.kind-allow {
  background: #f0f9eb;
  color: #67c23a;
}

.u-tag.kind-deny {
  background: #fef0f0;
  color: #f56c6c;
}

.u-ref {
  margin-left: auto;
  font-size: 12px;
  color: #909399;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
