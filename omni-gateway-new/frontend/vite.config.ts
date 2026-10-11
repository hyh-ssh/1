import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

const backend = process.env.GATEWAY_DEV_URL || 'http://localhost:8080'

export default defineConfig({
	plugins: [vue()],
	server: { port: 5173, proxy: { '/api': backend, '/v1': backend, '/healthz': backend, '/readyz': backend } },
	preview: { port: 4173, proxy: { '/api': 'http://backend:8080', '/v1': 'http://backend:8080', '/healthz': 'http://backend:8080', '/readyz': 'http://backend:8080' } },
	test: {
		environment: 'jsdom',
		include: ['src/**/*.spec.ts'],
	},
})
