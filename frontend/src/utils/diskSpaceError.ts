/** Stable English sentinel from Surge ErrInsufficientDiskSpace. */
export const INSUFFICIENT_DISK_SPACE_SENTINEL = 'insufficient disk space'

export function isInsufficientDiskSpaceMessage(msg: string | null | undefined): boolean {
  if (!msg) return false
  return msg.includes(INSUFFICIENT_DISK_SPACE_SENTINEL)
}

export function isInsufficientDiskSpaceCode(code: string | null | undefined): boolean {
  return code === '9'
}

export function isInsufficientDiskSpaceFailure(
  code?: string | null,
  message?: string | null,
): boolean {
  return isInsufficientDiskSpaceCode(code) || isInsufficientDiskSpaceMessage(message)
}
