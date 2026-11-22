// Help topic registry.
//
// Content lives as one Markdown file per topic per locale, next to this module:
//   src/help/zh-CN/<topic>.md      src/help/en/<topic>.md
//
// Why Markdown files instead of keys in the locale bundles: a topic runs 50-120 lines, needs
// sub-headings, tables and code blocks, and is reviewed like prose — as JSON strings it would be an
// escape-character swamp that nobody reads in a diff. The short, inline explanations stay in the
// bundles (`<ns>.*Tip` / `.et-help*`) where they belong.
//
// Why a registry instead of "whatever file happens to exist": the sidebar entry point and the
// per-screen "?" entry point must resolve the same topic to the same file, and the build-time check
// in scripts/check-locales.mjs needs a fixed list to compare the two locales against.
//
// Titles are not duplicated here: the first `# heading` of the file is the title, so renaming a topic
// means editing the file, not editing a second copy.

// Vite 4 spells the loader option `as: 'raw'` (Vite 5 renamed it to `query` / `import`).
const DOCS = import.meta.glob('./*/*.md', { as: 'raw', eager: true })

// Groups drive the "Help center" list only. `label` is a locale key, not copy.
//
// `side` decides which console lists the group: the tenant console and the platform console render the
// same registry, but neither user can act on the other side's screens, so a topic that talks about
// "Platform users" must not show up for a tenant. 'both' is for the two concept groups, which explain
// words that appear on both sides.
//
// Order matters: concepts first (they explain the words), then the current side's screens, then the
// pitfall list.
export const HELP_GROUPS = [
  {
    key: 'concept',
    label: 'help.groupConcept',
    side: 'both',
    topics: ['concept-plugin-gate', 'concept-version'],
  },
  {
    key: 'tenant',
    label: 'help.groupTenant',
    side: 'tenant',
    topics: [
      'page-chat',
      'page-agents',
      'page-skills',
      'page-tenant-tools',
      'page-flows',
      'page-running',
      'page-endpoints',
      'page-market',
      'page-settings',
    ],
  },
  {
    key: 'platform',
    label: 'help.groupPlatform',
    side: 'platform',
    topics: [
      'page-platform-tenants',
      'page-platform-users',
      'page-platform-plugins',
      'page-platform-system',
      'page-platform-assets',
      'page-platform-market',
      'page-my-tenants',
    ],
  },
  {
    key: 'pitfall',
    label: 'help.groupPitfall',
    side: 'both',
    topics: ['concept-pitfalls'],
  },
]

/** groupsFor returns the groups one console should list. `scope` is 'tenant' or 'platform'. */
export function groupsFor(scope) {
  return HELP_GROUPS.filter((g) => g.side === 'both' || g.side === scope)
}

/**
 * helpBody returns the raw Markdown for a topic, or '' when it does not exist.
 *
 * Falls back to English for the same reason vue-i18n does (`fallbackLocale: 'en'`): a missing
 * translation should show readable content, not an empty drawer. The build-time check reports the
 * missing file, so the fallback is a safety net, not an accepted state.
 */
export function helpBody(locale, topic) {
  if (!topic) return ''
  return DOCS[`./${locale}/${topic}.md`] ?? DOCS[`./en/${topic}.md`] ?? ''
}

// helpTitle reads the first level-1 heading. Kept deliberately dumb: no front-matter parser, no
// second metadata file — one source of truth per topic.
export function helpTitle(locale, topic) {
  const line = helpBody(locale, topic)
    .split(/\r?\n/)
    .find((l) => /^#\s+\S/.test(l))
  return line ? line.replace(/^#\s+/, '').trim() : topic
}
