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
# set to false. This ensures apps only use explicitly declared tools. The
# exception is integration-test, which keeps the host PATH (see there).
{ pkgs }:
let
  # envtest binaries for the integration tests; null only on systems
  # without an envtest build, where the tests skip.
  envtestAssets = import ./envtest-assets.nix { inherit pkgs; };
in
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
          export CROSSPLANE_CRDS_DIR="''${CROSSPLANE_CRDS_DIR:-${
            import ./crossplane-crds.nix { inherit pkgs; }
          }}"
          go test -covermode=count ./... "$@"
        '';
      }
    );
  };

  # Run the integration tests: CLI commands against an envtest API server.
  # envtest binaries and Crossplane's CRDs come from the Nix store. Unlike the
  # sandboxed `test` check this runs outside the Nix sandbox, and it keeps the
  # host PATH (Nix inputs first) so tests that run functions (#436's simulate
  # tests) can reach the host's Docker and its credential helpers
  # (docker-credential-*). CI runs it in the integration-tests job in
  # .github/workflows/ci.yml.
  integration-test = _: {
    type = "app";
    meta.description = "Run integration tests against envtest";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-integration-test";
        runtimeInputs = [ pkgs.unstable.go_1_26 ];
        inheritPath = true;
        text = ''
          export CGO_ENABLED=0
          export CROSSPLANE_CRDS_DIR="''${CROSSPLANE_CRDS_DIR:-${
            import ./crossplane-crds.nix { inherit pkgs; }
          }}"
          ${pkgs.lib.optionalString (
            envtestAssets != null
          ) ''export KUBEBUILDER_ASSETS="''${KUBEBUILDER_ASSETS:-${envtestAssets}}"''}
          # Integration tests are the test files with `//go:build integration`.
          # Run every package that has one: its test files with the tag, minus
          # those without it. go list applies Go's build-constraint rules.
          # Unit tests in those packages run here too.
          tests='{{range .TestGoFiles}}{{$.ImportPath}} {{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$.ImportPath}} {{.}}{{"\n"}}{{end}}'
          lists=$(mktemp -d)
          go list -f "$tests" ./... | sort >"$lists/untagged"
          go list -tags integration -f "$tests" ./... | sort >"$lists/tagged"
          mapfile -t pkgs < <(comm -13 "$lists/untagged" "$lists/tagged" | cut -d' ' -f1 | sort -u)
          rm -rf "$lists"
          if [ "''${#pkgs[@]}" -eq 0 ]; then
            echo "integration-test: found no packages with //go:build integration test files" >&2
            exit 1
          fi
          # -coverpkg credits the tests' coverage to the packages they exercise;
          # without it the profile only covers the test packages themselves.
          go test -tags integration -covermode=count \
            -coverpkg=./... -coverprofile=coverage-integration.txt "''${pkgs[@]}" "$@"
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
          pkgs.nixfmt
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

  # Run go mod tidy and refresh the Go vendor hashes in nix/vendor-hashes.nix.
  tidy = _: {
    type = "app";
    meta.description = "Run go mod tidy and refresh Go vendor hashes";
    program = pkgs.lib.getExe (
      pkgs.writeShellApplication {
        name = "crossplane-cli-tidy";
        runtimeInputs = [
          pkgs.unstable.go_1_26
          pkgs.nix
          pkgs.git
          pkgs.gnused
          pkgs.coreutils
        ];
        inheritPath = false;
        text = ''
          export CGO_ENABLED=0

          echo "Running go mod tidy..."
          go mod tidy

          # Refresh a hash (a Go vendor hash, or a fetcher's) by resetting it
          # to a placeholder and reading the real hash back from the build
          # error - the same trick nix-update uses for Go vendor hashes.
          refresh() {
            file="$1"  # .nix file holding the hash
            key="$2"   # attribute in $file
            attr="$3"  # flake package whose build checks that hash
            orig="" got=""

            # Remember the current value so we can restore it if anything fails.
            orig=$(sed -n "s|.*$key = \"\(sha256-[^\"]*\)\";.*|\1|p" "$file" | head -n1)

            # Set a placeholder so the build is forced to report the real hash.
            sed -i "s|$key = \"sha256-[^\"]*\";|$key = \"${pkgs.lib.fakeHash}\";|" "$file"

            # --option eval-cache false: we just edited $file, and Nix's flake
            # eval cache can otherwise serve a stale evaluation that ignores
            # the change and reports no/old hash.
            got=$(nix build ".#$attr" --no-link --option eval-cache false 2>&1 \
              | sed -n 's|.*got:[[:space:]]*\(sha256-[A-Za-z0-9+/=]*\).*|\1|p' \
              | head -n1 || true)
            if [ -z "$got" ]; then
              # Build failed for some other reason (e.g. network); restore the
              # previous value rather than leaving the placeholder committed.
              sed -i "s|$key = \"sha256-[^\"]*\";|$key = \"$orig\";|" "$file"
              echo "ERROR: could not determine hash for '$key' in $file (restored previous value)" >&2
              exit 1
            fi

            sed -i "s|$key = \"sha256-[^\"]*\";|$key = \"$got\";|" "$file"
            echo "  $key = $got"
          }

          echo "Refreshing Go vendor hashes..."
          refresh nix/vendor-hashes.nix root crossplane-vendor

          # Keep nix/crossplane-crds.nix at the crossplane/crossplane tag that
          # go.mod's apis/v2 version names (apis/vX and vX tag the same commit).
          echo "Syncing Crossplane CRDs with go.mod..."
          mod=github.com/crossplane/crossplane/apis/v2
          want=$(go list -m -f '{{if .Replace}}replaced{{else}}{{.Version}}{{end}}' "$mod")
          if [ "$want" = replaced ]; then
            echo "ERROR: $mod is replaced in go.mod; set rev and hash in nix/crossplane-crds.nix by hand" >&2
            exit 1
          fi
          # A pseudo-version (...-yyyymmddhhmmss-<12 hex>) names a commit, not
          # a tag, so there is no tag to pin; pin its commit by hand.
          if [[ "$want" =~ [0-9]{14}-[0-9a-f]{12}$ ]]; then
            echo "ERROR: $mod is at pseudo-version $want; set rev to its commit SHA and refresh hash in nix/crossplane-crds.nix by hand" >&2
            exit 1
          fi
          have=$(sed -n 's|.*rev = "\([^"]*\)";.*|\1|p' nix/crossplane-crds.nix | head -n1)
          if [ "$have" != "$want" ]; then
            sed -i "s|rev = \"$have\";|rev = \"$want\";|" nix/crossplane-crds.nix
            if ! (refresh nix/crossplane-crds.nix hash crossplane-crds); then
              sed -i "s|rev = \"$want\";|rev = \"$have\";|" nix/crossplane-crds.nix
              exit 1
            fi
            echo "  rev = $want"
          fi

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

          if [ "''${BRANCH}" == "main" ]; then
            echo "Uploading install.sh..."
            aws s3 cp --only-show-errors install.sh "s3://crossplane-cli-releases/"
          fi

          echo "Done"
        '';
      }
    );
  };
}
