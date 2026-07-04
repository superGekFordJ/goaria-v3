import {
  getBaseManifest,
  getBrowserActionInfo,
  getBackgroundScript,
  getHostPermissions,
  getCommonPermissions,
} from './shared'

// Chrome MV3 does not support webRequestBlocking.
// Interception uses the downloads API path B (future plan).
export function getManifestForChrome() {
  return {
    ...getBaseManifest(),
    background: {
      service_worker: getBackgroundScript(),
    },
    action: getBrowserActionInfo(),
    host_permissions: getHostPermissions(),
    permissions: getCommonPermissions(),
  }
}
