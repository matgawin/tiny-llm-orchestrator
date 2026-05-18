{
  pkgs,
  system,
  beads,
  orcPackage,
}: let
  codexWithBeads = pkgs.writeShellScriptBin "codex" ''
    export BEADS_DIR="''${BEADS_DIR:-$PWD/../.beads}"

    real_codex="''${CODEX_BIN:-$HOME/.bun/bin/codex}"
    if [ ! -x "$real_codex" ]; then
      echo "codex: set CODEX_BIN to the underlying codex executable" >&2
      exit 127
    fi

    exec "$real_codex" --add-dir "$BEADS_DIR" "$@"
  '';

  goplsCheck = pkgs.writeShellScriptBin "orc-gopls-check" ''
    set -euo pipefail

    mode="''${1:-changed}"

    case "$mode" in
      changed)
        mapfile -t files < <(
          jj diff --name-only |
            while IFS= read -r file; do
              case "$file" in
                *.go)
                  if [ -f "$file" ]; then
                    printf '%s\n' "$file"
                  fi
                  ;;
              esac
            done
        )
        ;;
      all)
        mapfile -t files < <(
          find . -type f -name '*.go' \
            -not -path './vendor/*' \
            -not -path './.direnv/*' \
            -not -path './.orc/runs/*' \
            -print
        )
        ;;
      *)
        echo "usage: orc-gopls-check [changed|all]" >&2
        exit 2
        ;;
    esac

    if [ "''${#files[@]}" -eq 0 ]; then
      echo "gopls: no Go files to check"
      exit 0
    fi

    gopls check "''${files[@]}"
  '';

  packages = with pkgs;
    [
      codexWithBeads
      goplsCheck
      bubblewrap
      go
      gofumpt
      golangci-lint
      go-tools
      gotools
      go-task
      gopls
      jq
      jujutsu
    ]
    ++ [
      beads.packages.${system}.default
      orcPackage
    ];

  shellHook = ''
    export BEADS_DIR="$PWD/../.beads"
    export PATH="$PATH:$HOME/.bun/bin"
  '';
in {
  default = pkgs.mkShell {
    inherit packages shellHook;
  };
  sandboxCodex = pkgs.mkShell {
    inherit packages;
    shellHook =
      shellHook
      + ''
        if [ "''${ORC_SANDBOX}" == "1" ]; then
          exec codex --dangerously-bypass-approvals-and-sandbox --model gpt-5.5
        fi
      '';
  };
}
