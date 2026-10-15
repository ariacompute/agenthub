import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// VITE_PROXY_TARGET lets the compose dev server reach the service by its
// compose name; local `bun run dev` keeps the default.
const target = process.env.VITE_PROXY_TARGET || 'http://localhost:3500'

export default defineConfig({
  plugins: [vue()],
  server: {
    host: '0.0.0.0',
    port: 5173,
    allowedHosts: true,
    proxy: {
      '/api': target,
      '/a2a': target,
      '/healthz': target,
    },
  },
  build: {
    outDir: '../backend/static',
    emptyOutDir: true,
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.spec.js'],
  },
})
