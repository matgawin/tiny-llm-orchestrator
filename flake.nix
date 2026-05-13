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
    rawOrcVersion = pkgs.lib.fileContents ./VERSION;
    orcVersion =
      if builtins.match "[0-9]+\\.[0-9]+\\.[0-9]+" rawOrcVersion != null
      then rawOrcVersion
      else throw "VERSION must match X.Y.Z numeric semver without a leading v";
    orc = import ./nix/orc.nix {
      inherit pkgs;
      src = ./.;
      version = orcVersion;
    };
    shells = import ./nix/shell.nix {
      inherit pkgs system beads;
      orcPackage = orc.package;
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
