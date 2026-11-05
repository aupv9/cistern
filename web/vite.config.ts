import { defineConfig, type Plugin } from 'vite'
import { tanstackStart } from '@tanstack/react-start/plugin/vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { msw } from 'msw/vite'

function mockSignIn(): Plugin {
  return {
    name: 'cistern-mock-sign-in',
    configureServer(server) {
      server.middlewares.use('/api/v1/auth/login', (req, res) => {
        const to = new URL(req.originalUrl ?? '/', 'http://localhost').searchParams.get('return_to') ?? '/'
        res.statusCode = 302
        res.setHeader('Location', to.startsWith('/') && !to.startsWith('//') ? to : '/')
        res.end()
      })
    },
  }
}

export default defineConfig(({ mode }) => {
  if (mode === 'mock') process.env.CISTERN_UI_MOCK = '1'
  const mock = process.env.CISTERN_UI_MOCK === '1'
  return {
    server: {
      port: 5480,
      strictPort: true,
      proxy: mock
        ? undefined
        : {
            '/api': {
              target: 'http://127.0.0.1:8480',
            },
          },
    },
    build: {
      outDir: mock ? 'dist-mock' : 'dist',
    },
    preview: {
      port: 4173,
      strictPort: true,
    },
    resolve: {
      tsconfigPaths: true,
    },
    plugins: [
      tailwindcss(),
      tanstackStart({
        spa: { enabled: true },
        sitemap: { enabled: false },
      }),
      react(),
      ...(mock ? [msw({ mode: 'worker-only' }), mockSignIn()] : []),
    ],
  }
})
