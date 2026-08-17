import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],

  // base 必须是 '/admin/'。默认的 '/' 会让 index.html 去请求 /assets/index-xxx.js，
  // 而网关只在 /admin 下发静态文件（见 internal/admin/webui.go），那些请求会 404，
  // 页面白屏——且这个故障**只在 embed 后的二进制里出现**，npm run dev 一切正常。
  base: '/admin/',

  build: {
    // 产物直接落进 Go 包目录：`//go:embed` 只能读自己包目录下的文件，
    // 放在 web/dist 的话 internal/webui 根本 embed 不到。
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },

  server: {
    // 开发时前端跑在 5173，接口打到本机跑着的网关。会话是 cookie，
    // 同源代理（而不是 CORS）才能让 cookie 正常带上。
    proxy: {
      '/admin/api': {
        target: 'http://127.0.0.1:8317',
        changeOrigin: false,
      },
    },
  },
})
