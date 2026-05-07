{
  pkgs,
  system,
  beads,
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

  packages = with pkgs;
    [
      codexWithBeads
      bubblewrap
      go
      gofumpt
      golangci-lint
      go-tools
      gotools
      go-task
      jq
      jujutsu
    ]
    ++ [beads.packages.${system}.default];

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
          exec codex --dangerously-bypass-approvals-and-sandbox
        fi
      '';
  };
}
