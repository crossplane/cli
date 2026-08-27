import { describe, it, expect } from 'vitest';
import { RunFunctionRequest } from '@crossplane-org/function-sdk-typescript';
import { Function } from './function.js';

describe('Function', () => {
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

    const rsp = await new Function().RunFunction(req);

    expect(rsp.desired).toBeDefined();
    expect(rsp.results.map((r) => r.message)).toContain('Function completed successfully');
  });
});
