# modbus-proxy

HTTP service that reads a REC Temovex controller the same way [temovex-exporter](https://github.com/loa/temovex-exporter) does, and publishes the five temperatures as JSON. `PUT /temperatures/set` writes the supply-air setpoint back to that controller.

The upstream session is EXOline-TCP (port 26486 in the exporter's unit file), not Modbus-TCP. Each scrape sends the exporter's five MULCMD reads, in the same order, and rounds each value to one decimal place before it is served. That rounding is the same `math.Round(v*10)/10` step the exporter applies to `temovex_temperature_c`.

| Name | Reading | Exporter label |
| --- | --- | --- |
| `set` | Supply-air setpoint | `temovex_temperature_c{name="set"}` |
| `al` | Exhaust air (avluft) | `temovex_temperature_c{name="al"}` |
| `fl` | Extract air (frånluft) | `temovex_temperature_c{name="fl"}` |
| `ul` | Outdoor air (uteluft) | `temovex_temperature_c{name="ul"}` |
| `tl` | Supply air (tilluft) | `temovex_temperature_c{name="tl"}` |

Requests are serialized on one TCP connection. A dropped connection is dialed again once before the request fails. A controller error frame is returned as-is and the connection stays open.

Do not publish this port on an untrusted network. The controller answers these reads and writes with no authentication. PUT changes the supply-air setpoint.

## Run

```bash
cd modbus-proxy
TEMOVEX_ADDR=192.168.3.2:26486 go run ./cmd/modbus-proxy
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `TEMOVEX_ADDR` | required | Controller `host:port`. The exporter uses `192.168.3.2:26486`. |
| `HTTP_ADDR` | `:8080` | Listen address. |
| `TEMOVEX_DIAL_TIMEOUT` | `10s` | TCP connect timeout. |
| `TEMOVEX_READ_TIMEOUT` | `3s` | Per-frame timeout. The EXOline reference client uses 3 seconds. |

```bash
curl -s localhost:8080/temperatures
```

```json
{
  "unit": "celsius",
  "temperatures": [
    {"name": "set", "description": "Supply-air setpoint", "celsius": 18.0},
    {"name": "al", "description": "Exhaust air", "celsius": 21.8},
    {"name": "fl", "description": "Extract air", "celsius": 22.1},
    {"name": "ul", "description": "Outdoor air", "celsius": 4.5},
    {"name": "tl", "description": "Supply air", "celsius": 19.2}
  ]
}
```

`GET /temperatures` reads all five sensors in one call. `PUT /temperatures/set` writes the supply-air setpoint (`set`). The other four names are measurements and have no write route. `GET /health` and `GET /ready` report that the process is serving. They do not dial the controller.

```bash
curl -s -X PUT localhost:8080/temperatures/set \
  -H 'content-type: application/json' \
  -d '{"celsius":21.5}'
```

```json
{
  "name": "set",
  "description": "Supply-air setpoint",
  "celsius": 21.5
}
```

`celsius` is rounded to one decimal place before it is written, the same rounding `GET /temperatures` applies. The rounded value must be from 0 to 50 inclusive. A value outside that range is rejected before a frame is sent. The controller can still reject a value outside its own configured limits, and that error is returned as JSON.

The write is a MULCMD-wrapped SRP (`0x32`) for line 4, cell 8, the same variable `GetSet` reads with RRP (`0xB6`). A successful answer is the five-byte ACK `3D 01 00 01 3E`.

## Docker

```bash
docker build -t modbus-proxy modbus-proxy
docker run --rm -p 8080:8080 -e TEMOVEX_ADDR=192.168.3.2:26486 modbus-proxy
```

## Test

```bash
cd modbus-proxy
go test ./...
```
