// 统一的 Markdown 渲染：对话正文里 agent 常回 md（标题 / 列表 / 表格 / 代码块），
// 直接当纯文本显示会满屏符号。这里集中做「渲染 + 消毒」，供 Chat 等组件复用。
//
// 两条安全底线：
//   1. markdown-it 关掉 html —— 不解析正文里的原生 HTML，源头就不产生危险节点；
//   2. 再过一遍 DOMPurify —— 双保险，防后续有人打开 html 或引入插件绕过第 1 条。
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'

const md = new MarkdownIt({
  html: false,     // 安全底线①：不允许正文里的原生 HTML
  linkify: true,   // 裸链接自动成 <a>
  breaks: true,    // 单个换行即换行：聊天场景符合直觉（否则要空一行才换行）
  typographer: false,
})

// 链接统一新窗口打开：对话窗口在应用内，点 <a> 原地跳走会把当前会话界面顶掉。
const defaultLinkOpen =
  md.renderer.rules.link_open ||
  ((tokens, idx, options, env, self) => self.renderToken(tokens, idx, options))
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  return defaultLinkOpen(tokens, idx, options, env, self)
}

// 渲染结果缓存：流式输出每来一个分片都会触发整条消息重渲染，而列表里其余块内容没变 ——
// 缓存把「每帧重算所有块」降为「只算正在增长的最后一块」。用插入序淘汰，避免长会话撑爆内存。
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
