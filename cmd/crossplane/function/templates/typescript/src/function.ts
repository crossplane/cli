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
    // Use desiredComposed to add, modify, or remove composed resources.
    // Example:
    //   desiredComposed['my-resource'] = { resource: { ... } };

    // Update the response with the desired composed resources.
    rsp = setDesiredComposedResources(rsp, desiredComposed);

    normal(rsp, 'Function completed successfully');
    return rsp;
  }
}
