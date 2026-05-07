{
  pkgs,
  src,
}: let
  orc = pkgs.buildGoModule {
    pname = "orc";
    version = "1.0.0";
    inherit src;
    vendorHash = "sha256-/jAJ1jeLiRsFxfflj8sqD85rluauepXODoEeGK4l8FQ=";
    subPackages = ["cmd/orc"];

    ldflags = [
      "-s"
      "-w"
      "-X tiny-llm-orchestrator/orc/internal/cli.version=1.0.0"
    ];
  };
in {
  package = orc;
  app = {
    type = "app";
    program = "${orc}/bin/orc";
  };
}
