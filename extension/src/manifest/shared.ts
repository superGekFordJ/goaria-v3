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
      16: 'icons/icon-16.png',
      32: 'icons/icon-32.png',
      48: 'icons/icon-48.png',
      128: 'icons/icon-128.png',
    },
    // Firefox MV3 upgrades ws:// to wss:// via the default CSP
    // upgrade-insecure-requests directive (Bug 1676024); the GoAria backend
    // has no TLS so the connection would fail. Explicit CSP keeps plain ws://
    // by omitting upgrade-insecure-requests and allowing 127.0.0.1 in
    // connect-src. If future extension pages need remote fetches, widen
    // connect-src accordingly.
    content_security_policy: {
      extension_pages:
        "script-src 'self' 'wasm-unsafe-eval'; object-src 'self'; connect-src ws://127.0.0.1:* http://127.0.0.1:*;",
    },
    content_scripts: [
      {
        matches: ['*://*/*'],
        js: ['src/contentscripts/contentScript.ts'],
      },
      {
        // Firefox path matching includes the query string (?n=<nonce>), so a
        // pattern without a trailing * won't match the pairing URL. Both
        // Firefox and Chrome ignore the port in match patterns, so omitting
        // the port still matches any port. The trailing * absorbs the query.
        // pair.ts also self-filters by pathname as a defensive guard.
        // If precise injection is found not to work in practice, fall back to
        // a global match (*://*/*) with the pathname guard as the sole filter.
        matches: ['http://127.0.0.1/__goaria_pair__/pair.html*'],
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
      16: 'icons/icon-16.png',
      32: 'icons/icon-32.png',
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
