// Shared Markdown renderer: the chat body renders agent replies (headings / lists / tables / code
// blocks), and the help drawer renders the topics under src/help. Keeping one renderer means one
// sanitizing policy instead of two that drift apart.
//
// Two safety rules, both mandatory:
//   1. markdown-it runs with html:false, so raw HTML in the source never becomes a node in the first place;
//   2. the output then goes through DOMPurify as a second line of defense, in case someone flips rule 1
//      or adds a plugin that bypasses it.
//
// This module lives at src/utils (not src/tenant/utils) because both subtrees use it and
// src/platform/** must not depend on src/tenant/** — see src/platform/README.md.
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'

const md = new MarkdownIt({
  html: false,     // safety rule 1: no raw HTML in the source
  linkify: true,   // bare URLs become <a>
  breaks: true,    // a single newline breaks the line: chat expects that
  typographer: false,
})

// Links open in a new window: the app is a window, and navigating away in place would replace the
// current view (a running conversation, an open help topic) with the target page.
const defaultLinkOpen =
  md.renderer.rules.link_open ||
  ((tokens, idx, options, env, self) => self.renderToken(tokens, idx, options))
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  return defaultLinkOpen(tokens, idx, options, env, self)
}

// Render cache: streaming replies re-render the whole message on every chunk while the other blocks
// are unchanged, so caching cuts "recompute every block per frame" down to "recompute the growing
// tail". Insertion-order eviction keeps long conversations from growing the map without bound.
const cache = new Map()
const CACHE_MAX = 200

export function renderMarkdown(source) {
  const text = source == null ? '' : String(source)
  if (!text) return ''
  const cached = cache.get(text)
  if (cached !== undefined) return cached

  const html = DOMPurify.sanitize(md.render(text), { ADD_ATTR: ['target'] })
  cache.set(text, html)
  if (cache.size > CACHE_MAX) cache.delete(cache.keys().next().value)
  return html
}
