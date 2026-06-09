import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // @appx-org/agent-client is linked from a sibling repo (file: dependency)
  // and ships TypeScript source. Following the symlink can otherwise pull a
  // second copy of React from the package's own node_modules, which breaks
  // hooks ("Invalid hook call"). Dedupe forces a single React instance.
  resolve: {
    dedupe: ['react', 'react-dom'],
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
})
