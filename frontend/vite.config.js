import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue(), tailwindcss()],

  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
      '@components': resolve(__dirname, 'src/components'),
      '@stores': resolve(__dirname, 'src/stores'),
      '@assets': resolve(__dirname, 'src/assets'),
    },
  },

  server: {
    host: '127.0.0.1',
    port: parseInt(process.env.PORT || "9245"),
    strictPort: true,
  },

  build: {
    // Target modern browsers for better performance
    target: 'esnext',

    // Output directory for Wails
    outDir: 'dist',

    // Optimize chunk splitting
    rollupOptions: {
      output: {
        manualChunks: (id) => {
          if (id.includes('node_modules/vue') || id.includes('node_modules/pinia')) return 'vue'
          if (id.includes('node_modules/lucide-vue-next')) return 'icons'
          if (id.includes('node_modules/vue-virtual-scroller')) return 'scroller'
        },
      },
    },

    // Minification settings
    minify: true,
    cssMinify: true,

    // Enable source maps for debugging (disable in production if needed)
    sourcemap: false,

    // Chunk size warning limit
    chunkSizeWarningLimit: 1000,
  },

  // Optimize dependencies
  optimizeDeps: {
    include: ['vue', 'pinia', 'lucide-vue-next', 'vue-virtual-scroller'],
  },

  // CSS configuration
  css: {
    devSourcemap: true,
  },
})
