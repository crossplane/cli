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
  version = "1.37.0";
  platforms = {
    x86_64-linux = {
      os = "linux";
      arch = "amd64";
      hash = "sha512-HRxFNjO3LBYaXVqIbN56yFC+GirHlqnh1P+srMZIaClb3S1XqmbNDBWPXOUQ9d/j+8YawhvT3PuHW9cGWKpmOg==";
    };
    aarch64-linux = {
      os = "linux";
      arch = "arm64";
      hash = "sha512-rmpnBQKYggCwExyUN1jP09OljPTmJH738OamRn8c2aMzyALoYQHwRGsbkqDjJzhyJOHkT+GkaT2gkpxuUpz+mg==";
    };
    x86_64-darwin = {
      os = "darwin";
      arch = "amd64";
      hash = "sha512-GaKlN2qKpXp7JexRmINNspvyug1v9XLX9Fumg7E/sUw+vsgGnpLbZsOfHHqc/xvCvoeeKqzcn6i4p+hhlZvfew==";
    };
    aarch64-darwin = {
      os = "darwin";
      arch = "arm64";
      hash = "sha512-+zjPrN1xtel6TUzOrIYa9fVQac94Pw5Jzxgb/DLrPlV8IJGlNNxdOPG5LFuhQrwZefIVpThbNjDqamYb5voWGw==";
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
