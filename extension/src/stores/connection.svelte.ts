type ConnectionStatus = 'disconnected' | 'connecting' | 'connected'

class ConnectionState {
  status = $state<ConnectionStatus>('disconnected')
  wsPort = $state(0)
  paired = $state(false)
  lastError = $state('')

  get isConnected() {
    return this.status === 'connected'
  }
}

export const connectionState = new ConnectionState()
