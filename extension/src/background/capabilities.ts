export type AuthAckInput = {
  type?: unknown
  protocol_version?: unknown
  host_version?: unknown
  capabilities?: unknown
}

export type ParsedAuthAck = {
  protocolVersion: number
  hostVersion: string
  // undefined means the field was missing or JSON null (legacy host).
  capabilities: string[] | undefined
}

export function parseAuthAck(msg: AuthAckInput): ParsedAuthAck {
  const protocolVersion = typeof msg.protocol_version === 'number' ? msg.protocol_version : 0
  const hostVersion = typeof msg.host_version === 'string' ? msg.host_version : ''
  if (msg.capabilities == null) {
    return { protocolVersion, hostVersion, capabilities: undefined }
  }
  if (!Array.isArray(msg.capabilities)) {
    return { protocolVersion, hostVersion, capabilities: undefined }
  }
  return {
    protocolVersion,
    hostVersion,
    capabilities: msg.capabilities.filter((c): c is string => typeof c === 'string'),
  }
}

export function isLegacyHost(ack: ParsedAuthAck): boolean {
  return ack.protocolVersion === 0 || ack.capabilities === undefined
}

export function hasCapability(capabilities: string[] | undefined | null, name: string): boolean {
  if (!capabilities) return false
  return capabilities.includes(name)
}
