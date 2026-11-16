import { fileURLToPath } from 'node:url';
import vue from '@vitejs/plugin-vue';

export default {
  root: fileURLToPath(new URL('.', import.meta.url)),
  plugins: [vue()],
  server: {
    host: '127.0.0.1',
    port: 19368,
    strictPort: true,
    fs: { allow: [fileURLToPath(new URL('../..', import.meta.url))] },
    proxy: { '/api': 'http://127.0.0.1:19367' },
  },
};
