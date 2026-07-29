import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const buildMode = env.VITE_LOCAL === '1' ? 'local' : 'server'
  return {
    plugins: [
      vue(),
      {
        name: 'memodump-build-mode',
        generateBundle() {
          this.emitFile({
            type: 'asset',
            fileName: 'build-mode.json',
            source: JSON.stringify({ mode: buildMode }) + '\n',
          })
        },
      },
    ],
    server: {
      proxy: {
        '/api': 'http://127.0.0.1:8080'
      }
    }
  }
})
