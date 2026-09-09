import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          ui: ['antd', '@ant-design/icons'],
        },
      },
    },
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.API_TARGET || 'http://127.0.0.1:8081',
        ws: true,
        configure(proxy) {
          proxy.on('proxyReq', (request, incoming) => {
            request.setHeader('X-Campus-Client-IP', incoming.socket.remoteAddress || 'unknown')
          })
        },
      },
      '/media': { target: process.env.API_TARGET || 'http://127.0.0.1:8081' },
    },
  },
})
