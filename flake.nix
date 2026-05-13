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
    rawOrcReleaseVersion = builtins.getEnv "ORC_RELEASE_VERSION";
    orcVersion =
      if rawOrcReleaseVersion == ""
      then "dev"
      else if builtins.match "[0-9]+\\.[0-9]+\\.[0-9]+" rawOrcReleaseVersion != null
      then rawOrcReleaseVersion
      else throw "ORC_RELEASE_VERSION must match X.Y.Z numeric semver without a leading v";
    orc = import ./nix/orc.nix {
      inherit pkgs;
      src = ./.;
      version = orcVersion;
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
