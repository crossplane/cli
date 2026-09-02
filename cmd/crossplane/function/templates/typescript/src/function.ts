import {
  type ComposeFunction,
  fatal,
  getObservedCompositeResource,
  normal,
} from '@crossplane-org/function-sdk-typescript';

/**
 * compose is a Crossplane composition function.
 *
 * serve() hands us a response already built from the request, so there is no
 * to(req) here, and rsp.desired is guaranteed to be present.
 */
export const compose: ComposeFunction = async (req, rsp, logger) => {
  try {
    // Get the observed composite resource (XR).
    const observedComposite = getObservedCompositeResource(req);
    logger?.debug({ observedComposite }, 'Observed composite resource');

    // TODO: Add your function logic here.
    //
    // Write composed resources straight onto the response. ComposeResponse
    // narrows desired to non-optional, so there is no need for rsp.desired!.
    // fromModel converts a kubernetes-models object — such as one of the
    // classes generated from your XRDs — into a Resource:
    //
    //   import { fromModel } from '@crossplane-org/function-sdk-typescript';
    //   import { VPC } from 'crossplane-models/ec2.aws.m.upbound.io/v1beta1';
    //
    //   const vpc = new VPC({ spec: { forProvider: { region: 'us-west-2' } } });
    //   vpc.validate();
    //   rsp.desired.resources['my-resource'] = fromModel(vpc);

    normal(rsp, 'Function completed successfully');
    return rsp;
  } catch (error) {
    logger?.error(
      { error: error instanceof Error ? error.message : String(error) },
      'Function invocation failed'
    );

    fatal(rsp, error instanceof Error ? error.message : String(error));
    return rsp;
  }
};
