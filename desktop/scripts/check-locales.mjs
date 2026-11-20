#!/usr/bin/env node
// Locale bundle self-check: file set, key set, placeholders, empty values.
//
// Why it hangs off the build: vue-i18n does not fail on a missing key, it silently falls back to fallbackLocale (en), which
// shows up as half an English screen or a raw key in the UI. Visual regression misses this, and it is always the same cause:
// one language was edited, the other forgotten.
//
// Criteria:
//   - zh-CN is the baseline (Chinese is the single source of truth); every other language is compared file by file, and a
//     third language is picked up automatically - this script does not need changing;
//   - the file sets must match: a missing file means that whole part of the bundle never loads, and a misspelled filename
//     does the same;
//   - placeholder sets must match: dropping {name} renders as "password must be at least   characters";
//   - an empty value is a problem too: an empty string and a missing key are both blank on screen.
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
  console.error(`[locales] baseline language ${BASELINE} not found (directory: ${LOCALES_DIR})`)
  process.exit(1)
}

const problems = []
const baselineFiles = filesOf(BASELINE)

// Empty values in the baseline are checked too: an empty string in the Chinese bundle is hidden by the same fallback.
for (const file of baselineFiles) {
  for (const [key, value] of load(BASELINE, file)) {
    if (!asText(value).trim()) problems.push(`${BASELINE}/${file} ${key} is empty`)
  }
}

for (const lang of languages) {
  if (lang === BASELINE) continue
  const files = filesOf(lang)

  for (const missing of baselineFiles.filter((f) => !files.includes(f))) {
    problems.push(`${lang} is missing the file ${missing}`)
  }
  for (const extra of files.filter((f) => !baselineFiles.includes(f))) {
    problems.push(`${lang} has an extra file ${extra} (it does not exist in the baseline, so it never loads)`)
  }

  for (const file of baselineFiles.filter((f) => files.includes(f))) {
    const base = load(BASELINE, file)
    const other = load(lang, file)

    for (const [key, value] of base) {
      if (!other.has(key)) {
        problems.push(`${lang}/${file} is missing the key ${key}`)
        continue
      }
      const want = placeholders(value)
      const got = placeholders(other.get(key))
      const lost = [...want].filter((p) => !got.has(p))
      const added = [...got].filter((p) => !want.has(p))
      if (lost.length) problems.push(`${lang}/${file} ${key} dropped the placeholder(s) {${lost.join('} {')}}`)
      if (added.length) problems.push(`${lang}/${file} ${key} has extra placeholder(s) {${added.join('} {')}}`)
    }

    for (const [key, value] of other) {
      if (!base.has(key)) problems.push(`${lang}/${file} has the extra key ${key}`)
      if (!asText(value).trim()) problems.push(`${lang}/${file} ${key} is empty`)
    }
  }
}

if (problems.length) {
  console.error(
    `[locales] ${problems.length} problem(s) (baseline ${BASELINE}, ${languages.length} language(s) x ${baselineFiles.length} file(s)):`
  )
  for (const problem of problems) console.error('  - ' + problem)
  process.exit(1)
}

if (languages.length === 1) {
  console.warn(`[locales] only ${BASELINE} is present; there is nothing to compare against`)
}
console.log(
  `[locales] ${languages.length} language(s) x ${baselineFiles.length} file(s): files, keys, placeholders and empty values match`
)
