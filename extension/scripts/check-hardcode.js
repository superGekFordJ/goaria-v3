import { readdirSync, readFileSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve, relative, join, dirname } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const root = resolve(__dirname, '..')
const srcDir = resolve(root, 'src')
const chineseRe = /[\u4e00-\u9fff]/

const errors = []
const warnings = []

function scanDir(dir, files = []) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      scanDir(full, files)
    } else if (entry.endsWith('.svelte') || entry.endsWith('.ts')) {
      files.push(full)
    }
  }
  return files
}

// Line-by-line scanner: tracks block/html comment and string state to
// classify Chinese characters as comment (warning) or code/string (error).
function scanFile(file) {
  const lines = readFileSync(file, 'utf8').split('\n')
  let inBlock = false
  let inHtml = false

  lines.forEach((line, idx) => {
    let inStr = false
    let strCh = null
    let codeHit = false
    let commentHit = false
    let i = 0

    while (i < line.length) {
      const ch = line[i]
      const two = line.slice(i, i + 2)
      const four = line.slice(i, i + 4)

      if (inBlock) {
        if (two === '*/') {
          inBlock = false
          i += 2
          continue
        }
        if (chineseRe.test(ch)) commentHit = true
        i++
        continue
      }

      if (inHtml) {
        if (line.slice(i, i + 3) === '-->') {
          inHtml = false
          i += 3
          continue
        }
        if (chineseRe.test(ch)) commentHit = true
        i++
        continue
      }

      if (inStr) {
        if (ch === '\\') {
          i += 2
          continue
        }
        if (ch === strCh) {
          inStr = false
          i++
          continue
        }
        if (chineseRe.test(ch)) codeHit = true
        i++
        continue
      }

      if (two === '//') {
        if (chineseRe.test(line.slice(i))) commentHit = true
        break
      }
      if (two === '/*') {
        inBlock = true
        i += 2
        continue
      }
      if (four === '<!--') {
        inHtml = true
        i += 4
        continue
      }
      if (ch === "'" || ch === '"' || ch === '`') {
        inStr = true
        strCh = ch
        i++
        continue
      }
      if (chineseRe.test(ch)) codeHit = true
      i++
    }

    const rel = relative(root, file)
    const ln = idx + 1
    if (codeHit) errors.push(`${rel}:${ln}: error: hardcoded Chinese in code/string`)
    if (commentHit) warnings.push(`${rel}:${ln}: warning: Chinese in comment`)
  })
}

const files = scanDir(srcDir)
for (const f of files) scanFile(f)

for (const e of errors) console.log(e)
for (const w of warnings) console.log(w)

console.log(`\n${errors.length} error(s), ${warnings.length} warning(s)`)

if (errors.length > 0) process.exit(1)
