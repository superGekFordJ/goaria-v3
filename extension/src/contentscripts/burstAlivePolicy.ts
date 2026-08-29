import { isFirefox } from '../utils/extensionInfo'

export function burstAliveMissShouldCancel(): boolean {
  return !isFirefox()
}
