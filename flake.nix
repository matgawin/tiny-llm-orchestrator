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
    orc = import ./nix/orc.nix {
      inherit pkgs;
      src = ./.;
    };
    shells = import ./nix/shell.nix {
      inherit pkgs system beads;
    };
  in {
    packages.${system} = {
      default = orc.package;
      orc = orc.package;
    };

    apps.${system} = {
      default = self.apps.${system}.orc;
      orc = orc.app;
    };

    devShells.${system} = {
      default = shells.default;
      sandboxCodex = shells.sandboxCodex;
    };
  };
}
