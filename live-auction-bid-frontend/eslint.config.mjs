import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';
import { defineConfig, globalIgnores } from 'eslint/config';

export default defineConfig([
  globalIgnores(['dist', 'coverage', 'playwright-report', 'test-results']),
  {
    files: ['src/**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      'no-restricted-syntax': [
        'error',
        {
          selector: "AssignmentExpression[left.object.name='location'][left.property.name='href']",
          message: '站内导航请使用 navigateApp 或 AppLink，避免整页重载。',
        },
        {
          selector: "AssignmentExpression[left.object.object.name='window'][left.object.property.name='location'][left.property.name='href']",
          message: '站内导航请使用 navigateApp 或 AppLink，避免整页重载。',
        },
      ],
    },
  },
  {
    files: ['*.config.{js,mjs,ts}', 'scripts/**/*.mjs', 'tests/**/*.{ts,tsx}'],
    extends: [js.configs.recommended, tseslint.configs.recommended],
    languageOptions: {
      globals: globals.node,
    },
  },
]);
