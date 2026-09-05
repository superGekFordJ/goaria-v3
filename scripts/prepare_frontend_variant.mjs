#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(__dirname, '..')
const cacheDir = path.join(repoRoot, 'build', 'extractor', 'cache')
const stampFile = path.join(cacheDir, '.frontend_variant_stamp')
const distDir = path.join(repoRoot, 'frontend', 'dist')

const desiredStamp = process.argv[2] === 'true' ? 'true' : 'false'

fs.mkdirSync(cacheDir, { recursive: true })

let currentStamp = ''
if (fs.existsSync(stampFile)) {
  currentStamp = fs.readFileSync(stampFile, 'utf8').trim()
}

if (currentStamp !== desiredStamp) {
  fs.writeFileSync(stampFile, desiredStamp, 'utf8')
  if (fs.existsSync(distDir)) {
    fs.rmSync(distDir, { recursive: true, force: true })
  }
}
