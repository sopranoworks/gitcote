import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

const webCoreRoot = resolve(__dirname, '../../shoka/packages/web-core/src')
const nm = resolve(__dirname, 'node_modules')

export default defineConfig({
  base: '/',
  plugins: [react()],
  resolve: {
    alias: {
      '@shoka/web-core/tokens.css': resolve(webCoreRoot, 'styles/tokens.css'),
      '@shoka/web-core/pages/SettingsPage': resolve(webCoreRoot, 'pages/SettingsPage.tsx'),
      '@shoka/web-core': resolve(webCoreRoot, 'index.ts'),
      'react': resolve(nm, 'react'),
      'react-dom': resolve(nm, 'react-dom'),
      '@tanstack/react-router': resolve(nm, '@tanstack/react-router'),
      '@tanstack/react-query': resolve(nm, '@tanstack/react-query'),
    },
  },
  build: {
    outDir: '../cmd/gityard/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/ws/ui': { target: 'ws://localhost:8080', ws: true },
      '/auth': { target: 'http://localhost:8080' },
      '/git': { target: 'http://localhost:8080' },
    },
  },
})
