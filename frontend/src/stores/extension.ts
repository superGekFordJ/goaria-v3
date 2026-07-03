import { defineStore } from 'pinia'
import { ref } from 'vue'
import { Events } from '@wailsio/runtime'
import { GetExtensionStatus, PairExtension, UnpairExtension } from '../../bindings/goaria-v3/app.js'
import { ExtensionStatus } from '../../bindings/goaria-v3/internal/extension/models.js'

export const useExtensionStore = defineStore('extension', () => {
  const status = ref<'disconnected' | 'listening' | 'paired'>('disconnected')
  const wsPort = ref(0)
  const connectedClients = ref(0)
  const paired = ref(false)
  const pairing = ref(false)
  const pairUrl = ref('')

  let statusUnsubscribe: (() => void) | null = null
  let pairedUnsubscribe: (() => void) | null = null
  let unpairedUnsubscribe: (() => void) | null = null

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
      await refreshStatus()
    } catch (err) {
      console.error('Failed to unpair extension:', err)
    }
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
      paired.value = true
      status.value = 'paired'
      pairUrl.value = ''
      refreshStatus()
    })

    unpairedUnsubscribe = Events.On('extension:unpaired', () => {
      paired.value = false
      status.value = 'listening'
      pairUrl.value = ''
      refreshStatus()
    })
  }

  function unsubscribeFromEvents() {
    statusUnsubscribe?.()
    pairedUnsubscribe?.()
    unpairedUnsubscribe?.()
    statusUnsubscribe = null
    pairedUnsubscribe = null
    unpairedUnsubscribe = null
  }

  return {
    status,
    wsPort,
    connectedClients,
    paired,
    pairing,
    pairUrl,
    refreshStatus,
    pair,
    unpair,
    subscribeToEvents,
    unsubscribeFromEvents,
  }
})
