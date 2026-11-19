// 数据类型徽标的唯一实现：type → 单字母（S/N/B/O）、type → CSS 类（type-string …）、type → 可读名。
// 纯模块没有组件上下文，文案走全局 i18n 实例（与 settings/format.js 同一做法）。
//
// 背景：这套表原先在 12 个面板里各有一份（Assign/Rpc/Mcp/Http 写成常量，
// Code/Call/Branch/Intent/Result/Progress/Loop/LLM 在函数里内联 map），
// 且已经开始漂移 —— LLM 那处的注释写着「与 CodeNodeProps 一致」，靠人肉维持。
// 新增一种类型（例如 array[integer]）只改本文件。
import i18n from '@/locales'

const ICONS = {
  string: 'S',
  number: 'N',
  boolean: 'B',
  object: 'O',
  // secret：全局变量专有的类型（值不回显），只出现在变量选择器里
  secret: 'K',
  'array[string]': 'S',
  'array[number]': 'N',
  'array[boolean]': 'B',
  'array[object]': 'O'
}

const CLASSES = {
  string: 'type-string',
  number: 'type-number',
  boolean: 'type-boolean',
  object: 'type-object',
  secret: 'type-secret',
  'array[string]': 'type-array-string',
  'array[number]': 'type-array-number',
  'array[boolean]': 'type-array-boolean',
  'array[object]': 'type-array-object'
}

export const getDataTypeIcon = (type) => ICONS[type] || 'O'

export const getDataTypeIconClass = (type) => CLASSES[type] || 'type-object'

// 类型的可读名（变量面板、变量下拉里展示）：文案在语言包里，这里只留 key 映射
const LABEL_KEYS = {
  string: 'string',
  number: 'number',
  boolean: 'boolean',
  object: 'object',
  secret: 'secret',
  'array[string]': 'arrayString',
  'array[number]': 'arrayNumber',
  'array[boolean]': 'arrayBoolean',
  'array[object]': 'arrayObject'
}

// 认不出的类型原样返回（与原先一致），别为了翻译把未知值吞掉
export const getDataTypeLabel = (type) => {
  const key = LABEL_KEYS[type]
  return key ? i18n.global.t(`flow.dataType.${key}`) : type
}
