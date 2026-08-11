import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    // go:embed cannot reach outside its own package directory, so the bundle
    // is written straight into the Go package that embeds it.
    outDir: '../server/dist',
    emptyOutDir: false,
  },
  server: {
    // `npm run dev` proxies the API to a locally running imagethat-web.
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8347',
        changeOrigin: false,
      },
    },
  },
})
