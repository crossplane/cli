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

Test your function using `crossplane resource render`:

```shell
crossplane resource render xr.yaml composition.yaml functions.yaml
```

## Learn More

- [Composition Functions documentation](https://docs.crossplane.io/latest/concepts/composition-functions/)
- [TypeScript Function SDK](https://github.com/crossplane/function-sdk-typescript)
