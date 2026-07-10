import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

const config = {
  preprocess: vitePreprocess(),
  kit: {
    alias: {
      $lib: './src/lib',
      '$lib/*': './src/lib/*'
    },
    adapter: adapter({
      pages: 'build',
      assets: 'build',
      fallback: 'fallback.html',
      precompress: false,
      strict: true
    })
  }
};

export default config;
