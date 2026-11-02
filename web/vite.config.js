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
    // Only files the tests actually load are measured: the console's screens are
    // .vue templates that these node tests never mount, and counting them as 0%
    // would hide a real regression in the modules that do carry logic.
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json-summary'],
      reportsDirectory: 'coverage',
      // The logic layer is what these node tests can actually reach: screens are
      // .vue templates that would sit at 0% and drown the number that matters.
      include: ['src/lib/**/*.js'],
    },
  },
})
