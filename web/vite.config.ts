import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import adapter from '@sveltejs/adapter-static';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit({
      adapter: adapter({
        pages: 'build',
        assets: 'build',
        fallback: 'index.html',
        precompress: false,
        strict: false
      })
    })],
  server: {
    host: true,
    proxy: {
      '/api': 'http://192.168.10.5:8080',
      '/dav': 'http://192.168.10.5:8080',
      '/sabnzbd': 'http://192.168.10.5:8080'
    }
  }
});
