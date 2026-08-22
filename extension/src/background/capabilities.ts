import type { MatchDigestSnapshot } from './digestMatch'
import { isValidMatchSnapshot } from './digestMatch'

export type AuthAckInput = {
  type?: unknown
  protocol_version?: unknown
  host_version?: unknown
  capabilities?: unknown
  match?: unknown
}

export type ParsedAuthAck = {
  protocolVersion: number
  hostVersion: string
  // undefined is fail-closed; wireState distinguishes legacy absence from malformed data.
  capabilities: string[] | undefined
  capabilitiesWireState: 'missing' | 'valid' | 'malformed'
  match?: MatchDigestSnapshot
}

export function parseAuthAck(msg: AuthAckInput): ParsedAuthAck {
  const protocolVersion = typeof msg.protocol_version === 'number' ? msg.protocol_version : 0
  const hostVersion = typeof msg.host_version === 'string' ? msg.host_version : ''
  const match = parseMatchObject(msg.match)
  if (msg.capabilities == null) {
    return {
      protocolVersion,
      hostVersion,
      capabilities: undefined,
      capabilitiesWireState: 'missing',
      match,
    }
  }
  if (!Array.isArray(msg.capabilities)) {
    return {
      protocolVersion,
      hostVersion,
      capabilities: undefined,
      capabilitiesWireState: 'malformed',
      match,
    }
  }
  return {
    protocolVersion,
    hostVersion,
    capabilities: msg.capabilities.filter((c): c is string => typeof c === 'string'),
    capabilitiesWireState: 'valid',
    match,
  }
}

function parseMatchObject(raw: unknown): MatchDigestSnapshot | undefined {
  if (raw == null || typeof raw !== 'object' || Array.isArray(raw)) {
    return undefined
  }
  const obj = raw as Record<string, unknown>
  const candidate = {
    digest_version: obj.digest_version,
    salt: obj.salt,
    exact_digests: obj.exact_digests,
    subdomain_digests: obj.subdomain_digests,
  }
  if (!isValidMatchSnapshot(candidate as MatchDigestSnapshot)) {
    return undefined
  }
  return {
    digest_version: candidate.digest_version as number,
    salt: candidate.salt as string,
    exact_digests: [...(candidate.exact_digests as string[])],
    subdomain_digests: [...(candidate.subdomain_digests as string[])],
  }
}

export function isLegacyHost(ack: ParsedAuthAck): boolean {
  return deriveLegacyHostState(ack) === true
}

export function deriveLegacyHostState(ack?: ParsedAuthAck): boolean | undefined {
  if (!ack || ack.capabilitiesWireState === 'malformed') return undefined
  return ack.protocolVersion === 0 || ack.capabilitiesWireState === 'missing'
}

export function hasCapability(capabilities: string[] | undefined | null, name: string): boolean {
  if (!capabilities) return false
  return capabilities.includes(name)
}
