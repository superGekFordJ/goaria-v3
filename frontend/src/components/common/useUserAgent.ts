import { ref } from 'vue'

export interface UAPreset {
  ua: string
  os: string
  browser: string
}

export function useUserAgent() {
  const userAgentPresets = ref<UAPreset[]>([])
  const isFetchingUA = ref(false)
  const showUADropdown = ref(false)
  const uaFetchError = ref('')

  const parseUA = (ua: string): UAPreset => {
    let os = 'Unknown OS'
    if (ua.includes('Windows')) os = 'Windows'
    else if (ua.includes('Macintosh')) os = 'macOS'
    else if (ua.includes('Linux')) os = 'Linux'
    else if (ua.includes('Android')) os = 'Android'
    else if (ua.includes('iPhone')) os = 'iOS'
    else if (ua.includes('iPad')) os = 'iOS (iPad)'

    let browser = 'Unknown Browser'
    if (ua.includes('Edg/')) browser = 'Edge'
    else if (ua.includes('OPR/') || ua.includes('Opera')) browser = 'Opera'
    else if (ua.includes('Chrome/')) browser = 'Chrome'
    else if (ua.includes('Firefox/')) browser = 'Firefox'
    else if (ua.includes('Safari/') && !ua.includes('Chrome/')) browser = 'Safari'

    return { ua, os, browser }
  }

  const fetchUserAgents = async () => {
    if (isFetchingUA.value) return
    isFetchingUA.value = true
    uaFetchError.value = ''
    try {
      const response = await fetch(
        'https://raw.githubusercontent.com/microlinkhq/top-user-agents/master/src/index.json',
      )
      if (!response.ok) throw new Error('Failed to fetch')
      const data: string[] = await response.json()

      // Parse first 20 popular UAs
      userAgentPresets.value = data.slice(0, 24).map(parseUA)
      showUADropdown.value = true
    } catch {
      uaFetchError.value = '获取失败，请检查网络'
      setTimeout(() => {
        uaFetchError.value = ''
      }, 3000)
    } finally {
      isFetchingUA.value = false
    }
  }

  const closeUADropdown = () => {
    showUADropdown.value = false
  }

  return {
    userAgentPresets,
    isFetchingUA,
    showUADropdown,
    uaFetchError,
    fetchUserAgents,
    closeUADropdown,
  }
}
