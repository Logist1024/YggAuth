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
    // 第三方依赖与业务代码分开放:依赖只在升级时才变,业务代码几乎每次改文案
    // 都会变。混在同一个 1.5MB 的 index-*.js 里,等于每改一个字都要让用户重新
    // 下载整个 antd;拆出去之后,重复访问只需重新拿业务 chunk。
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('node_modules/')) return 'vendor'
        },
      },
    },
  },
  server: {
    port: 5173,
    // 后端跑在 3000(见 docs/08-deployment.md 第九节:Vite dev server
    // 代理 /api /oauth /mc 到后端 3000)。写成 8080 会让开发模式下
    // 所有接口 502,而浏览器只看到「后端挂了」。
    proxy: {
      '/api': 'http://127.0.0.1:3000',
      '/oauth': 'http://127.0.0.1:3000',
      '/mc': 'http://127.0.0.1:3000',
    },
  },
})
