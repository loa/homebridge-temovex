# modbus-proxy

HTTP service that reads a REC Temovex controller the same way [temovex-exporter](https://github.com/loa/temovex-exporter) does, and publishes the five temperatures as JSON.

The upstream session is EXOline-TCP (port 26486 in the exporter's unit file), not Modbus-TCP. Each scrape sends the exporter's five MULCMD reads, in the same order, and rounds each value to one decimal place before it is served. That rounding is the same `math.Round(v*10)/10` step the exporter applies to `temovex_temperature_c`.

| Name | Reading | Exporter label |
| --- | --- | --- |
| `set` | Supply-air setpoint | `temovex_temperature_c{name="set"}` |
| `al` | Exhaust air (avluft) | `temovex_temperature_c{name="al"}` |
| `fl` | Extract air (frånluft) | `temovex_temperature_c{name="fl"}` |
| `ul` | Outdoor air (uteluft) | `temovex_temperature_c{name="ul"}` |
| `tl` | Supply air (tilluft) | `temovex_temperature_c{name="tl"}` |

Requests are serialized on one TCP connection. A dropped connection is dialed again once before the request fails. A controller error frame is returned as-is and the connection stays open.

Do not publish this port on an untrusted network. The controller answers these reads with no authentication.

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
{"unit":"celsius","set":18.0,"al":21.8,"fl":22.1,"ul":4.5,"tl":19.2}
```

`GET /temperatures/ul` returns that one sensor:

```json
{"name":"ul","description":"Outdoor air","unit":"celsius","celsius":4.5}
```

`GET /health` and `GET /ready` report that the process is serving. They do not dial the controller.

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
