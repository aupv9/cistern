import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

const out = process.argv[2] ?? 'dist'
const client = resolve(out, 'client')
const shell = join(client, '_shell.html')
const mock = out !== 'dist'
const problems = []

if (!existsSync(shell)) problems.push(`missing ${shell}`)

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

if (existsSync(client)) {
  const files = walk(client)
  const worker = files.some((f) => f.endsWith('mockServiceWorker.js'))
  if (mock && !worker) problems.push('the mock build has no mockServiceWorker.js')
  if (!mock && worker) problems.push('the production build contains mockServiceWorker.js')
  for (const f of files.filter((x) => /\.(html|css|js)$/.test(x))) {
    const text = readFileSync(f, 'utf8')
    if (/fonts\.googleapis|fonts\.gstatic|cdn\.jsdelivr|unpkg\.com/.test(text)) problems.push(`${f} references an external CDN`)
    if (!mock && /mock-csrf-token/.test(text)) problems.push(`${f} contains mock fixtures`)
  }
}

if (problems.length) {
  for (const p of problems) console.error(`check-build: ${p}`)
  process.exit(1)
}
console.log(`check-build: ok, static client in ${client} with _shell.html`)
