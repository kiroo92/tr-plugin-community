import { defineConfig, mergeConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { tokenrouterPlugin } from '@tokenflux/tokenrouter-plugin-sdk/vite'

// vite.config.ts builds the community frontend under its manifest identity.
// The SDK scopes production styles to the moonup.community mount and supplies
// the same identity to the development host. Matching the manifest keeps
// stylesheet selectors attached to the plugin's user and administrator pages.
export default defineConfig(
  mergeConfig(
    tokenrouterPlugin({ pluginId: 'moonup.community', outDir: '../dist/frontend' }),
    { plugins: [vue()] },
  ),
)
