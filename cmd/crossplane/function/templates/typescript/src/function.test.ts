import { describe, it, expect } from 'vitest';
import { fromCompose, RunFunctionRequest } from '@crossplane-org/function-sdk-typescript';
import { compose } from './function.js';

describe('compose', () => {
  const func = fromCompose(compose);

  it('composes a response from an observed composite resource', async () => {
    const req = RunFunctionRequest.fromJSON({
      observed: {
        composite: {
          resource: {
            apiVersion: 'example.crossplane.io/v1alpha1',
            kind: 'Example',
            metadata: { name: 'example' },
            spec: {},
          },
        },
      },
    });

    const rsp = await func.RunFunction(req);

    expect(rsp.desired).toBeDefined();
    expect(rsp.results.map((r) => r.message)).toContain('Function completed successfully');
  });
});
