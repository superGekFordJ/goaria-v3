import { defineStore } from 'pinia'
import { ref, reactive } from 'vue'
import {
  GetConfig,
  SaveConfig,
  SelectDirectory,
} from '../../bindings/goaria-v3/internal/wailsapp/app.js'
import { AppConfig } from '../../bindings/goaria-v3/internal/config/models.js'

export const useConfigStore = defineStore(
  'config',
  () => {
    const settings = reactive<AppConfig>(new AppConfig())
    const isLoading = ref(false)
    const isSaving = ref(false)

    /**
     * Fetch configuration from Go backend
     */
    async function fetchConfig() {
      isLoading.value = true
      try {
        const res = await GetConfig()
        if (res) {
          Object.assign(settings, res)
        }
      } catch (err) {
        console.error('Failed to fetch config:', err)
      } finally {
        isLoading.value = false
      }
    }

    /**
     * Save current settings to Go backend
     */
    async function updateConfig() {
      isSaving.value = true
      try {
        // Ensure numeric fields are strings for the Go backend
        const cfg = { ...settings }
        cfg.rpc_port = String(cfg.rpc_port ?? '')
        cfg.max_connections = String(cfg.max_connections ?? '')
        cfg.max_concurrent_downloads = String(cfg.max_concurrent_downloads ?? '')

        const result = await SaveConfig(cfg as AppConfig)
        // Backend returns "success" or error message
        if (result !== 'success') {
          throw new Error(result)
        }
        return true
      } catch (err) {
        console.error('Failed to save config:', err)
        return false
      } finally {
        isSaving.value = false
      }
    }

    /**
     * Open native directory picker and update download_dir
     */
    async function pickDirectory(): Promise<string | null> {
      try {
        const path = await SelectDirectory()
        if (path) {
          settings.download_dir = path
          return path
        }
        return null
      } catch (err) {
        console.error('Failed to select directory:', err)
        return null
      }
    }

    return {
      settings,
      isLoading,
      isSaving,
      fetchConfig,
      updateConfig,
      pickDirectory,
    }
  },
  {
    persist: true, // Enable persistence via pinia-plugin-persistedstate
  },
)
