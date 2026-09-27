import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  // Mirror the main vite config's isCustomElement so `<rela-slot>` and
  // `<rela-editor>` are treated as native custom elements under vitest too.
  plugins: [
    vue({
      template: {
        compilerOptions: {
          isCustomElement: (tag) => tag.startsWith('rela-'),
        },
      },
    }),
  ],
  // Mirror the vite `define` so components referencing the compile-time
  // __E2E_TEST_HOOKS__ flag don't ReferenceError under vitest. Off in unit
  // tests — test hooks are an E2E concern (issue #890).
  define: {
    __E2E_TEST_HOOKS__: 'false',
  },
  test: {
    globals: true,
    environment: 'happy-dom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{js,ts,vue}', '*.{test,spec}.ts'],
    // Milkdown's Timer.start() schedules a 3s setTimeout that is never
    // cleared, even after the timer resolves. When a file finishes sooner,
    // happy-dom removes its globals and the orphan timeout then calls the
    // bare removeEventListener. Drop only that error; any other still fails.
    onUnhandledError(error) {
      if (
        error.name === 'ReferenceError' &&
        error.message === 'removeEventListener is not defined' &&
        error.stack?.includes('@milkdown/ctx')
      ) {
        return false
      }
    },
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
})
