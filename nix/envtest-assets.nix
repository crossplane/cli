# envtest control-plane binaries (kube-apiserver, etcd, kubectl) for the
# integration tests, from the controller-tools envtest release. Tests find them
# through KUBEBUILDER_ASSETS. null is returned only for systems without an
# envtest build, so callers can skip wiring them.
{ pkgs }:
let
  version = "1.35.0";
  platforms = {
    x86_64-linux = {
      os = "linux";
      arch = "amd64";
      hash = "sha256-VxZxne8Uo/7D7SheXoxCgOYmiFQDm1BzqW6MCtr7HAI=";
    };
    aarch64-linux = {
      os = "linux";
      arch = "arm64";
      hash = "sha256-UFf7Re7PJGkp2naLIdMkNLjJbiKnjvbN/pEvGmeq5Fo=";
    };
    x86_64-darwin = {
      os = "darwin";
      arch = "amd64";
      hash = "sha256-5zP3Lv/IqAdvLI64kt5K60u1TqAggoCM4+UfgPL/heI=";
    };
    aarch64-darwin = {
      os = "darwin";
      arch = "arm64";
      hash = "sha256-PGsevXRbgtrtR2BftWX3xnDIozRLV6N3qRTQE7a57vA=";
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
