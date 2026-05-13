oxr = option("params").oxr
dxr = {
    **oxr
    status.dummy = "cool-status"
}
readyCondition = {
        conditionStatus: "True"
        conditionType: "Ready"
        time: "5s"
} if (oxr.spec.parameters.networkRef.name) else {}
statusCondition = {
        conditionStatus: "False"
        conditionType: "Ready"
        conditionReason: "Network reference is missing"
        time: "0s"
} if not (oxr.spec.parameters.networkRef.name) else {}
items = [{
    apiVersion: "nop.crossplane.io/v1alpha1"
    kind: "NopResource"
    metadata.name = oxr.metadata.name
    spec.forProvider = {
      conditionAfter = [readyCondition] if readyCondition else [statusCondition]
      fields = {
        initialNodeCount: oxr.spec.parameters.initialNodeCount
        networkRef: oxr.spec.parameters.networkRef
        subnetworkRef: oxr.spec.parameters.subnetworkRef
        project: oxr.spec.parameters.project
        location: oxr.spec.parameters.location
      }
    }
}]

