{
  description = "Tiny LLM Orchestrator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    beads.url = "github:gastownhall/beads";
  };

  outputs = {
    self,
    nixpkgs,
    beads,
  }: let
    system = "x86_64-linux";
    pkgs = import nixpkgs {inherit system;};
    codexWithBeads = pkgs.writeShellScriptBin "codex" ''
      export BEADS_DIR="''${BEADS_DIR:-$PWD/../.beads}"

      real_codex="''${CODEX_BIN:-$HOME/.bun/bin/codex}"
      if [ ! -x "$real_codex" ]; then
        echo "codex: set CODEX_BIN to the underlying codex executable" >&2
        exit 127
      fi

      exec "$real_codex" --add-dir "$BEADS_DIR" "$@"
    '';
  in {
    devShells.${system}.default = pkgs.mkShell {
      packages = with pkgs;
        [
          codexWithBeads
          go
          jq
          jujutsu
          zellij
        ]
        ++ [beads.packages.${system}.default];

      shellHook = ''
        export BEADS_DIR="$PWD/../.beads"
        export PATH="$PATH:$HOME/.bun/bin"
      '';
    };
  };
}
