import { createReadStream, existsSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { extname, join, normalize, resolve } from 'node:path'

const root = resolve(process.argv[2] ?? 'dist-mock/client')
const port = Number(process.argv[3] ?? 4173)
const types = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
}

function send(res, file, status = 200) {
  res.writeHead(status, {
    'Content-Type': types[extname(file)] ?? 'application/octet-stream',
    'Cache-Control': file.includes(`${join('', 'assets')}`) ? 'public, max-age=31536000, immutable' : 'no-cache',
  })
  createReadStream(file).pipe(res)
}

createServer((req, res) => {
  const url = new URL(req.url ?? '/', 'http://localhost')
  if (url.pathname === '/api/v1/auth/login') {
    const to = url.searchParams.get('return_to') ?? '/'
    res.writeHead(302, { Location: to.startsWith('/') && !to.startsWith('//') ? to : '/' })
    res.end()
    return
  }
  if (url.pathname.startsWith('/api/')) {
    res.writeHead(404, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ error: { code: 'not_found', message: 'This static server only serves the UI.' } }))
    return
  }
  const file = normalize(join(root, decodeURIComponent(url.pathname)))
  if (file.startsWith(root) && existsSync(file) && statSync(file).isFile()) {
    send(res, file)
    return
  }
  send(res, join(root, '_shell.html'))
}).listen(port, '127.0.0.1', () => {
  console.log(`serving ${root} on http://127.0.0.1:${port}`)
})
