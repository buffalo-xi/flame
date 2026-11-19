#!/usr/bin/env node
// 语言包自检：文件集合 / 键集 / 占位符 / 空值。
//
// 为什么挂在 build 前：vue-i18n 缺键不会报错，只会静默回退到 fallbackLocale（en），
// 界面上表现为「英文界面里冒出半句英文」或者干脆显示原始 key。人眼回归是漏的，
// 而这类问题每次都是同一个原因：改了一种语言，忘了另一种。
//
// 口径：
//   - 以 zh-CN 为基准（中文是唯一真源），其余语言逐文件对比；加第三门语言时自动纳入，
//     不用改这个脚本；
//   - 文件集合必须一致：少一个文件 = 那部分整包不生效，拼错文件名也一样；
//   - 占位符集合必须一致：译丢 {name} 会渲染成「密码至少  位」；
//   - 空值也算问题：空串和缺键在界面上一样是空白。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const LOCALES_DIR = join(HERE, '..', 'src', 'locales')
const BASELINE = 'zh-CN'
const PLACEHOLDER = /\{([A-Za-z0-9_]+)\}/g

function flatten(value, prefix = '', out = new Map()) {
  for (const [k, v] of Object.entries(value)) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) flatten(v, key, out)
    else out.set(key, v)
  }
  return out
}

function asText(value) {
  return Array.isArray(value) ? value.join('|') : String(value ?? '')
}

function placeholders(value) {
  return new Set([...asText(value).matchAll(PLACEHOLDER)].map((m) => m[1]))
}

function filesOf(lang) {
  return readdirSync(join(LOCALES_DIR, lang))
    .filter((f) => f.endsWith('.json'))
    .sort()
}

function load(lang, file) {
  return flatten(JSON.parse(readFileSync(join(LOCALES_DIR, lang, file), 'utf8')))
}

const languages = readdirSync(LOCALES_DIR)
  .filter((name) => statSync(join(LOCALES_DIR, name)).isDirectory())
  .sort()

if (!languages.includes(BASELINE)) {
  console.error(`[locales] 找不到基准语言 ${BASELINE}（目录：${LOCALES_DIR}）`)
  process.exit(1)
}

const problems = []
const baselineFiles = filesOf(BASELINE)

// 基准语言自己的空值也要查：中文包里的空串同样会被回退掩盖
for (const file of baselineFiles) {
  for (const [key, value] of load(BASELINE, file)) {
    if (!asText(value).trim()) problems.push(`${BASELINE}/${file} ${key} 是空值`)
  }
}

for (const lang of languages) {
  if (lang === BASELINE) continue
  const files = filesOf(lang)

  for (const missing of baselineFiles.filter((f) => !files.includes(f))) {
    problems.push(`${lang} 缺文件 ${missing}`)
  }
  for (const extra of files.filter((f) => !baselineFiles.includes(f))) {
    problems.push(`${lang} 多出文件 ${extra}（基准语言里没有，永远不会被加载）`)
  }

  for (const file of baselineFiles.filter((f) => files.includes(f))) {
    const base = load(BASELINE, file)
    const other = load(lang, file)

    for (const [key, value] of base) {
      if (!other.has(key)) {
        problems.push(`${lang}/${file} 缺键 ${key}`)
        continue
      }
      const want = placeholders(value)
      const got = placeholders(other.get(key))
      const lost = [...want].filter((p) => !got.has(p))
      const added = [...got].filter((p) => !want.has(p))
      if (lost.length) problems.push(`${lang}/${file} ${key} 丢了占位符 {${lost.join('} {')}}`)
      if (added.length) problems.push(`${lang}/${file} ${key} 多出占位符 {${added.join('} {')}}`)
    }

    for (const [key, value] of other) {
      if (!base.has(key)) problems.push(`${lang}/${file} 多出键 ${key}`)
      if (!asText(value).trim()) problems.push(`${lang}/${file} ${key} 是空值`)
    }
  }
}

if (problems.length) {
  console.error(
    `[locales] ${problems.length} 个问题（基准 ${BASELINE}，${languages.length} 种语言 × ${baselineFiles.length} 个文件）：`
  )
  for (const problem of problems) console.error('  - ' + problem)
  process.exit(1)
}

if (languages.length === 1) {
  console.warn(`[locales] 只有 ${BASELINE}，没有别的语言可供对比`)
}
console.log(`[locales] ${languages.length} 种语言 × ${baselineFiles.length} 个文件：文件、键、占位符、空值一致`)
