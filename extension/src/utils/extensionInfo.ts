export type ExtensionTarget = 'chrome' | 'firefox'

export function getExtensionBrowserTarget(): ExtensionTarget {
  const target = process.env.EXTENSION_TARGET
  if (target === 'firefox') return 'firefox'
  return 'chrome'
}

export function isFirefox(): boolean {
  return getExtensionBrowserTarget() === 'firefox'
}

export function isChrome(): boolean {
  return getExtensionBrowserTarget() === 'chrome'
}
