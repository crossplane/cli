{{- if .Imports }}
{{- range .Imports }}
import {{.ImportPath}} as {{.Alias}}
{{- end }}
{{ "\n" -}}
{{- end -}}
oxr = option("params").oxr # observed composite resource
_ocds = option("params").ocds # observed composed resources
_dxr = option("params").dxr # desired composite resource
dcds = option("params").dcds # desired composed resources

_metadata = lambda name: str -> any {
    { annotations = { "krm.kcl.dev/composition-resource-name" = name }}
}

# Example to retrieve variables from "xr"; update as needed
# _region = "us-east-1"
# if oxr.spec?.parameters?.region:
#     _region = oxr.spec.parameters.region

_items = [
# Example S3 Bucket managed resource configuration; update as needed
# s3v1beta2.Bucket{
#     metadata: _metadata("my-bucket")
#     spec: {
#         forProvider: {
#             region: _region
#         }
#     }
# }
]
items = _items
