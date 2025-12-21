import { defineStore } from 'pinia'
import { ref } from 'vue'

export const useUIStore = defineStore(
  'ui',
  () => {
    // State
    const activeTab = ref('downloads')

    // Actions
    function setActiveTab(tab: string) {
      activeTab.value = tab
    }

    return {
      activeTab,
      setActiveTab,
    }
  },
  {
    persist: true,
  },
)
