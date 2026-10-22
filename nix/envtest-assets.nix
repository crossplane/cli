# envtest control-plane binaries (kube-apiserver, etcd, kubectl) for the diff
# integration tests, from the controller-tools envtest release. Pinned to the
# Kubernetes version crossplane-diff's Earthfile targets (KUBE_VERSION).
# Tests find them through KUBEBUILDER_ASSETS; on non-Linux systems there is
# nothing to provide and null is returned so callers can skip wiring.
{ pkgs }:
let
  version = "1.30.3";
  hashes = {
    amd64 = "sha256-DDwRyCoyUfBEaw5WPmYRmfrVEhDpmscC1MIroilgo9I=";
    arm64 = "sha256-Z26hrMSpoO04w9dr7CH2/b3v6U6QyunAj6vqD+opk5M=";
  };
  arch =
    {
      x86_64 = "amd64";
      aarch64 = "arm64";
    }
    .${pkgs.stdenv.hostPlatform.uname.processor} or null;
in
if !pkgs.stdenv.hostPlatform.isLinux || arch == null then
  null
else
  pkgs.runCommand "envtest-assets-${version}"
    {
      nativeBuildInputs = [ pkgs.gnutar ];
      meta = {
        description = "envtest control-plane binaries (Kubernetes v${version})";
        platforms = pkgs.lib.platforms.linux;
      };
    }
    ''
      mkdir -p $out
      tar -xzf ${
        pkgs.fetchurl {
          url = "https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v${version}/envtest-v${version}-linux-${arch}.tar.gz";
          hash = hashes.${arch};
        }
      } --strip-components=2 -C $out controller-tools/envtest
    ''
