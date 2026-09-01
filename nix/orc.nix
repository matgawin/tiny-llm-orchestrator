{
  pkgs,
  src,
  version,
}: let
  orc = pkgs.buildGoModule {
    pname = "orc";
    inherit version;
    inherit src;
    vendorHash = "sha256-3f3tHrgQRyEyVGKxU9GAiClBgm5lMJY/QC6tjYPnViA=";
    subPackages = ["cmd/orc"];

    ldflags = [
      "-s"
      "-w"
      "-X tiny-llm-orchestrator/orc/internal/cli.version=${version}"
    ];
  };
in {
  package = orc;
  app = {
    type = "app";
    program = "${orc}/bin/orc";
  };
}
