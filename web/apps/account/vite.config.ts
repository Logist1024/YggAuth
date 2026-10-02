import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建产物直接输出到 Go 侧的 web/dist/account。
//
// 用相对路径而不是绝对路径:绝对路径在开发机与 CI 上不同,
// 产物的 sourcemap 会把构建者的目录结构写进去。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@yggauth/shared': fileURLToPath(new URL('../../shared/src/index.ts', import.meta.url)),
    },
  },
  build: {
    outDir: fileURLToPath(new URL('../../../internal/webserver/dist/account', import.meta.url)),
    emptyOutDir: true,
    // 不生成 sourcemap:构建产物会被打进二进制并分发,
    // 带上它等于把源码发出去。
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
})
