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
  root = "sha256-gDcfAAG7Mx1LykU8Ov1cY/4GnKOE8jUd0+0wVssC/pA=";
}
