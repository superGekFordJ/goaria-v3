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
        manualChunks: {
          // Separate vendor chunks
          vue: ['vue', 'pinia'],
          icons: ['lucide-vue-next'],
          scroller: ['vue-virtual-scroller'],
        },
      },
    },

    // Minification settings
    minify: 'esbuild',
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
