# RTP Stream Cleaner (POC)

## Documentation

* [Introduction](INTRODUCTION.md)
* [Specification](SPEC.md)
* [Architecture](docs/architecture.md)
* [Kamailio flow](docs/kamailio-flow.md)

## Quick start

Build binary:

```bash
go build -o bin/rtp-cleaner ./cmd/rtp-cleaner
```

Run service:

```bash
PUBLIC_IP=203.0.113.10 ./bin/rtp-cleaner
```

## Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `API_LISTEN_ADDR` | `0.0.0.0:8080` | HTTP listen address. |
| `PUBLIC_IP` | _(required)_ | Public IP returned by the session API. |
| `INTERNAL_IP` | _(optional)_ | Internal IP returned by the session API. If empty, `PUBLIC_IP` is used instead (so `PUBLIC_IP` must be set). |
| `RTP_PORT_MIN` | `30000` | First port in allocator range. |
| `RTP_PORT_MAX` | `40000` | Last port in allocator range. |
| `PEER_LEARNING_WINDOW_SEC` | `10` | Time window to learn/re-learn doorphone peer on audio leg A. |
| `MAX_FRAME_WAIT_MS` | `120` | Max wait before forcing a video frame flush. |
| `IDLE_TIMEOUT_SEC` | `60` | Auto-delete sessions after inactivity. |

## API quick reference

Create session:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/session \
  -H 'Content-Type: application/json' \
  -d '{"call_id":"demo","from_tag":"a","to_tag":"b","audio":{"enable":true},"video":{"enable":true,"fix":true}}'
```

Update session with rtpengine destination:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/session/<session_id>/update \
  -H 'Content-Type: application/json' \
  -d '{"audio":{"rtpengine_dest":"10.0.0.5:40100"},"video":{"rtpengine_dest":"10.0.0.5:40102"}}'
```

Delete session:

```bash
curl -s -X DELETE http://127.0.0.1:8080/v1/session/<session_id>
```

## Checking video marker/timestamp (tshark)

Capture RTP on a specific port and print marker/timestamp:

```bash
tshark -i any -f "udp port <video_port>" -Y rtp -T fields \
  -e frame.time_relative -e rtp.seq -e rtp.marker -e rtp.timestamp
```

Verify marker placement per access unit in a saved pcap:

```bash
tshark -r capture.pcapng -Y rtp -T fields -e rtp.seq -e rtp.marker -e rtp.timestamp
```

## Local UDP passthrough test (audio)

Run the service:

```bash
PUBLIC_IP=127.0.0.1 go run ./cmd/rtp-cleaner
```

Create a session and capture returned `audio.a_port` and `audio.b_port`:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/session \
  -H 'Content-Type: application/json' \
  -d '{"call_id":"demo","from_tag":"a","to_tag":"b","audio":{"enable":true},"video":{"enable":false}}'
```

Start a local UDP listener to emulate rtpengine (update destination):

```bash
socat -u UDP-RECV:40000 STDOUT
```

Update the session with the destination above:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/session/<session_id>/update \
  -H 'Content-Type: application/json' \
  -d '{"audio":{"rtpengine_dest":"127.0.0.1:40000"}}'
```

Send a packet to audio A (should be forwarded to the rtpengine listener):

```bash
echo "ping-a" | socat -u - UDP:127.0.0.1:<audio_a_port>
```

In another terminal, emulate rtpengine sending back from audio B:

```bash
echo "ping-b" | socat -u - UDP:127.0.0.1:<audio_b_port>
```

The packet should arrive on the doorphone peer (the host/port that sent the A packet). You can observe it by running a listener before sending the A packet:

```bash
socat -u UDP-RECV:<doorphone_port> STDOUT
```

## Limitations (POC)

* No RTCP support.
* No SRTP support.
* No ICE or NAT traversal beyond comedia on leg A.
