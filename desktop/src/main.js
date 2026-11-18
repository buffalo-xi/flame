import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
// 副作用：统一提示时长（成功 1s / 错误与警告 2s）。
// 放在最前面，保证任何模块的顶层代码调用提示时也已经是包装后的方法。
import './utils/message.js'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'
import App from './App.vue'
import BreakpointMenuItem from './tenant/components/flows/flow/BreakpointMenuItem.vue'

const app = createApp(App)

// 全局注册Element Plus图标
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

// 全局注册断点菜单项（节点下拉菜单复用）
app.component('BreakpointMenuItem', BreakpointMenuItem)

app.use(ElementPlus)
app.mount('#app')
