# Build functions for Crossplane CLI.
#
# All functions are builders that take an attrset of arguments.
# This makes dependencies explicit and keeps flake.nix as a clean manifest.
#
# Key primitives used here:
#   pkgs.buildGoApplication - gomod2nix's Go builder (https://github.com/nix-community/gomod2nix)
#   pkgs.runCommand         - Run a shell script, capture output directory as $out
{ pkgs, self }:
let
  # Build a Go binary for a specific platform.
  goBinary =
    {
      version,
      pname,
      subPackage,
      platform,
    }:
    let
      ext = if platform.os == "windows" then ".exe" else "";
    in
    pkgs.buildGoApplication {
      pname = "${pname}-${platform.os}-${platform.arch}";
      inherit version;
      src = self;
      pwd = self;
      modules = "${self}/gomod2nix.toml";
      subPackages = [ subPackage ];

      # Cross-compile by merging GOOS/GOARCH into Go's attrset (// merges attrsets).
      go = pkgs.unstable.go_1_26 // {
        GOOS = platform.os;
        GOARCH = platform.arch;
      };

      CGO_ENABLED = "0";
      doCheck = false;

      preBuild = ''
        ldflags="-s -w -X=github.com/crossplane/crossplane-runtime/v2/pkg/version.version=${version}"
      '';

      postInstall = ''
        if [ -d $out/bin/${platform.os}_${platform.arch} ]; then
          mv $out/bin/${platform.os}_${platform.arch}/* $out/bin/
          rmdir $out/bin/${platform.os}_${platform.arch}
        fi
        cd $out/bin
        sha256sum ${pname}${ext} | head -c 64 > ${pname}${ext}.sha256
      '';

      meta = {
        description = "Crossplane - The cloud native control plane framework";
        homepage = "https://crossplane.io";
        license = pkgs.lib.licenses.asl20;
        mainProgram = pname;
      };
    };

  # Build tarball with checksums.
  bundle =
    {
      version,
      drv,
      platform,
    }:
    let
      ext = if platform.os == "windows" then ".exe" else "";
    in
    pkgs.runCommand "crossplane-bundle-${platform.os}-${platform.arch}-${version}"
      {
        nativeBuildInputs = [
          pkgs.gnutar
          pkgs.gzip
        ];
      }
      ''
        mkdir -p $out
        cp ${drv}/bin/crossplane${ext} .
        cp ${drv}/bin/crossplane${ext}.sha256 .
        chmod 755 crossplane${ext}
        chmod 644 crossplane${ext}.sha256
        tar -czvf $out/crossplane-cli.tar.gz crossplane${ext} crossplane${ext}.sha256
        cd $out
        sha256sum crossplane-cli.tar.gz | head -c 64 > crossplane-cli.tar.gz.sha256
      '';

in
{
  # Full release package with all artifacts.
  release =
    {
      version,
      goPlatforms,
    }:
    let

      bins = builtins.listToAttrs (
        map (p: {
          name = "${p.os}-${p.arch}";
          value = goBinary {
            inherit version;
            pname = "crossplane";
            subPackage = "cmd/crossplane";
            platform = p;
          };
        }) goPlatforms
      );

      bundles = builtins.listToAttrs (
        map (p: {
          name = "${p.os}-${p.arch}";
          value = bundle {
            inherit version;
            drv = bins."${p.os}-${p.arch}";
            platform = p;
          };
        }) goPlatforms
      );
    in
    pkgs.runCommand "crossplane-release-${version}" { } ''
      mkdir -p $out/bin $out/bundle

      ${pkgs.lib.concatMapStrings (p: ''
        mkdir -p $out/bin/${p.os}_${p.arch}
        cp ${bins."${p.os}-${p.arch}"}/bin/* $out/bin/${p.os}_${p.arch}/
        ${
          let
            ext = if p.os == "windows" then ".exe" else "";
          in
          ''
            chmod 755 $out/bin/${p.os}_${p.arch}/crossplane${ext}
            chmod 644 $out/bin/${p.os}_${p.arch}/crossplane${ext}.sha256
          ''
        }
      '') goPlatforms}

      ${pkgs.lib.concatMapStrings (p: ''
        mkdir -p $out/bundle/${p.os}_${p.arch}
        cp ${bundles."${p.os}-${p.arch}"}/* $out/bundle/${p.os}_${p.arch}/
      '') goPlatforms}
    '';
}
