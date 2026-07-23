import {
  getBaseManifest,
  getBrowserActionInfo,
  getBackgroundScript,
  getHostPermissions,
  getCommonPermissions,
} from './shared'

// Firefox MV3 retains webRequestBlocking (Mozilla blog + Bugzilla 1820569 + MDN).
// The claim "manifest version 3 does not allow request blocking" applies only to Chrome MV3.
export function getManifestForFirefox() {
  return {
    ...getBaseManifest(),
    background: {
      scripts: [getBackgroundScript()],
    },
    action: getBrowserActionInfo(),
    browser_specific_settings: {
      gecko: {
        id: 'goaria-integration@goaria.app',
        strict_min_version: '109.0',
        data_collection_permissions: {
          required: ['none'],
        },
      },
    },
    host_permissions: getHostPermissions(),
    permissions: [...getCommonPermissions(), 'webRequestBlocking'],
  }
}
