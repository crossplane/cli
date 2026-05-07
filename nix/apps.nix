# Interactive development commands for Crossplane CLI.
#
# Apps run outside the Nix sandbox with full filesystem and network access.
# They're designed for local development where Go modules are already available.
#
# All apps are builder functions that take an attrset of arguments and return a
# complete app definition ({ type, meta.description, program }). Most use
# writeShellApplication to create the program. The text block is preprocessed:
#
#   ${somePkg}/bin/foo   -> /nix/store/.../bin/foo  (Nix store path)
#   ''${SOME_VAR}        -> ${SOME_VAR}             (shell variable, escaped)
#
# Each app declares its tool dependencies via runtimeInputs, with inheritPath
# set to false. This ensures apps only use explicitly declared tools.
{ pkgs }:
{
  # Run Go unit tests.
  test = _: {
    type = "app";
    meta.description = "Run unit tests";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-test";
        runtimeInputs = [ pkgs.unstable.go_1_26 ];
        inheritPath = false;
        text = ''
          export CGO_ENABLED=0
          go test -covermode=count ./... "$@"
        '';
      }
    );
  };

  # Run linters with auto-fix. Formats code first, then reports remaining issues.
  lint = _: {
    type = "app";
    meta.description = "Format code and run linters";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-lint";
        runtimeInputs = [
          pkgs.findutils
          pkgs.unstable.go_1_26
          pkgs.unstable.golangci-lint
          pkgs.statix
          pkgs.deadnix
          pkgs.nixfmt-rfc-style
          pkgs.shellcheck
          pkgs.gnupatch
          pkgs.shfmt
        ];
        inheritPath = false;
        text = ''
          export CGO_ENABLED=0
          export GOLANGCI_LINT_CACHE="''${XDG_CACHE_HOME:-$HOME/.cache}/golangci-lint"

          echo "Formatting and linting Nix..."
          statix fix .
          deadnix --edit flake.nix nix/*.nix
          nixfmt flake.nix nix/*.nix

          echo "Formatting and linting shell..."
          find . -name '*.sh' -type f | while read -r script; do
            shellcheck --format=diff "$script" | patch -p1 || true
            shfmt -w "$script"
          done
          find . -name '*.sh' -type f -exec shellcheck {} +

          echo "Formatting and linting Go..."
          golangci-lint run --fix "$@"
        '';
      }
    );
  };

  # Run code generation.
  generate = _: {
    type = "app";
    meta.description = "Run code generation";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-generate";
        runtimeInputs = [
          pkgs.coreutils
          pkgs.gnused
          pkgs.unstable.go_1_26

          # Code generation
          pkgs.buf
          pkgs.goverter
          pkgs.protoc-gen-go
          pkgs.protoc-gen-go-grpc
          pkgs.kubernetes-controller-tools
        ];
        inheritPath = false;
        text = ''
          export CGO_ENABLED=0

          echo "Running go generate..."
          go generate -tags generate .

          echo "Done"
        '';
      }
    );
  };

  # Run go mod tidy and regenerate gomod2nix.toml.
  tidy = _: {
    type = "app";
    meta.description = "Run go mod tidy and regenerate gomod2nix.toml";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-tidy";
        runtimeInputs = [
          pkgs.unstable.go_1_26
          pkgs.gomod2nix
        ];
        inheritPath = false;
        text = ''
          export CGO_ENABLED=0

          echo "Running go mod tidy..."
          go mod tidy
          echo "Regenerating gomod2nix.toml..."
          gomod2nix generate --with-deps

          echo "Done"
        '';
      }
    );
  };

  # Push build artifacts to S3.
  pushArtifacts =
    { release, version }:
    {
      type = "app";
      meta.description = "Push build artifacts to S3";
      program = pkgs.lib.getExe (
        pkgs.writeShellApplication {
          name = "crossplane-cli-push-artifacts";
          runtimeInputs = [ pkgs.awscli2 ];
          inheritPath = false;
          text = ''
            BRANCH="''${1:?Usage: nix run .#push-artifacts -- <branch>}"

            echo "Pushing artifacts to s3://crossplane-cli-releases/build/''${BRANCH}/${version}..."
            aws s3 sync --delete --only-show-errors \
              ${release} \
              "s3://crossplane-cli-releases/build/''${BRANCH}/${version}"
            echo "Done"
          '';
        }
      );
    };

  # Promote build artifacts to a release channel.
  promoteArtifacts = _: {
    type = "app";
    meta.description = "Promote build artifacts to a release channel";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-promote-artifacts";
        runtimeInputs = [
          pkgs.coreutils
          pkgs.awscli2
        ];
        inheritPath = false;
        text = ''
          BRANCH="''${1:?Usage: nix run .#promote-artifacts -- <branch> <version> <channel> [--prerelease]}"
          VERSION="''${2:?Usage: nix run .#promote-artifacts -- <branch> <version> <channel> [--prerelease]}"
          CHANNEL="''${3:?Usage: nix run .#promote-artifacts -- <branch> <version> <channel> [--prerelease]}"
          PRERELEASE="''${4:-}"

          BUILD_PATH="s3://crossplane-cli-releases/build/''${BRANCH}/''${VERSION}"
          CHANNEL_PATH="s3://crossplane-cli-releases/''${CHANNEL}"

          WORKDIR=$(mktemp -d)
          trap 'rm -rf "$WORKDIR"' EXIT

          echo "Promoting artifacts from ''${BUILD_PATH} to ''${CHANNEL}..."

          aws s3 sync --delete --only-show-errors "''${BUILD_PATH}" "''${CHANNEL_PATH}/''${VERSION}"

          if [ "''${PRERELEASE}" != "--prerelease" ]; then
            aws s3 sync --delete --only-show-errors "''${BUILD_PATH}" "''${CHANNEL_PATH}/current"
          fi

          echo "Done"
        '';
      }
    );
  };
}
