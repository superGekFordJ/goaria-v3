#!/usr/bin/env node
import { execSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(__dirname, '..')

function runTask(taskCmd, extraEnv = {}) {
  console.log(`\n>> Executing: ${taskCmd} (overrides: ${JSON.stringify(extraEnv)})`)
  execSync(taskCmd, {
    cwd: repoRoot,
    stdio: 'inherit',
    env: { ...process.env, ...extraEnv },
  })
}

function verifyArtifacts(variant) {
  console.log(`>> Verifying artifact closure for: ${variant}`)
  execSync(`node scripts/check_frontend_extractor_artifacts.mjs ${variant}`, {
    cwd: repoRoot,
    stdio: 'inherit',
  })
}

console.log('=== Step 1: Generic build with conflicting inherited VITE_GOARIA_EXTRACTOR=true ===')
runTask('wails3 task common:build:frontend EXTRACTOR_VARIANT=generic-no-pack', {
  VITE_GOARIA_EXTRACTOR: 'true',
})
verifyArtifacts('generic-no-pack')

console.log('\n=== Step 2: Tagged build with conflicting inherited VITE_GOARIA_EXTRACTOR=false ===')
runTask('wails3 task common:build:frontend EXTRACTOR_VARIANT=full-pack', {
  VITE_GOARIA_EXTRACTOR: 'false',
})
verifyArtifacts('full-pack')

console.log('\n=== Step 3: Switchback to Generic without manual clean (stamp invalidation) ===')
runTask('wails3 task common:build:frontend EXTRACTOR_VARIANT=generic-no-pack', {
  VITE_GOARIA_EXTRACTOR: 'true',
})
verifyArtifacts('generic-no-pack')

console.log('\nAll variant closure, wrapper enforcement, and back-to-back switch tests PASSED.')
