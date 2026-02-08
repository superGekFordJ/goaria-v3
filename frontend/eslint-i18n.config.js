import pluginVue from 'eslint-plugin-vue'
import pluginVueI18n from '@intlify/eslint-plugin-vue-i18n'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  {
    ignores: ['**/dist/**', '**/wailsjs/**', '**/node_modules/**'],
  },
  ...pluginVue.configs['flat/recommended'],
  ...pluginVueI18n.configs['flat/recommended'],
  {
    files: ['**/*.vue', '**/*.ts', '**/*.js'], // Apply to these files
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
      },
    },
    settings: {
      'vue-i18n': {
        localeDir: './src/i18n/locales/*.{json,json5,yaml,yml}', // Extension is required
        messageSyntaxVersion: '^9.0.0',
      },
    },
    rules: {
      // Disable standard rules to reduce noise, we only want i18n
      'vue/multi-word-component-names': 'off',
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unused-vars': 'off',
      
      // Enable i18n hardcoded text detection
      '@intlify/vue-i18n/no-raw-text': [
        'warn',
        {
          ignorePattern: '^[-#:()&]+$', // Ignore symbols
          ignoreText: [],
          ignoreNodes: ['v-icon', 'v-icon'], 
        },
      ],
      '@intlify/vue-i18n/no-missing-keys': 'off', // We don't have keys yet
      '@intlify/vue-i18n/no-unused-keys': 'off',
       // We only care about raw text for now
       '@intlify/vue-i18n/no-v-html': 'off',
    },
  }
)
