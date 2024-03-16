{
  description = "Nix flake for github-artifact-proxy";
  inputs.nixpkgs.url = "nixpkgs/nixos-unstable";
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        overlay = final: prev: {
          github-artifact-proxy = with final; buildGoModule rec {
            name = "github-artifact-proxy";
            src = ./.;

            vendorHash = "sha256-4FvWw/WxFgOeCnA4th7rp09J+xs2xNh3d0MuplJOBW0=";

            subPackages = [ "cmd/github-artifact-proxy" ];
          };
        };
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            overlay
          ];
        };
      in rec {
        packages = flake-utils.lib.flattenTree {
          github-artifact-proxy = pkgs.github-artifact-proxy;
        };
        defaultPackage = packages.github-artifact-proxy;
        devShell = with pkgs; mkShell {
          hardeningDisable = [ "fortify" ];
          buildInputs = [
            go
          ];
        };
      }
    );
}
