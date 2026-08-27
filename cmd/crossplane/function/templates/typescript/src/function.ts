import {
  type RunFunctionRequest,
  type RunFunctionResponse,
  type FunctionHandler,
  type Logger,
  to,
  normal,
  getObservedCompositeResource,
  getDesiredComposedResources,
  setDesiredComposedResources,
} from '@crossplane-org/function-sdk-typescript';

/**
 * Function is a Crossplane composition function.
 */
export class Function implements FunctionHandler {
  async RunFunction(req: RunFunctionRequest, logger?: Logger): Promise<RunFunctionResponse> {
    let rsp = to(req);

    // Get the observed composite resource (XR).
    const observedComposite = getObservedCompositeResource(req);
    logger?.debug({ observedComposite }, 'Observed composite resource');

    // Get the desired composed resources from previous functions in the pipeline.
    const desiredComposed = getDesiredComposedResources(req);
    logger?.debug({ desiredComposed }, 'Desired composed resources');

    // TODO: Add your function logic here.
    // Use desiredComposed to add, modify, or remove composed resources. Each
    // entry is a Resource, so a kubernetes-models object such as one of the
    // generated crossplane-models classes has to be converted first:
    //
    //   import { Resource } from '@crossplane-org/function-sdk-typescript';
    //   import { VPC } from 'crossplane-models/ec2.aws.m.upbound.io/v1beta1';
    //
    //   const vpc = new VPC({ spec: { forProvider: { region: 'us-west-2' } } });
    //   vpc.validate();
    //   desiredComposed['my-resource'] = Resource.fromJSON({ resource: vpc.toJSON() });

    // Update the response with the desired composed resources.
    rsp = setDesiredComposedResources(rsp, desiredComposed);

    normal(rsp, 'Function completed successfully');
    return rsp;
  }
}
