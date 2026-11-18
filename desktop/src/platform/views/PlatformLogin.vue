<template>
  <!-- 平台登录：账号 + 密码，**没有租户字段**（身份由平台用户表决定，登录后按 role 分流） -->
  <div class="pl-container">
    <div class="pl-box">
      <!-- 登录方式切换：由外壳 AuthGate 通过插槽注入卡片右上角 -->
      <slot name="mode-switch"></slot>

      <h2 class="pl-title">{{ $t('login.platformTitle') }}</h2>

      <el-form :model="form" label-width="0" label-position="top">
        <div class="input-item">
          <el-input v-model="form.name" :placeholder="$t('login.username')" clearable @keyup.enter="handleLogin" />
        </div>
        <div class="input-item">
          <el-input v-model="form.password" type="password" :placeholder="$t('login.password')" show-password @keyup.enter="handleLogin" />
        </div>
        <div class="input-item checkbox-item">
          <el-checkbox v-model="form.rememberMe">{{ $t('login.remember') }}</el-checkbox>
          <span class="pl-inline-hint">{{ $t('login.rememberHint') }}</span>
        </div>
        <div class="input-item">
          <el-button type="primary" class="login-btn" :loading="loading" @click="handleLogin">
            {{ $t('login.platformSubmit') }}
          </el-button>
        </div>
      </el-form>

      <div class="pl-hint">
        {{ $t('login.platformHint') }}
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { saveAuth } from '../auth'

const emit = defineEmits(['login-success'])

// 「记住我」＝记住账号名（**绝不记密码**），下次打开自动填上。
// 键名与控制台的 app_flame_auth_remembered_* 分开，两边互不影响。
const REMEMBER_NAME_KEY = 'app_flame_mgmt_remembered_username'
const REMEMBER_FLAG_KEY = 'app_flame_mgmt_remember_me'

const loading = ref(false)
const form = reactive({ name: '', password: '', rememberMe: false })

const handleLogin = async () => {
  if (!form.name || !form.password) {
    ElMessage({ message: '账号和密码不能为空', type: 'error', duration: 2000 })
    return
  }
  loading.value = true
  try {
    const data = await api.login(form.name.trim(), form.password)
    if (!data?.token || !data?.user) {
      throw new Error('登录响应缺少 token 或用户信息')
    }
    saveAuth(data.token, data.user)

    // 记住账号名：勾选则写入，取消勾选则清掉历史值（与"记住我"的字面语义一致）
    try {
      if (form.rememberMe) {
        localStorage.setItem(REMEMBER_NAME_KEY, form.name.trim())
        localStorage.setItem(REMEMBER_FLAG_KEY, 'true')
      } else {
        localStorage.removeItem(REMEMBER_NAME_KEY)
        localStorage.removeItem(REMEMBER_FLAG_KEY)
      }
    } catch {
      /* 隐私模式等场景下忽略 */
    }

    emit('login-success')
  } catch (err) {
    // 后端对"账号不存在 / 密码错误"用同一句文案，避免账号枚举；这里原样透出
    ElMessage({ message: err.message || '登录失败，请重试', type: 'error', duration: 2000 })
  } finally {
    loading.value = false
  }
}

// 回填上次记住的账号名（只有勾选过"记住我"才会存在）
onMounted(() => {
  try {
    if (localStorage.getItem(REMEMBER_FLAG_KEY) === 'true') {
      form.name = localStorage.getItem(REMEMBER_NAME_KEY) || ''
      form.rememberMe = true
    }
  } catch {
    /* 忽略 */
  }
})
</script>

<style scoped>
* { margin: 0; padding: 0; box-sizing: border-box; font-family: Segoe UI, Microsoft Yahei, sans-serif; }

/* 与租户登录页同一套观感（科技网格 + 静止卡片），切换 tab 时视觉不跳 */
.pl-container {
  height: 100vh;
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f6f8fc;
  background-image:
    linear-gradient(rgba(37, 99, 235, 0.05) 1px, transparent 1px),
    linear-gradient(90deg, rgba(37, 99, 235, 0.05) 1px, transparent 1px);
  background-size: 40px 40px;
  background-repeat: repeat;
  overflow: hidden;
  padding: 0 30px;
}

.pl-box {
  width: 400px;
  padding: 50px 45px;
  background: rgba(255, 255, 255, 0.85);
  border-radius: 20px;
  box-shadow: 0 8px 30px rgba(37, 99, 235, 0.1);
  border: 1px solid rgba(37, 99, 235, 0.15);
  backdrop-filter: blur(6px);
  position: relative; /* 卡片右上角切换条的定位参照（与控制台登录卡片一致） */
}

.pl-title {
  text-align: center;
  font-size: 26px;
  color: #1e293b;
  /* 与控制台登录卡片一致：给右上角的切换条（.card-switch）留出间距 */
  margin-top: 16px;
  margin-bottom: 40px;
  letter-spacing: 1px;
}

.input-item {
  margin-bottom: 24px;
}

/* 与控制台登录页一致：勾选项与下方按钮的间距略小 */
.checkbox-item {
  margin-bottom: 16px;
}

/* 「记住我」旁边的一句：它在勾选时就该回答「会不会记住密码」，别让人猜 */
.pl-inline-hint {
  margin-left: 8px;
  font-size: 12px;
  color: #94a3b8;
  vertical-align: middle;
}

:deep(.el-input__wrapper) {
  width: 100%;
  padding: 16px 20px;
  background: #f9fafb;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  font-size: 15px;
  color: #334155;
  transition: 0.3s;
  box-shadow: none;
}

:deep(.el-input__wrapper:hover),
:deep(.el-input__wrapper.is-focus) {
  border-color: #2563eb;
  background: #fff;
  box-shadow: 0 0 0 3px rgba(37, 99, 235, 0.08);
}

.login-btn {
  width: 100%;
  padding: 18px 0;
  border-radius: 12px;
  font-size: 16px;
  font-weight: 500;
  letter-spacing: 2px;
  background: #2563eb;
  border-color: #2563eb;
}

.pl-hint {
  margin-top: 6px;
  text-align: center;
  font-size: 12px;
  color: #94a3b8;
  line-height: 1.6;
}
</style>
