<template>
  <div class="domain">
    <div class="d-sub">
      全局共享：租户在「设置 → LLM 配置」点预设时，会把名称 / 协议 / URL <strong>复制</strong>进自己的服务商记录，
      所以删除预设不会影响已经复制过的租户配置。
    </div>

    <div class="preset-bar">
      <span class="bar-count">共 {{ items.length }} 个预设</span>
      <el-button size="small" type="primary" :icon="Plus" @click="openCreate">新建预设</el-button>
    </div>

    <el-table v-loading="loading" :data="items" size="small" style="width: 100%">
      <el-table-column label="名称" min-width="190">
        <template #default="{ row }">
          <div class="p-name">
            <span class="p-logo">
              <img v-if="row.icon" :src="resolveAsset(row.icon)" alt="" />
              <el-icon v-else><ChatDotRound /></el-icon>
            </span>
            <span class="p-text">{{ row.name }}</span>
            <el-tag v-if="row.is_popular" size="small" type="warning" effect="light">热门</el-tag>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="protocol" label="协议" width="120" />
      <el-table-column prop="url" label="URL" min-width="200" show-overflow-tooltip />
      <el-table-column prop="description" label="描述" min-width="140" show-overflow-tooltip />
      <el-table-column prop="sort_order" label="排序" width="70" align="center" />
      <el-table-column label="操作" width="110" align="right">
        <template #default="{ row }">
          <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
      <template #empty>暂无预设，点右上角「新建预设」添加</template>
    </el-table>

    <div class="tip">
      协议可选 vertexai / anthropic / googleai / openai，也可直接输入自定义值；图标填路径（如
      /static/llm/openai.png）或完整 URL，留空则用默认图标。列表按「热门优先 → 排序值升序」展示。
    </div>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑预设' : '新建预设'" width="520px">
      <el-form label-width="72px">
        <el-form-item label="名称">
          <el-input v-model="form.name" maxlength="100" placeholder="如 OpenAI" />
        </el-form-item>
        <el-form-item label="协议">
          <el-select
            v-model="form.protocol"
            filterable
            allow-create
            default-first-option
            placeholder="选择或输入"
            style="width: 100%"
          >
            <el-option v-for="p in PROTOCOLS" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>
        <el-form-item label="URL">
          <el-input v-model="form.url" placeholder="如 https://api.openai.com/v1" />
        </el-form-item>
        <el-form-item label="图标">
          <el-input v-model="form.icon" placeholder="如 /static/llm/openai.png" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" type="textarea" :rows="2" placeholder="一句话说明（可选）" />
        </el-form-item>
        <el-form-item label="热门">
          <el-switch v-model="form.is_popular" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="form.sort_order" :min="-9999" :max="9999" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, Plus } from '@element-plus/icons-vue'
import { api, resolveAsset } from '../../api'

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
    ElMessage.error(err?.message || '加载失败')
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
  if (!form.name.trim()) return ElMessage.error('名称不能为空')
  if (!form.protocol.trim()) return ElMessage.error('协议不能为空')
  if (!form.url.trim()) return ElMessage.error('URL 不能为空')

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
      ElMessage.success('预设已更新')
    } else {
      await api.createLlmPreset(body)
      ElMessage.success('预设已创建')
    }
    dialogVisible.value = false
    await load()
  } catch (err) {
    ElMessage.error(err?.message || '保存失败')
  } finally {
    saving.value = false
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(
      `删除预设「${row.name}」？已经复制过该预设的租户不受影响（它们存的是自己的副本）。`,
      '删除预设',
      { type: 'warning', confirmButtonText: '删除' }
    )
  } catch {
    return
  }
  try {
    await api.deleteLlmPreset(row.id)
    ElMessage.success('已删除')
    await load()
  } catch (err) {
    ElMessage.error(err?.message || '删除失败')
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
