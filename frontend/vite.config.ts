import { defineConfig } from 'vite';

export default defineConfig({
  build: { target: 'safari15', outDir: 'dist', emptyOutDir: true, chunkSizeWarningLimit: 2000 },
});
