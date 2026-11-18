<template>
  <!-- 管理员页容器：只按顶栏选中的 tab 渲染对应视图，并把 count / session-expired 上抛。
       功能切换本身已在顶栏（PlatformApp 的 nav-menu），这里不再有自己的 tab。 -->
  <div class="ac">
    <AdminTenantsView
      v-if="tab === 'tenants'"
      @count="emit('count', 'tenants', $event)"
      @session-expired="emit('session-expired')"
    />
    <!-- 本地市场（原「资产审核」）：徽标给「待审数」（资产列表里最该被处理的那些），不是该页总条数 -->
    <AdminAssetsView
      v-else-if="tab === 'assets'"
      @count="emit('count', 'assets', $event)"
      @session-expired="emit('session-expired')"
    />
    <AdminMarketView
      v-else-if="tab === 'market'"
      @count="emit('count', 'market', $event)"
      @session-expired="emit('session-expired')"
    />
    <AdminPluginsView v-else-if="tab === 'plugins'" />
    <AdminUsersView
      v-else-if="tab === 'users'"
      @count="emit('count', 'users', $event)"
      @session-expired="emit('session-expired')"
    />
    <AdminSystemView v-else-if="tab === 'system'" />
  </div>
</template>

<script setup>
import AdminTenantsView from './AdminTenantsView.vue'
import AdminAssetsView from './AdminAssetsView.vue'
import AdminMarketView from './AdminMarketView.vue'
import AdminPluginsView from './AdminPluginsView.vue'
import AdminUsersView from './AdminUsersView.vue'
import AdminSystemView from './AdminSystemView.vue'

defineProps({
  // 由 PlatformApp 的顶栏导航驱动：'tenants' | 'assets' | 'plugins' | 'users' | 'system'
  tab: { type: String, default: 'tenants' }
})

const emit = defineEmits(['count', 'session-expired'])
</script>

<style scoped>
.ac {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
</style>
