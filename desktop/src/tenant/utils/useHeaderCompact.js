import { ref, watch, onBeforeUnmount } from 'vue'

/**
 * 观察元素宽度，输出头部密度档位，用于「分层降级」：
 *
 *   wide → 全部信息（名称 + 描述 + 长文案 + 全部按钮）
 *   md   → 次要信息收窄（如描述收起、搜索框变窄）
 *   sm   → 继续降级（如平铺按钮组收进下拉 / 「⋯」）
 *   xs   → 最窄档（如搜索框收成图标）——仅在显式传入 thresholds.xs 时出现
 *
 * 三个全屏编辑页（智能体 / 技能 / 工作流）头部结构一致，共用这一份逻辑。
 * 阈值按各页按钮数量微调。
 *
 * @param {import('vue').Ref<HTMLElement|null>} elRef 目标元素（通常是 .full-screen-header）
 * @param {{ md?: number, sm?: number, xs?: number }} [thresholds] 档位阈值（px）
 * @returns {import('vue').Ref<'wide'|'md'|'sm'|'xs'>}
 */
export function useHeaderCompact(elRef, thresholds = {}) {
  const md = thresholds.md ?? 1000
  const sm = thresholds.sm ?? 820
  const xs = thresholds.xs            // 可选：不传则维持 wide / md / sm 三档
  const size = ref('wide')
  let observer = null

  const pick = (w) => {
    if (w <= sm) {
      if (xs != null && w <= xs) return 'xs'
      return 'sm'
    }
    return w <= md ? 'md' : 'wide'
  }

  const observe = (el) => {
    if (observer) observer.disconnect()
    observer = null
    if (!el) return
    // 首帧先按当前宽度定档，避免闪一下
    size.value = pick(el.getBoundingClientRect().width)
    if (typeof ResizeObserver === 'undefined') return
    observer = new ResizeObserver((entries) => {
      const w = entries[0] && entries[0].contentRect && entries[0].contentRect.width
      if (typeof w === 'number' && w > 0) size.value = pick(w)
    })
    observer.observe(el)
  }

  watch(elRef, observe, { immediate: true, flush: 'post' })
  onBeforeUnmount(() => {
    if (observer) observer.disconnect()
  })

  return size
}

export default useHeaderCompact
