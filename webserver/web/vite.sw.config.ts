import { defineConfig } from 'vite'

// The service worker (src/sw.ts) is built on its own as one classic script at
// the site root: a worker's scope is its own path, and not every browser can
// run a module service worker that imports shared chunks.
export default defineConfig({
  build: {
    outDir: '../server/dist',
    emptyOutDir: false,
    copyPublicDir: false,
    lib: {
      entry: 'src/sw.ts',
      formats: ['iife'],
      name: 'imagethatSW',
      fileName: () => 'sw.js',
    },
  },
})
