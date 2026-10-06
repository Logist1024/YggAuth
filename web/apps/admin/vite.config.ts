import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物直接输出到 Go 侧的 web/dist/admin。
//
// 用相对路径而不是绝对路径:绝对路径在开发机与 CI 上不同,
// 产物的 sourcemap 会把构建者的目录结构写进去。
export default defineConfig({
  // 后台挂在 /admin 前缀下(见 internal/webserver/webserver.go)。
  //
  // 不设 base 时 Vite 默认用 "/",产物里的资源引用会是 /assets/...,
  // 而那个路径由挂在根上的账号站 SPA 接管 —— 它会把未知路径回退成
  // 一份 index.html。浏览器看到 text/html 配 nosniff 直接拒绝执行,
  // 后台首页就是一片白,且控制台只有一行 MIME 类型错误。
  base: '/admin/',
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@yggauth/shared': fileURLToPath(new URL('../../shared/src/index.ts', import.meta.url)),
    },
  },
  build: {
    outDir: fileURLToPath(new URL('../../../internal/webserver/dist/admin', import.meta.url)),
    emptyOutDir: true,
    // 不生成 sourcemap:构建产物会被打进二进制并分发,
    // 带上它等于把源码发出去。
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
    // 同账号站:依赖只随升级变,业务代码随每次改动变;分开才能让重复访问
    // 只重新下载业务 chunk,而不是整个 1.5MB 的 vendor。
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('node_modules/')) return 'vendor'
        },
      },
    },
  },
  server: {
    port: 5174,
    // 同 account 应用:后端在 3000(docs/08-deployment.md 第九节)。
    proxy: {
      '/api': 'http://127.0.0.1:3000',
      '/oauth': 'http://127.0.0.1:3000',
      '/mc': 'http://127.0.0.1:3000',
    },
  },
})
