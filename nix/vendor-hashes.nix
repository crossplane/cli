# Go vendor hashes for buildGoModule.
#
# This file is the buildGoModule equivalent of the old gomod2nix.toml file: it
# pins the hash of the module's vendored dependencies so builds stay
# reproducible inside the Nix sandbox.
#
# Regenerate after changing Go dependencies with:
#
#   nix run .#tidy
#
# (tidy runs `go mod tidy` and then rewrites the hash below. Don't edit it by
# hand.)
{
  # Root module: github.com/crossplane/cli/v2
  root = "sha256-2GMqkUzvKXIrzGEtcceDDK1Fil7fziiW8Wi1FGCRS4c=";
}
