{
  description = "Nix build for headlessdesk";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    gomod2nix.url = "github:nix-community/gomod2nix/v1.7.0";
    gomod2nix.inputs.nixpkgs.follows = "nixpkgs";
    gomod2nix.inputs.flake-utils.follows = "flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      gomod2nix,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        go = pkgs.go_1_25;
        gomod2nixPkgs = gomod2nix.legacyPackages.${system};
        package = builtins.fromJSON (builtins.readFile ./package.json);
        freerdp = pkgs.freerdp.overrideAttrs (old: {
          cmakeFlags = (old.cmakeFlags or [ ]) ++ [
            "-DWITH_OPENH264=OFF"
            "-DWITH_OPENH264_LOADING=OFF"
            "-DWITH_FFMPEG=ON"
          ];
        });
      in
      {
        packages.default = gomod2nixPkgs.buildGoApplication {
          pname = "headlessdesk";
          version = package.version;

          src = ./.;
          pwd = ./.;
          modules = ./gomod2nix.toml;
          modRoot = ".";
          subPackages = [ "cmd/headlessdesk" ];

          inherit go;

          nativeBuildInputs = [
            pkgs.makeWrapper
            pkgs.pkg-config
          ];

          buildInputs = [
            freerdp
            pkgs.libei
            pkgs.libvncserver
          ];

          CGO_ENABLED = "1";

          ldflags = [
            "-s"
            "-w"
          ];

          postInstall = ''
            wrapProgram $out/bin/headlessdesk \
              --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.fuse3 ]}

            mkdir -p $out/share/applications
            substitute ${./deploy/applications/headlessdesk.desktop} \
              $out/share/applications/headlessdesk.desktop \
              --replace-fail '{{HOME}}/.local/bin/headlessdesk' \
              "$out/bin/.headlessdesk-wrapped"
          '';

          meta = with pkgs.lib; {
            description = "Headless remote desktop screenshot and control server written in Go";
            license = licenses.gpl3Plus;
            mainProgram = "headlessdesk";
            platforms = platforms.linux;
          };
        };

        apps.default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/headlessdesk";
        };

        devShells.default = pkgs.mkShell {
          packages = [
            go
            pkgs.gopls
            pkgs.pkg-config
            gomod2nixPkgs.gomod2nix
            freerdp
            pkgs.fuse3
            pkgs.libei
            pkgs.libvncserver
          ];

          env.CGO_ENABLED = "1";
        };
      }
    );
}
