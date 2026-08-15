import { defineStore } from 'pinia'
import { ref } from 'vue'
import { Browser, Events } from '@wailsio/runtime'
import {
  GetExtensionStatus,
  PairExtension,
  UnpairExtension,
  RegeneratePairing,
} from '../../bindings/goaria-v3/internal/wailsapp/app.js'
import { ExtensionStatus } from '../../bindings/goaria-v3/internal/extension/models.js'
import { copyToClipboard, clearClipboardIfMatches } from '../utils/clipboard'

export const useExtensionStore = defineStore('extension', () => {
  const status = ref<'disconnected' | 'listening' | 'paired'>('disconnected')
  const wsPort = ref(0)
  const connectedClients = ref(0)
  const paired = ref(false)
  const pairing = ref(false)
  const pairUrl = ref('')
  const pairingPanelOpen = ref(false)
  const regenerating = ref(false)
  // No shared toast store exists in the frontend; ExtensionSection watches this ref.
  const authFailedNotice = ref(false)
  const unpairRotatedNotice = ref(false)
  const showUnpairConfirm = ref(false)

  let statusUnsubscribe: (() => void) | null = null
  let pairedUnsubscribe: (() => void) | null = null
  let unpairedUnsubscribe: (() => void) | null = null
  let authFailedUnsubscribe: (() => void) | null = null

  async function refreshStatus() {
    try {
      const res = await GetExtensionStatus()
      if (res) {
        status.value = (res.status as typeof status.value) || 'disconnected'
        wsPort.value = res.ws_port || 0
        connectedClients.value = res.connected_clients || 0
        paired.value = Boolean(res.paired)
      }
    } catch (err) {
      console.error('Failed to get extension status:', err)
    }
  }

  async function pair() {
    if (pairing.value) return
    pairing.value = true
    try {
      const url = await PairExtension()
      if (url) {
        pairUrl.value = url
        pairingPanelOpen.value = true
      }
      await refreshStatus()
    } catch (err) {
      console.error('Failed to pair extension:', err)
    } finally {
      pairing.value = false
    }
  }

  async function unpair() {
    try {
      await UnpairExtension()
      paired.value = false
      status.value = 'listening'
      pairUrl.value = ''
      unpairRotatedNotice.value = true
      showUnpairConfirm.value = false
      await refreshStatus()
    } catch (err) {
      console.error('Failed to unpair extension:', err)
    }
  }

  function requestUnpair() {
    showUnpairConfirm.value = true
  }

  function cancelUnpair() {
    showUnpairConfirm.value = false
  }

  async function regenerate() {
    if (regenerating.value) return
    regenerating.value = true
    try {
      const url = await RegeneratePairing()
      if (url) {
        pairUrl.value = url
      }
    } catch (err) {
      console.error('Failed to regenerate pairing:', err)
    } finally {
      regenerating.value = false
    }
  }

  async function openInBrowser(url: string) {
    if (!url) return
    try {
      await Browser.OpenURL(url)
    } catch (err) {
      console.error('Failed to open pairing URL in browser:', err)
    }
  }

  async function copyPairUrl(): Promise<boolean> {
    return copyToClipboard(pairUrl.value)
  }

  function subscribeToEvents() {
    if (statusUnsubscribe) return

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    statusUnsubscribe = Events.On('extension:status', (ev: any) => {
      const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as ExtensionStatus
      if (data) {
        status.value = (data.status as typeof status.value) || 'disconnected'
        wsPort.value = data.ws_port || 0
        connectedClients.value = data.connected_clients || 0
        paired.value = Boolean(data.paired)
      }
    })

    pairedUnsubscribe = Events.On('extension:paired', () => {
      const consumedUrl = pairUrl.value
      paired.value = true
      status.value = 'paired'
      pairUrl.value = ''
      pairingPanelOpen.value = false
      if (consumedUrl) {
        void clearClipboardIfMatches(consumedUrl)
      }
      refreshStatus()
    })

    unpairedUnsubscribe = Events.On('extension:unpaired', () => {
      paired.value = false
      status.value = 'listening'
      pairUrl.value = ''
      refreshStatus()
    })

    authFailedUnsubscribe = Events.On('extension:auth_failed', () => {
      paired.value = false
      status.value = 'listening'
      pairUrl.value = ''
      authFailedNotice.value = true
    })
  }

  function unsubscribeFromEvents() {
    statusUnsubscribe?.()
    pairedUnsubscribe?.()
    unpairedUnsubscribe?.()
    authFailedUnsubscribe?.()
    statusUnsubscribe = null
    pairedUnsubscribe = null
    unpairedUnsubscribe = null
    authFailedUnsubscribe = null
  }

  return {
    status,
    wsPort,
    connectedClients,
    paired,
    pairing,
    pairUrl,
    pairingPanelOpen,
    regenerating,
    authFailedNotice,
    unpairRotatedNotice,
    showUnpairConfirm,
    refreshStatus,
    pair,
    unpair,
    requestUnpair,
    cancelUnpair,
    regenerate,
    openInBrowser,
    copyPairUrl,
    subscribeToEvents,
    unsubscribeFromEvents,
  }
})
