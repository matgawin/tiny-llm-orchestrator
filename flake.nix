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
  in {
    devShells.${system}.default = pkgs.mkShell {
      packages = with pkgs;
        [
          jujutsu
          zellij
          jq
        ]
        ++ [beads.packages.${system}.default];

      shellHook = ''
        export BEADS_DIR="$PWD/../.beads"
        export PATH="$HOME/.bun/bin:$PATH"
      '';
    };
  };
}
