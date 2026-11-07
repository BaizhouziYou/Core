import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: { cssMinify: false },
  server: { host: '127.0.0.1', proxy: { '/api': 'http://127.0.0.1:5001' } },
})
