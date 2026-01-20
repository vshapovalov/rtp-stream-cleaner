# RTP Stream Cleaner (POC)

## Documentation

* [Introduction](INTRODUCTION.md)
* [Specification](SPEC.md)

## Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `API_LISTEN_ADDR` | `0.0.0.0:8080` | HTTP listen address. |
| `PUBLIC_IP` | _(required)_ | Public IP returned by the session API. |
| `INTERNAL_IP` | _(optional)_ | Internal IP returned by the session API. If empty, `PUBLIC_IP` is used instead. |
| `RTP_PORT_MIN` | `30000` | First port in allocator range. |
| `RTP_PORT_MAX` | `40000` | Last port in allocator range. |
