# envtest control-plane binaries (kube-apiserver, etcd, kubectl) for the
# integration tests, from the controller-tools envtest release. Tests find them
# through KUBEBUILDER_ASSETS. null is returned only for systems without an
# envtest build, so callers can skip wiring them.
{ pkgs }:
let
  version = "1.30.3";
  platforms = {
    x86_64-linux = {
      os = "linux";
      arch = "amd64";
      hash = "sha256-DDwRyCoyUfBEaw5WPmYRmfrVEhDpmscC1MIroilgo9I=";
    };
    aarch64-linux = {
      os = "linux";
      arch = "arm64";
      hash = "sha256-Z26hrMSpoO04w9dr7CH2/b3v6U6QyunAj6vqD+opk5M=";
    };
    x86_64-darwin = {
      os = "darwin";
      arch = "amd64";
      hash = "sha256-hixH51R+1/LyMOkABsvdtSa/Os6mNhruixg4NzlSdJ4=";
    };
    aarch64-darwin = {
      os = "darwin";
      arch = "arm64";
      hash = "sha256-sRcsK819TPzkb2guCqw/neEei7oSoWPe2maYEJY1myQ=";
    };
  };
  target = platforms.${pkgs.stdenv.hostPlatform.system} or null;
in
if target == null then
  null
else
  pkgs.runCommand "envtest-assets-${version}"
    {
      nativeBuildInputs = [ pkgs.gnutar ];
      meta = {
        description = "envtest control-plane binaries (Kubernetes v${version})";
        platforms = builtins.attrNames platforms;
      };
    }
    ''
      mkdir -p $out
      tar -xzf ${
        pkgs.fetchurl {
          url = "https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v${version}/envtest-v${version}-${target.os}-${target.arch}.tar.gz";
          inherit (target) hash;
        }
      } --strip-components=2 -C $out controller-tools/envtest
    ''
