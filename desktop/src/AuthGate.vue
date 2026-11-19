<template>
  <!-- 登录薄壳：租户登录（控制台，原样复用）与 平台登录（管理台）二选一。
       切换条通过插槽注入各自卡片的右上角（不悬在视口顶部）。 -->
  <div class="auth-gate">
    <Login v-if="tab === 'desk'" @login-success="onSuccess('desk')">
      <template #mode-switch>
        <div class="card-switch">
          <button :class="{ active: tab === 'desk' }" @click="tab = 'desk'">{{ $t('login.tenantTab') }}</button>
          <button :class="{ active: tab === 'manage' }" @click="tab = 'manage'">{{ $t('login.platformTab') }}</button>
          <!-- 语言：按钮写「当前」语言，点开再选目标（两种以上语言时二态按钮不成立） -->
          <el-dropdown trigger="click" @command="setLocale">
            <button class="loc" type="button" :title="$t('common.language')">{{ localeShort }}</button>
            <template #dropdown>
              <LocaleMenu :active="activeLocale" />
            </template>
          </el-dropdown>
        </div>
      </template>
    </Login>

    <PlatformLogin v-else @login-success="onSuccess('manage')">
      <template #mode-switch>
        <div class="card-switch">
          <button :class="{ active: tab === 'desk' }" @click="tab = 'desk'">{{ $t('login.tenantTab') }}</button>
          <button :class="{ active: tab === 'manage' }" @click="tab = 'manage'">{{ $t('login.platformTab') }}</button>
          <!-- 语言：按钮写「当前」语言，点开再选目标（两种以上语言时二态按钮不成立） -->
          <el-dropdown trigger="click" @command="setLocale">
            <button class="loc" type="button" :title="$t('common.language')">{{ localeShort }}</button>
            <template #dropdown>
              <LocaleMenu :active="activeLocale" />
            </template>
          </el-dropdown>
        </div>
      </template>
    </PlatformLogin>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import Login from './tenant/components/Login.vue'
import PlatformLogin from './platform/views/PlatformLogin.vue'
import LocaleMenu from './LocaleMenu.vue'
import { currentLocale, currentLocaleShort, setLocale } from './locales'

const props = defineProps({
  // 默认停在哪个 tab：跟随上次使用的模式（App.vue 传入），首次进来是租户登录
  initialTab: { type: String, default: 'desk' }
})

const emit = defineEmits(['login-success'])

const tab = ref(props.initialTab === 'manage' ? 'manage' : 'desk')

// 语言切换：按钮显示当前语言的短码，菜单里列出全部可选语言并标出当前项。
// 不走「点一下翻到另一种」—— 语言多于两种时不知道要翻到哪一个。
const localeShort = computed(() => currentLocaleShort())
const activeLocale = computed(() => currentLocale())

const onSuccess = (mode) => emit('login-success', mode)
</script>

<style scoped>
.auth-gate {
  min-height: 100vh;
  width: 100%;
}

/* 卡片右上角的登录方式切换 = 小药丸分段控件（segmented control）：
   灰底凹槽 + 选中项白底药丸 + 轻阴影，两段不显示分隔线。
   插槽内容编译在 AuthGate 的作用域里，所以 scoped 样式写在这里即可生效；
   定位参照是两个登录卡片自己的 position: relative（.login-box / .pl-box）。 */
.card-switch {
  position: absolute;
  top: 12px;
  right: 14px;
  z-index: 2;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  background: #f1f5f9;
  border-radius: 9px;
}

.card-switch button {
  border: none;
  background: none;
  min-width: 56px;
  padding: 3px 10px;
  border-radius: 7px;
  font-size: 12px;
  line-height: 18px;
  text-align: center;
  white-space: nowrap;
  color: #64748b;
  cursor: pointer;
  transition: all 0.18s;
  font-family: Segoe UI, Microsoft Yahei, sans-serif;
}

.card-switch button:hover {
  color: #2563eb;
}

/* 选中项：白色药丸（两个按钮等宽，加粗不会引起位移） */
.card-switch button.active {
  background: #fff;
  color: #2563eb;
  font-weight: 600;
  box-shadow: 0 1px 3px rgba(15, 23, 42, 0.1);
}
</style>
