import { defineConfig, type PluginOption } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import webExtension from '@samrum/vite-plugin-web-extension'
import EnvironmentPlugin from 'vite-plugin-environment'
import path from 'path'
import { getManifest } from './src/manifest/manifest'
import { getExtensionBrowserTarget } from './src/utils/extensionInfo'

export default defineConfig(() => ({
  build: {
    outDir: `dist/${getExtensionBrowserTarget()}`,
    emptyOutDir: true,
  },
  resolve: {
    alias: {
      '~': path.resolve(__dirname, './src'),
    },
  },
  plugins: [
    svelte(),
    EnvironmentPlugin({ EXTENSION_TARGET: 'chrome' }),
    webExtension({ manifest: getManifest() }) as PluginOption,
  ],
}))
