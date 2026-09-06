#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(__dirname, '..')

const infoJsonPath = path.join(repoRoot, 'build', 'windows', 'info.json')
const configYmlPath = path.join(repoRoot, 'build', 'config.yml')

let version = ''
if (fs.existsSync(infoJsonPath)) {
  try {
    const data = JSON.parse(fs.readFileSync(infoJsonPath, 'utf8'))
    version = data.fixed?.file_version || data.info?.['0000']?.ProductVersion || ''
  } catch {}
}

if (!version && fs.existsSync(configYmlPath)) {
  const content = fs.readFileSync(configYmlPath, 'utf8')
  const match = content.match(/^\s*version:\s*['"]?([^'"\r\n]+)['b]?/m)
  if (match) {
    version = match[1]
  }
}

process.stdout.write(version)
