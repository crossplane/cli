import js from '@eslint/js';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,

  {
    languageOptions: {
      parserOptions: {
        // tsconfig.json excludes tests so they stay out of dist/, but the type
        // aware rules still need them in a program, so lint against a config
        // that includes everything.
        project: './tsconfig.eslint.json',
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      // RunFunction is async because FunctionHandler requires a Promise, not
      // because it necessarily awaits anything.
      '@typescript-eslint/require-await': 'off',
    },
  },

  { ignores: ['dist/**', 'node_modules/**', '*.config.js'] }
);
