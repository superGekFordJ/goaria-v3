import { existsSync, rmSync, statSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execSync } from 'node:child_process'

const __dirname = dirname(fileURLToPath(import.meta.url))
const root = resolve(__dirname, '..')
const distDir = resolve(root, 'dist')

const targets = process.argv.slice(2)
const validTargets = targets.length > 0 ? targets : ['chrome', 'firefox']

for (const target of validTargets) {
  const targetDir = resolve(distDir, target)
  const zipPath = resolve(distDir, `goaria-extension-${target}.zip`)

  if (!existsSync(targetDir)) {
    console.error(`[zip] Error: Source directory does not exist: ${targetDir}`)
    process.exit(1)
  }

  // Remove existing zip to avoid 7z incremental additions of stale chunk hashes
  if (existsSync(zipPath)) {
    rmSync(zipPath, { force: true })
    console.log(`[zip] Cleaned stale archive: ${zipPath}`)
  }

  console.log(`[zip] Packaging ${target} from ${targetDir}...`)
  execSync(`7z a -tzip "${zipPath}" *`, {
    cwd: targetDir,
    stdio: 'ignore',
  })

  if (existsSync(zipPath)) {
    const size = statSync(zipPath).size
    console.log(
      `[zip] Successfully packaged ${target}: ${(size / 1024).toFixed(1)} KiB (${zipPath})`
    )
  }
}
