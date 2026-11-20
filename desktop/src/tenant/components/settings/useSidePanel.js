import { ref, computed, watch, nextTick, onUnmounted, provide, inject } from 'vue'

const SIDE_PANEL_KEY = Symbol('side-panel-context')

/**
 * 右侧侧边面板的公共行为：定位、拖宽、互斥、点外部关闭。
 *
 * 收拢这一份是因为设置页里 7 个表单面板原来各抄一遍：
 * 布局基准、GAP、拖拽区间、外部点击判定只要有一处走样，面板就会出现
 * 「位置只对某一个面板」「点外面关不掉」「拖宽后跑到窗口外面」这类只在某页复现的问题。
 *
 * 用 provide 向下传递：拆分后的子组件（如 BindingsTab）自己的面板要靠同一套坐标，
 * 否则父子两个面板的 top/高度会对不齐。
 *
 * @param {object} opts
 * @param {import('vue').Ref<HTMLElement|null>} opts.anchorRef 定位基准元素（面板顶部与它对齐）
 * @param {() => void} [opts.onCloseAll] 关闭全部时的额外复位（如左侧候选列表）
 * @param {number} [opts.gap] 与窗口边缘的留白
 * @param {number} [opts.minWidth]
 * @param {number} [opts.maxWidth]
 */
export function provideSidePanel({
  anchorRef,
  onCloseAll,
  gap = 12,
  minWidth = 320,
  maxWidth = 900
}) {
  // null = 用各面板自己的默认宽度；拖拽后才有具体值
  const width = ref(null)
  const baseRect = ref({ top: 0, right: 0, height: 600 })

  // 面板可见性开关由使用方注册进来（父组件的常驻面板、子组件的按需面板都在这里汇合）。
  // version 是给 computed 用的依赖：新注册一个 ref 时需要让它重新求值。
  const visibleRefs = []
  const version = ref(0)
  const anyVisible = computed(() => {
    version.value
    return visibleRefs.some((r) => r.value)
  })

  const register = (r) => {
    if (!visibleRefs.includes(r)) {
      visibleRefs.push(r)
      version.value++
    }
    return r
  }

  const updateBase = () => {
    const el = anchorRef.value
    if (!el) return
    const r = el.getBoundingClientRect()
    // 顶部对齐内容区，底部与右侧各留一个 GAP：
    // - 高度取「视口可用高度 - GAP」，内容区本身可能被内容撑高或撑不满，不能直接用它的高度；
    // - right 按窗口算，内容区宽度/布局变化时面板位置不会跑偏；
    // - top clamp 到 GAP，页面滚动时面板不跟着跑出视口。
    const top = Math.max(gap, r.top)
    const next = {
      top,
      right: window.innerWidth - gap,
      height: Math.max(120, window.innerHeight - top - gap)
    }
    const cur = baseRect.value
    // 位置没变就不写回，避免面板内滚动等高频事件触发无谓重渲染
    if (cur.top === next.top && cur.right === next.right && cur.height === next.height) return
    baseRect.value = next
  }

  // 按「默认宽度 / 拖拽后宽度」算内联样式（贴右边缘）
  const styleFor = (defWidth) => {
    const w = width.value ?? defWidth
    return {
      top: baseRect.value.top + 'px',
      left: Math.max(0, baseRect.value.right - w) + 'px',
      width: w + 'px',
      height: baseRect.value.height + 'px'
    }
  }

  // 左侧拖拽条改宽：向左拖变宽，向右拖变窄
  const startResize = (e) => {
    // 事件是子组件 emit 上来的，异步后 currentTarget 会失效，这里取一次并留 target 兜底
    const handle = e.currentTarget || e.target
    const panelEl = handle && handle.parentElement
    const startWidth = panelEl ? panelEl.getBoundingClientRect().width : 560
    const startX = e.clientX
    width.value = Math.round(startWidth)
    const onMove = (ev) => {
      width.value = Math.max(minWidth, Math.min(startWidth + (startX - ev.clientX), maxWidth))
    }
    const onUp = () => {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }

  const closeAll = () => {
    for (const r of visibleRefs) r.value = false
    if (onCloseAll) onCloseAll()
  }

  // 点面板外部关闭；放行 Element Plus 浮层（下拉、提示、确认框）
  const onOutsideClick = (e) => {
    const t = e.target
    if (!t || !t.closest) return
    if (t.closest('.el-select-dropdown') || t.closest('.el-popper') ||
        t.closest('.el-message-box') || t.closest('.el-overlay')) return
    if (t.closest('.settings-panel')) return
    closeAll()
  }

  watch(anyVisible, (visible) => {
    if (visible) {
      width.value = null // 每次打开都用该面板的默认宽度
      nextTick(() => {
        if (!anyVisible.value) return
        updateBase()
        document.addEventListener('mousedown', onOutsideClick)
      })
      window.addEventListener('resize', updateBase)
      // 捕获容器滚动：面板 fixed 定位需要跟随内容区位置变化
      window.addEventListener('scroll', updateBase, true)
    } else {
      document.removeEventListener('mousedown', onOutsideClick)
      window.removeEventListener('resize', updateBase)
      window.removeEventListener('scroll', updateBase, true)
    }
  })

  onUnmounted(() => {
    document.removeEventListener('mousedown', onOutsideClick)
    window.removeEventListener('resize', updateBase)
    window.removeEventListener('scroll', updateBase, true)
  })

  const ctx = { width, baseRect, anyVisible, updateBase, styleFor, startResize, closeAll, register }
  provide(SIDE_PANEL_KEY, ctx)
  return ctx
}

/** 子组件取同一套侧边面板坐标（父组件用 provideSidePanel 提供） */
export function useSidePanelContext() {
  const ctx = inject(SIDE_PANEL_KEY, null)
  if (!ctx) throw new Error('useSidePanelContext must be used in a subtree of provideSidePanel')
  return ctx
}
