import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import path from 'path'

export default defineConfig({
  plugins: [
    vue()
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src')
    }
  },
  build: {
    // 生产不产出 .map：dist 是整包发出去的（见 .gitignore 的 dist/ 与 desktop/dist.zip），
    // 带上 sourcemap 等于把源码、注释、接口路径一起交给用户。调试本地用 dev server 就够。
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          vendor: ['vue', 'element-plus']
        },
        chunkFileNames: 'assets/[name]-[hash].js',
        entryFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash].[ext]'
      }
    }
  },
  server: {
    port: 5173,
    host: '0.0.0.0',
    open: true,
    proxy: {
      '/nexus': {
        target: 'http://localhost:27006',
        changeOrigin: true,
        ws: true,
        rewrite: (path) => path.replace(/^\/nexus/, '')
      }
    }
  }
})

