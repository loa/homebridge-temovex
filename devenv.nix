{ pkgs, ... }:

{
  # https://devenv.sh/packages/
  packages = [ pkgs.git ];

  # https://devenv.sh/profiles/
  # devenv up
  # devenv --profile modbus-proxy shell
  profiles."modbus-proxy".module = {
    languages.go.enable = true;

    env.TEMOVEX_ADDR = "192.168.3.2:26486";
    env.HTTP_ADDR = ":8080";

    # https://devenv.sh/processes/
    processes.modbus-proxy = {
      exec = "go run ./cmd/modbus-proxy";
      cwd = "modbus-proxy";
      env.TEMOVEX_ADDR = "192.168.3.2:26486";
      env.HTTP_ADDR = ":8080";
      ready.http.get = {
        port = 8080;
        path = "/health";
      };
    };

    enterTest = ''
      cd modbus-proxy
      go test ./...
    '';
  };
}
