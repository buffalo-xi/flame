<!--
  What the selected tool (or endpoint) actually expects: description, one row per declared argument, and the raw schema.
  Opened from the tool / endpoint rows of the MCP and RPC properties panels, so the author can read the contract without
  leaving the flow editor or guessing keys for the arguments box.
-->
<template>
  <el-dialog
    :model-value="modelValue"
    class="tool-detail-dialog"
    width="600px"
    append-to-body
    @update:model-value="(open) => emit('update:modelValue', open)"
  >
    <template #header>
      <div class="td-header">
        <el-icon class="td-header-icon"><Tools /></el-icon>
        <span class="td-header-name">{{ subjectName || t('flow.toolDetail.empty') }}</span>
      </div>
    </template>

    <div v-if="subject" class="td-body">
      <p class="td-desc" :class="{ 'td-desc-empty': !subject.description }">{{ description }}</p>

      <div v-if="meta.length" class="td-meta-list">
        <div v-for="row in meta" :key="row.label" class="td-meta">
          <span class="td-meta-label">{{ row.label }}</span>
          <span class="td-meta-value">{{ row.value }}</span>
        </div>
      </div>

      <div class="td-section">{{ t('flow.toolDetail.params') }}</div>
      <table v-if="params.length" class="td-table">
        <thead>
          <tr>
            <th class="td-col-name">{{ t('flow.toolDetail.colName') }}</th>
            <th class="td-col-type">{{ t('flow.toolDetail.colType') }}</th>
            <th class="td-col-required">{{ t('flow.toolDetail.colRequired') }}</th>
            <th>{{ t('flow.toolDetail.colDescription') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in params" :key="p.name">
            <td class="td-param-name">{{ p.name }}</td>
            <td class="td-param-type">{{ p.type }}</td>
            <td :class="p.required ? 'td-required' : 'td-optional'">
              {{ p.required ? t('flow.toolDetail.yes') : t('flow.toolDetail.no') }}
            </td>
            <td>
              <span v-if="p.description">{{ p.description }}</span>
              <span v-if="p.enums.length" class="td-enum">{{ t('flow.toolDetail.enum') }}: {{ p.enums.join(' / ') }}</span>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="td-empty">{{ t('flow.toolDetail.noParams') }}</p>

      <template v-if="schemaText">
        <div class="td-section">{{ t('flow.toolDetail.rawSchema') }}</div>
        <pre class="td-schema">{{ schemaText }}</pre>
      </template>
    </div>

    <p v-else class="td-empty">{{ t('flow.toolDetail.empty') }}</p>

    <template #footer>
      <el-button size="small" @click="emit('update:modelValue', false)">{{ t('flow.toolDetail.close') }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Tools } from '@element-plus/icons-vue'
import { toolSchema } from './toolSample.js'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  // A tool of a connection or an exposed endpoint: both carry name / description / input_schema.
  subject: { type: Object, default: null },
  // Extra label/value rows under the description (owning connection, endpoint target type, exposure).
  meta: { type: Array, default: () => [] }
})

const emit = defineEmits(['update:modelValue'])

const { t } = useI18n()

const subjectName = computed(() => props.subject?.name || props.subject?.tool_name || '')

const schema = computed(() => toolSchema(props.subject))

const schemaText = computed(() => (schema.value ? JSON.stringify(schema.value, null, 2) : ''))

const description = computed(() => props.subject?.description || t('flow.toolDetail.noDescription'))

// A declared type is shown as written; anything structural falls back to its shape so a row is never blank.
const typeLabel = (def) => {
  if (!def || typeof def !== 'object') return 'any'
  if (Array.isArray(def.type)) return def.type.join(' | ')
  if (def.type) return String(def.type)
  if (def.properties) return 'object'
  if (def.items) return 'array'
  if (def.anyOf || def.oneOf) return 'union'
  return 'any'
}

const params = computed(() => {
  const properties = schema.value?.properties
  if (!properties || typeof properties !== 'object') return []
  const required = Array.isArray(schema.value.required) ? schema.value.required : []
  return Object.keys(properties).map(name => {
    const def = properties[name]
    return {
      name,
      type: typeLabel(def),
      required: required.includes(name),
      description: def?.description || '',
      enums: Array.isArray(def?.enum) ? def.enum.map(v => String(v)) : []
    }
  })
})
</script>

<style scoped>
.td-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: #333;
}
.td-header-icon { font-size: 16px; color: #1890ff; }
.td-header-name { word-break: break-all; }

.td-body { display: flex; flex-direction: column; gap: 10px; }
.td-desc {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  color: #333;
  white-space: pre-wrap;
  word-break: break-word;
}
.td-desc-empty { color: #999; }

.td-meta-list { display: flex; flex-direction: column; gap: 4px; }
.td-meta { display: flex; align-items: center; gap: 8px; font-size: 12px; }
.td-meta-label { color: #909399; min-width: 72px; }
.td-meta-value { color: #333; }

.td-section {
  font-size: 12px;
  font-weight: 600;
  color: #666;
  padding-bottom: 6px;
  border-bottom: 1px solid #ebeef5;
}

.td-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  line-height: 1.5;
}
.td-table th {
  text-align: left;
  font-weight: 500;
  color: #909399;
  padding: 6px 8px;
  background: #fafafa;
  border-bottom: 1px solid #ebeef5;
  white-space: nowrap;
}
.td-table td {
  padding: 6px 8px;
  color: #333;
  border-bottom: 1px solid #f2f2f2;
  vertical-align: top;
  word-break: break-word;
}
.td-col-name { width: 30%; }
.td-col-type { width: 18%; }
.td-col-required { width: 12%; }
.td-param-name { font-family: Monaco, Menlo, monospace; color: #c7254e; }
.td-param-type { font-family: Monaco, Menlo, monospace; color: #1890ff; }
.td-required { color: #f56c6c; }
.td-optional { color: #909399; }
.td-enum {
  display: block;
  margin-top: 2px;
  font-family: Monaco, Menlo, monospace;
  color: #b8860b;
}

.td-schema {
  margin: 0;
  max-height: 220px;
  overflow: auto;
  scrollbar-gutter: stable;
  padding: 8px 10px;
  background: #fafafa;
  border-radius: 4px;
  font-size: 12px;
  line-height: 1.5;
  font-family: Monaco, Menlo, monospace;
  color: #333;
  white-space: pre-wrap;
  word-break: break-all;
  /* Bar space stays reserved, the thumb is painted only while the pointer is over the block
     (same rule as .et-pre / .ug-pre / the help drawer) */
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.td-schema::-webkit-scrollbar { width: 6px; height: 6px; }
.td-schema::-webkit-scrollbar-track { background: transparent; }
.td-schema::-webkit-scrollbar-thumb { background: transparent; border-radius: 3px; }
.td-schema:hover { scrollbar-color: #c1c1c1 transparent; }
.td-schema:hover::-webkit-scrollbar-thumb { background: #c1c1c1; }

.td-empty {
  margin: 0;
  font-size: 12px;
  color: #999;
  text-align: center;
  padding: 10px 0;
}
</style>
