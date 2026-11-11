import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// The browser only ever talks to irisd through this origin. In dev and in
// preview, Vite proxies /api to the real daemon; the API key stays server-side.
const apiTarget = process.env.IRIS_API_TARGET ?? 'http://127.0.0.1:3300'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: {
    port: 5175,
    strictPort: true,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: false },
    },
  },
  preview: {
    port: 4175,
    strictPort: true,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: false },
    },
  },
})
