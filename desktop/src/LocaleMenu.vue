<template>
  <!-- 语言下拉菜单：三处外壳（登录页 / 控制台 / 管理台）共用一份，
       加语言时只改 locales/index.js 的 LOCALES，这里自动多一项。
       菜单挂在各外壳自己的 <el-dropdown> 的 #dropdown 插槽里，
       选中项由外层 el-dropdown 的 @command 收走，本组件不直接调 setLocale。 -->
  <el-dropdown-menu>
    <el-dropdown-item v-for="l in LOCALES" :key="l.value" :command="l.value">
      <span class="locale-label">{{ l.label }}</span>
      <el-icon v-if="l.value === active" class="locale-check"><Check /></el-icon>
    </el-dropdown-item>
  </el-dropdown-menu>
</template>

<script setup>
import { Check } from '@element-plus/icons-vue'
import { LOCALES } from './locales'

defineProps({
  // 当前生效的语言值：只用来打勾，不参与切换
  active: { type: String, default: '' }
})
</script>

<style scoped>
/* 菜单被 teleport 到 body，但 scoped 的 data-v 属性在元素上，样式照样生效 */
.locale-label {
  flex: 1;
}

.locale-check {
  margin-left: 10px;
  color: #1890ff;
}
</style>
