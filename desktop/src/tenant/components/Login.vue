<template>
  <div class="login-container">
    <div class="login-box">
      <!-- 登录方式切换（租户登录 / 平台登录）：由外壳 AuthGate 通过插槽注入卡片右上角。
           不传插槽时什么都不显示，本组件保持被动 -->
      <slot name="mode-switch"></slot>

      <h2 class="login-title">Flame Desk</h2>
      
      <el-form :model="form" label-width="0" label-position="top">
               
        <div class="input-item">
          <el-input 
            v-model="form.username" 
            placeholder="账号"
            clearable
          />
        </div>
        
        <div class="input-item">
          <el-input 
            v-model="form.password" 
            type="password" 
            placeholder="密码"
            show-password
            @keyup.enter="handleLogin"
          />
        </div>
       
         <div class="input-item">
          <el-input 
            v-model="form.schema" 
            placeholder="租户"
            clearable
          />
        </div>

        <div class="input-item checkbox-item">
          <el-checkbox v-model="form.rememberMe">记住我</el-checkbox>
        </div>
        
        <div class="input-item">
          <el-button 
            type="primary" 
            @click="handleLogin" 
            class="login-btn"
            :loading="loading"
          >
            登录系统
          </el-button>
        </div>
      </el-form>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted, onUnmounted } from 'vue'
import apiConfig from '../config/api'
import { saveAuth } from '../utils/auth'
import { ElMessage } from 'element-plus'

const emit = defineEmits(['login-success'])

const loading = ref(false)

const form = reactive({
  schema: '',
  username: '',
  password: '',
  rememberMe: false
})

// 背景图片已移除
const handleLogin = async () => {
  // 注意：不要把 password / token 写进 console —— 浏览器日志、截图、前端错误上报都会泄露
  if (form.schema && form.username) {
    loading.value = true
    
    try {
      const loginData = {
        username: form.username,
        password: form.password || '',
        schema: form.schema
      }
      
      const response = await fetch(apiConfig.login, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify(loginData)
      })
      
      if (!response.ok) {
        throw new Error(`登录失败: ${response.status}`)
      }
      
      const result = await response.json()
      
      // 根据code判断登录状态
      if (result.code === 200) {
        // 登录成功，保存信息
        if (result.data && result.data.token && result.data.user) {
          saveAuth(result.data.token, result.data.user)
        }
        
        if (form.rememberMe) {
          localStorage.setItem('app_flame_auth_remembered_schema', form.schema)
          localStorage.setItem('app_flame_auth_remembered_username', form.username)
          localStorage.setItem('app_flame_auth_remember_me', 'true')
        } else {
          localStorage.removeItem('app_flame_auth_remembered_schema')
          localStorage.removeItem('app_flame_auth_remembered_username')
          localStorage.removeItem('app_flame_auth_remember_me')
        }
        
        console.log('登录成功')
        loading.value = false
        emit('login-success')
      } else {
        // 登录失败，显示错误信息
        const errorMessage = result.msg || '登录失败，请重试'
        console.error('登录失败:', errorMessage)
        ElMessage({
          message: errorMessage,
          type: 'error',
          duration: 2000
        })
        loading.value = false
      }
    } catch (err) {
      console.error('登录失败:', err)
      const errorMessage = `登录失败: ${err.message}`
      ElMessage({
        message: errorMessage,
        type: 'error',
        duration: 2000
      })
      loading.value = false
    }
  } else {
    const errorMessage = '租户、用户名和密码不能为空'
    console.error('登录失败:', errorMessage)
    ElMessage({
      message: errorMessage,
      type: 'error',
      duration: 2000
    })
  }
}

onMounted(() => {
  const savedSchema = localStorage.getItem('app_flame_auth_remembered_schema')
  const savedUsername = localStorage.getItem('app_flame_auth_remembered_username')
  const savedRememberMe = localStorage.getItem('app_flame_auth_remember_me')
  
  if (savedRememberMe === 'true') {
    if (savedSchema) {
      form.schema = savedSchema
    }
    if (savedUsername) {
      form.username = savedUsername
    }
    form.rememberMe = true
  }
})

onUnmounted(() => {
  // 背景图片相关的定时器已移除
})
</script>

<style scoped>
* { margin: 0; padding: 0; box-sizing: border-box; font-family: Segoe UI, Microsoft Yahei, sans-serif; }

/* 科技网格背景（不动） */
.login-container {
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
  margin: 0;
  position: relative;
}

/* 登录窗口：完全静止，不悬浮、不动画 */
.login-box {
  width: 400px;
  padding: 50px 45px;
  background: rgba(255, 255, 255, 0.85);
  border-radius: 20px;
  box-shadow: 0 8px 30px rgba(37, 99, 235, 0.1);
  border: 1px solid rgba(37, 99, 235, 0.15);
  backdrop-filter: blur(6px);
  position: relative;
}

/* 标题样式 */
.login-title {
  text-align: center;
  font-size: 26px;
  color: #1e293b;
  /* 上边距留给卡片右上角的登录方式切换条（.card-switch，由 AuthGate 注入），
     否则两者只差约 10px 会显得挤 */
  margin-top: 16px;
  margin-bottom: 40px;
  position: relative;
  letter-spacing: 1px;
}

.login-title::before,
.login-title::after {
  content: '';
  width: 30px;
  height: 1px;
  background: #2563eb;
  position: absolute;
  top: 50%;
}

.login-title::before { left: 20px; }
.login-title::after { right: 20px; }

/* 输入框 */
.input-item {
  margin-bottom: 24px;
}

.checkbox-item {
  margin-bottom: 16px;
}

:deep(.el-input__wrapper) {
  width: 100%;
  padding: 16px 20px;
  background: #f9fafb;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  font-size: 15px;
  color: #334155;
  outline: none;
  transition: 0.3s;
  box-shadow: none;
}

:deep(.el-input__wrapper:hover) {
  border-color: #2563eb;
  background: #fff;
  box-shadow: 0 0 0 4px rgba(37, 99, 235, 0.1);
}

:deep(.el-input__wrapper.is-focus) {
  border-color: #2563eb;
  background: #fff;
  box-shadow: 0 0 0 4px rgba(37, 99, 235, 0.1);
}

:deep(.el-input__inner) {
  font-size: 15px;
  color: #334155;
}

/* 复选框 */
:deep(.el-checkbox__label) {
  font-size: 14px;
  color: #64748b;
}

:deep(.el-checkbox__input.is-checked .el-checkbox__inner) {
  background-color: #2563eb;
  border-color: #2563eb;
}

:deep(.el-checkbox__input.is-checked + .el-checkbox__label) {
  color: #2563eb;
}

/* 按钮：无缩放、无移动 */
.login-btn {
  width: 100%;
  padding: 16px;
  border: none;
  border-radius: 12px;
  background: linear-gradient(90deg, #2563eb, #3b82f6);
  color: #fff;
  font-size: 16px;
  font-weight: 500;
  cursor: pointer;
}

:deep(.el-button--primary) {
  background: linear-gradient(90deg, #2563eb, #3b82f6);
  border: none;
  border-radius: 12px;
  font-size: 16px;
  font-weight: 500;
  padding: 16px;
}

:deep(.el-button--primary:hover) {
  background: linear-gradient(90deg, #1d4ed8, #2563eb);
}

:deep(.el-button--primary:active) {
  background: linear-gradient(90deg, #1e40af, #1d4ed8);
}

:deep(.el-button--primary.is-loading) {
  background: linear-gradient(90deg, #2563eb, #3b82f6);
}
</style>

