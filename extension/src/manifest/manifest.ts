import { getManifestForChrome } from './manifest.chrome'
import { getManifestForFirefox } from './manifest.firefox'
import { isFirefox } from '../utils/extensionInfo'

export function getManifest() {
  return isFirefox() ? getManifestForFirefox() : getManifestForChrome()
}
