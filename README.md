# Homebridge Temovex

Homebridge platform for a REC Temovex unit. It talks to [modbus-proxy](modbus-proxy/README.md) over HTTP.

The supply-air setpoint is a thermostat. Exhaust air, extract air, outdoor air, and supply air are separate temperature sensors. HomeKit reads trigger `GET /temperatures`, and reads that arrive together share that request. Changing the thermostat target sends `PUT /temperatures/set`. There is no background poll.

## Configuration

`url` is the proxy base URL and must include the protocol.

```json
{
  "platform": "Temovex",
  "name": "Temovex",
  "url": "http://192.168.1.10:8080"
}
```

The setpoint accepted by the proxy is 10–40 °C. The thermostat shows that value as both the current and target temperature, in Celsius. Its mode stays on heat: the proxy has no heating or cooling mode to change.

| Proxy name | Accessory | Reading |
| --- | --- | --- |
| `set` | Thermostat | Supply-air setpoint |
| `al` | Temperature sensor | Exhaust air |
| `fl` | Temperature sensor | Extract air |
| `ul` | Temperature sensor | Outdoor air |
| `tl` | Temperature sensor | Supply air |

## Development

Node.js 22 or 24 is required.

```shell
npm install
npm run lint
npm run build
npm test
```

`npm run watch` rebuilds and starts Homebridge with `test/hbConfig/config.json`.

The proxy is a separate Go service. Its tests are `go test ./...` in `modbus-proxy`.
