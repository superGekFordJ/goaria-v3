import { defineStore } from 'pinia'
import { computed, reactive, ref } from 'vue'
import {
  GetConfig,
  GetAria2Connected,
  SaveConfig,
  SelectDirectory,
} from '../../bindings/goaria-v3/internal/wailsapp/app.js'
import { AppConfig } from '../../bindings/goaria-v3/internal/config/models.js'
import { SaveConfigResult } from '../../bindings/goaria-v3/internal/wailsapp/models.js'

function cloneConfig(candidate: AppConfig): AppConfig {
  const cfg = new AppConfig({ ...candidate })
  cfg.rpc_port = String(cfg.rpc_port ?? '')
  cfg.max_connections = String(cfg.max_connections ?? '')
  cfg.max_concurrent_downloads = String(cfg.max_concurrent_downloads ?? '')
  return cfg
}

export const useConfigStore = defineStore(
  'config',
  () => {
    const settings = reactive<AppConfig>(new AppConfig())
    const fetchInFlight = ref(0)
    const isLoading = computed(() => fetchInFlight.value > 0)
    const isHydrated = ref(false)
    const hydrateFailed = ref(false)
    const saveInFlight = ref(0)
    const isSaving = computed(() => saveInFlight.value > 0)
    const aria2Connected = ref(false)

    function setAria2Connected(connected: boolean) {
      aria2Connected.value = connected
    }

    function applyCanonicalConfig(snapshot: AppConfig | null | undefined) {
      if (!snapshot) return
      Object.assign(settings, cloneConfig(snapshot))
    }

    /**
     * Fetch current Aria2 WebSocket connection status from Go backend
     */
    async function refreshAria2Connected() {
      try {
        const ok = await GetAria2Connected()
        aria2Connected.value = Boolean(ok)
      } catch (err) {
        if (import.meta.env.DEV) {
          console.warn('[Config] Failed to get Aria2 connection status:', err)
        }
        aria2Connected.value = false
      }
    }

    /**
     * Fetch configuration from Go backend
     */
    async function fetchConfig() {
      fetchInFlight.value++
      try {
        const res = await GetConfig()
        if (res) {
          applyCanonicalConfig(res)
          isHydrated.value = true
          hydrateFailed.value = false
          return
        }
        if (!isHydrated.value) {
          hydrateFailed.value = true
        }
      } catch {
        if (!isHydrated.value) {
          hydrateFailed.value = true
        }
      } finally {
        fetchInFlight.value--
      }
    }

    /**
     * Save a complete candidate snapshot. Does not write result.config into settings.
     */
    async function updateConfig(candidate: AppConfig): Promise<SaveConfigResult> {
      const request = cloneConfig(candidate)
      saveInFlight.value++
      try {
        return await SaveConfig(request)
      } finally {
        saveInFlight.value--
      }
    }

    /**
     * Open native directory picker. Does not mutate settings.
     */
    async function pickDirectory(): Promise<string | null> {
      try {
        const path = await SelectDirectory()
        return path || null
      } catch (err) {
        console.error('Failed to select directory:', err)
        return null
      }
    }

    return {
      settings,
      isLoading,
      isHydrated,
      hydrateFailed,
      isSaving,
      aria2Connected,
      setAria2Connected,
      refreshAria2Connected,
      fetchConfig,
      updateConfig,
      applyCanonicalConfig,
      pickDirectory,
    }
  },
  {
    persist: {
      pick: ['settings'],
    },
  },
)
