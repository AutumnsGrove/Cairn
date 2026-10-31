import svelte from 'eslint-plugin-svelte';
import ts from 'typescript-eslint';
import globals from 'globals';

export default [
  {
    ignores: [
      'build/',
      '.svelte-kit/',
      'node_modules/',
      'test-results/',
      'playwright-report/',
      // Generated from api/ by make gen; linting it would fight the generator.
      'src/lib/contract/',
    ],
  },
  ...ts.configs.recommended,
  ...svelte.configs.recommended,
  {
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
  },
  {
    files: ['**/*.svelte'],
    languageOptions: {
      parserOptions: { parser: ts.parser },
    },
  },
];
