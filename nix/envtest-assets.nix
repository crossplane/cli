# envtest control-plane binaries (kube-apiserver, etcd, kubectl) for the
# integration tests, from the controller-tools envtest release. Tests find them
# through KUBEBUILDER_ASSETS. null is returned only for systems without an
# envtest build, so callers can skip wiring them.
#
# Don't edit version or the hashes by hand: `nix run .#tidy` sets version to the
# newest envtest release for go.mod's k8s.io/api minor, and the sha512 hashes
# from controller-tools' envtest-releases.yaml index.
{ pkgs }:
let
  version = "1.35.0";
  platforms = {
    x86_64-linux = {
      os = "linux";
      arch = "amd64";
      hash = "sha512-EwNpwW8HbnJNCJGJr67elgMW9fXeps9XvnpPxvCcdzQokxklCXkOQFbhFuIy3/gy7YY/W9Vdy1XTjzq4NIKKEQ==";
    };
    aarch64-linux = {
      os = "linux";
      arch = "arm64";
      hash = "sha512-5T4riDmPW5UD4/B02CotywkMcIs0lAhIYHzmWBOKXUolli4EKraDzMAmqKbJDAvn9ljkLd4Ihzadc8O2ji/IbA==";
    };
    x86_64-darwin = {
      os = "darwin";
      arch = "amd64";
      hash = "sha512-/MxYO6bTIsiKjFb3h2CQp61jRgBGpLyuQUCTsjzminXxcsp0hLfHR1twdlfspRCKitP9heG09wpsmcovItvWsg==";
    };
    aarch64-darwin = {
      os = "darwin";
      arch = "arm64";
      hash = "sha512-u10Ls5dZVjMbCqDAOZVbTE3GxcKI5a82k2TH0vvqwRoCUifauxJ1aQgLJZ3Sm2l8x3+ner0nriZyHdsj6O4GEw==";
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
