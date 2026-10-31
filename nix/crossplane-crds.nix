# Crossplane's own CRDs (Compositions, XRDs, Functions, ...) for the
# integration tests and the CRD helpers' unit tests. Pinned to the
# github.com/crossplane/crossplane/apis/v2 version in go.mod; `nix run .#tidy`
# (which renovate runs after go.mod bumps) keeps rev and hash in sync. It
# fails, leaving them for you to set, when that version is a pseudo-version.
# Tests find them through CROSSPLANE_CRDS_DIR.
{ pkgs }:
pkgs.fetchFromGitHub {
  owner = "crossplane";
  repo = "crossplane";
  rev = "v2.4.1";
  sparseCheckout = [ "cluster/crds" ];
  hash = "sha256-+jJSqzsVmhw4eU93tFJi0Hcn/iVXc5EZ4Ktp/TYO+s0=";
}
+ "/cluster/crds"
