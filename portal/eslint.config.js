import { createConfigForNuxt } from '@nuxt/eslint-config'

export default createConfigForNuxt({
  // Global ESLint configuration
  ignores: [
    '.nuxt',
    '.output',
    'node_modules',
    'public',
  ],
  languageOptions: {
    ecmaVersion: 2022,
    sourceType: 'module',
  },
  rules: {
    // Base rules
    'no-console': process.env.NODE_ENV === 'production' ? 'error' : 'warn',
    'no-debugger': process.env.NODE_ENV === 'production' ? 'error' : 'warn',
    
    // Vue specific rules
    'vue/multi-word-component-names': 'off',
    'vue/no-v-html': 'warn',
    'vue/require-default-prop': 'error',
    'vue/component-name-in-template-casing': ['error', 'PascalCase'],
    
    // TypeScript specific rules
    '@typescript-eslint/no-unused-vars': ['error', { 
      argsIgnorePattern: '^_', 
      varsIgnorePattern: '^_' 
    }],
    '@typescript-eslint/explicit-function-return-type': 'off',
    '@typescript-eslint/no-explicit-any': 'warn',
  }
})