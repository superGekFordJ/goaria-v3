function cryptoOrUndefined(): Crypto | undefined {
  return typeof globalThis !== 'undefined' ? globalThis.crypto : undefined
}

function randomBytes(size: number): Uint8Array {
  const bytes = new Uint8Array(size)
  const c = cryptoOrUndefined()
  if (c && typeof c.getRandomValues === 'function') {
    c.getRandomValues(bytes)
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256)
    }
  }
  return bytes
}

function toHex(bytes: Uint8Array): string {
  return [...bytes].map(b => b.toString(16).padStart(2, '0')).join('')
}

export function mintRequestId(): string {
  const c = cryptoOrUndefined()
  if (c && typeof c.randomUUID === 'function') {
    return c.randomUUID()
  }
  return toHex(randomBytes(32))
}

/** RFC 4122 36-char lowercase UUID. Host rejects any other request_id shape. */
export function mintDirectBatchRequestId(): string {
  const c = cryptoOrUndefined()
  if (c && typeof c.randomUUID === 'function') {
    return c.randomUUID().toLowerCase()
  }
  const hex = toHex(randomBytes(16))
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

/** 32 lowercase hex; unique per item inside a direct batch request. */
export function mintClientItemId(): string {
  return toHex(randomBytes(16))
}
