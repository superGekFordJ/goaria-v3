import pkg from '../../package.json' with { type: 'json' }

export const EXTENSION_NAME = 'GoAria'
export const EXTENSION_DESCRIPTION = 'Download manager integration for GoAria'
export const EXTENSION_VERSION = pkg.version

export function getBaseManifest() {
  return {
    name: EXTENSION_NAME,
    description: EXTENSION_DESCRIPTION,
    version: EXTENSION_VERSION,
    manifest_version: 3,
    icons: {
      48: 'icons/icon-48.png',
      96: 'icons/icon-96.png',
      128: 'icons/icon-128.png',
    },
    content_scripts: [
      {
        matches: ['*://*/*'],
        js: ['src/contentscripts/contentScript.ts'],
      },
      {
        // Omitting the port matches any port (manifest patterns can't pin a
        // port). The unique /__goaria_pair__/ path narrows the attack surface.
        matches: ['http://127.0.0.1/__goaria_pair__/pair.html'],
        js: ['src/contentscripts/pair.ts'],
      },
    ],
    web_accessible_resources: [
      {
        resources: ['icons/*'],
        matches: ['*://*/*'],
      },
    ],
  }
}

export function getBrowserActionInfo() {
  return {
    default_title: EXTENSION_NAME,
    default_popup: 'src/popup/popup.html',
    default_icon: {
      48: 'icons/icon-48.png',
      128: 'icons/icon-128.png',
    },
  }
}

export function getBackgroundScript() {
  return 'src/background/background.ts'
}

export function getHostPermissions() {
  return ['*://*/*']
}

export function getCommonPermissions() {
  return ['webRequest', 'cookies', 'storage', 'tabs', 'downloads', 'notifications']
}
