# Crossplane Composition Function

This is a [Crossplane](https://crossplane.io) composition function written in TypeScript.

## Development

Install dependencies:

```shell
npm install
```

Build the function:

```shell
npm run build
```

Run locally (for testing):

```shell
npm run local
```

## Testing

Unit tests run with [Vitest](https://vitest.dev), alongside the code in `src/`:

```shell
npm test
```

End to end, render the composition against an example XR:

```shell
crossplane composition render xr.yaml composition.yaml
```

## Linting

```shell
npm run lint
```

## Why there are two TypeScript compilers

TypeScript 7's native compiler no longer exposes the JavaScript compiler API that
`typescript-eslint` is built on, so the two cannot share one install. `package.json`
therefore aliases both:

```json
"@typescript/native": "npm:typescript@^7.0.0",
"typescript": "npm:@typescript/typescript6@^6.0.2"
```

TypeScript 7 provides the `tsc` binary that `npm run build` uses. TypeScript 6 keeps the
`typescript` package *name*, which is what `typescript-eslint` imports to get the compiler
API — and exposes its own binary as `tsc6`, so the two never collide. `npm run
typecheck:legacy` runs the TypeScript 6 check, which is worth doing once when upgrading
because TypeScript 7 drops deprecated compiler options.

Remove the alias and go back to a plain `typescript` devDependency once TypeScript 7.1 ships
its programmatic API and `typescript-eslint` adopts it.

## Learn More

- [Composition Functions documentation](https://docs.crossplane.io/latest/concepts/composition-functions/)
- [TypeScript Function SDK](https://github.com/crossplane/function-sdk-typescript)
