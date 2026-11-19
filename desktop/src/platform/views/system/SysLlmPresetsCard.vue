<template>
  <div class="domain">
    <div class="d-sub">
      {{ t('platform.system.llmPresets.intro1') }} <strong>{{ t('platform.system.llmPresets.copyWord') }}</strong>{{ t('platform.system.llmPresets.intro2') }}
    </div>

    <div class="preset-bar">
      <span class="bar-count">{{ t('platform.system.llmPresets.barCount', { count: items.length }) }}</span>
      <el-button size="small" type="primary" :icon="Plus" @click="openCreate">{{ t('platform.system.llmPresets.newPreset') }}</el-button>
    </div>

    <el-table v-loading="loading" :data="items" size="small" style="width: 100%">
      <el-table-column :label="t('platform.system.llmPresets.colName')" min-width="190">
        <template #default="{ row }">
          <div class="p-name">
            <span class="p-logo">
              <img v-if="row.icon" :src="resolveAsset(row.icon)" alt="" />
              <el-icon v-else><ChatDotRound /></el-icon>
            </span>
            <span class="p-text">{{ row.name }}</span>
            <el-tag v-if="row.is_popular" size="small" type="warning" effect="light">{{ t('platform.system.llmPresets.popular') }}</el-tag>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="protocol" :label="t('platform.system.llmPresets.colProtocol')" width="120" />
      <el-table-column prop="url" label="URL" min-width="200" show-overflow-tooltip />
      <el-table-column prop="description" :label="t('platform.system.llmPresets.colDesc')" min-width="140" show-overflow-tooltip />
      <el-table-column prop="sort_order" :label="t('platform.system.llmPresets.colSort')" width="70" align="center" />
      <el-table-column :label="t('platform.system.llmPresets.colActions')" width="110" align="right">
        <template #default="{ row }">
          <el-button link type="primary" @click="openEdit(row)">{{ t('common.edit') }}</el-button>
          <el-button link type="danger" @click="remove(row)">{{ t('common.delete') }}</el-button>
        </template>
      </el-table-column>
      <template #empty>{{ t('platform.system.llmPresets.empty') }}</template>
    </el-table>

    <div class="tip">
      {{ t('platform.system.llmPresets.tip') }}
    </div>

    <el-dialog v-model="dialogVisible" :title="editingId ? t('platform.system.llmPresets.dialogEdit') : t('platform.system.llmPresets.dialogCreate')" width="520px">
      <el-form label-width="72px">
        <el-form-item :label="t('platform.system.llmPresets.labelName')">
          <el-input v-model="form.name" maxlength="100" :placeholder="t('platform.system.llmPresets.phName')" />
        </el-form-item>
        <el-form-item :label="t('platform.system.llmPresets.labelProtocol')">
          <el-select
            v-model="form.protocol"
            filterable
            allow-create
            default-first-option
            :placeholder="t('platform.system.llmPresets.phProtocol')"
            style="width: 100%"
          >
            <el-option v-for="p in PROTOCOLS" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>
        <el-form-item label="URL">
          <el-input v-model="form.url" :placeholder="t('platform.system.llmPresets.phUrl')" />
        </el-form-item>
        <el-form-item :label="t('platform.system.llmPresets.labelIcon')">
          <el-input v-model="form.icon" :placeholder="t('platform.system.llmPresets.phIcon')" />
        </el-form-item>
        <el-form-item :label="t('platform.system.llmPresets.labelDesc')">
          <el-input v-model="form.description" type="textarea" :rows="2" :placeholder="t('platform.system.llmPresets.phDesc')" />
        </el-form-item>
        <el-form-item :label="t('platform.system.llmPresets.labelPopular')">
          <el-switch v-model="form.is_popular" />
        </el-form-item>
        <el-form-item :label="t('platform.system.llmPresets.labelSort')">
          <el-input-number v-model="form.sort_order" :min="-9999" :max="9999" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="saving" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, Plus } from '@element-plus/icons-vue'
import { api, resolveAsset } from '../../api'

const { t } = useI18n()

// 协议预置项：可选，也可直接输入自定义值（表上 protocol 无 CHECK 约束，只管长度 ≤50）
const PROTOCOLS = ['vertexai', 'anthropic', 'googleai', 'openai']

const loading = ref(false)
const saving = ref(false)
const items = ref([])

const dialogVisible = ref(false)
const editingId = ref('')
const form = reactive({ name: '', icon: '', description: '', protocol: '', url: '', is_popular: false, sort_order: 0 })

const str = (v) => (v === null || v === undefined ? '' : String(v))

async function load() {
  loading.value = true
  try {
    const data = await api.listLlmPresets()
    items.value = data?.items || []
  } catch (err) {
    ElMessage.error(err?.message || t('platform.system.card.loadFailed'))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = ''
  Object.assign(form, { name: '', icon: '', description: '', protocol: '', url: '', is_popular: false, sort_order: 0 })
  dialogVisible.value = true
}

function openEdit(row) {
  editingId.value = row.id
  Object.assign(form, {
    name: str(row.name),
    icon: str(row.icon),
    description: str(row.description),
    protocol: str(row.protocol),
    url: str(row.url),
    is_popular: !!row.is_popular,
    sort_order: Number(row.sort_order) || 0
  })
  dialogVisible.value = true
}

async function submit() {
  if (!form.name.trim()) return ElMessage.error(t('platform.system.llmPresets.nameRequired'))
  if (!form.protocol.trim()) return ElMessage.error(t('platform.system.llmPresets.protocolRequired'))
  if (!form.url.trim()) return ElMessage.error(t('platform.system.llmPresets.urlRequired'))

  const body = {
    name: form.name.trim(),
    icon: form.icon.trim(),
    description: form.description,
    protocol: form.protocol.trim(),
    url: form.url.trim(),
    is_popular: form.is_popular,
    sort_order: Number(form.sort_order) || 0
  }

  saving.value = true
  try {
    if (editingId.value) {
      await api.updateLlmPreset(editingId.value, body)
      ElMessage.success(t('platform.system.llmPresets.updated'))
    } else {
      await api.createLlmPreset(body)
      ElMessage.success(t('platform.system.llmPresets.created'))
    }
    dialogVisible.value = false
    await load()
  } catch (err) {
    ElMessage.error(err?.message || t('platform.system.card.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(
      t('platform.system.llmPresets.deleteConfirm', { name: row.name }),
      t('platform.system.llmPresets.deleteTitle'),
      { type: 'warning', confirmButtonText: t('common.delete') }
    )
  } catch {
    return
  }
  try {
    await api.deleteLlmPreset(row.id)
    ElMessage.success(t('platform.system.llmPresets.deleted'))
    await load()
  } catch (err) {
    ElMessage.error(err?.message || t('platform.system.card.deleteFailed'))
  }
}

onMounted(load)
</script>

<style scoped>
.d-sub {
  margin-bottom: 10px;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}

.preset-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.bar-count {
  flex: 1;
  font-size: 12px;
  color: #909399;
}

.p-name {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.p-logo {
  width: 18px;
  height: 18px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #909399;
}

.p-logo img {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.p-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tip {
  margin-top: 10px;
  font-size: 12px;
  color: #909399;
  line-height: 1.6;
}
</style>
