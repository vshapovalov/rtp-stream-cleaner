# Spec: RTP Cleaner (POC) for Kamailio + rtpengine

## 0. Context and goal

Call infrastructure:

```
Doorphone ⇄ Kamailio ⇄ RTP-cleaner ⇄ rtpengine ⇄ WebRTC client
```

Problem: the doorphone sends **H264 over RTP** with incorrect:

* `marker` (not aligned to access units),
* `timestamp` (repeats, non-monotonic),
* SPS/PPS can arrive outside the IDR context.

As a result, WebRTC accepts RTP packets but the **decoder drops frames**.

Project goal: implement a **live RTP-cleaner (POC, not production)** that:

* operates **only on the doorphone ⇄ rtpengine leg**,
* forwards **audio unchanged**,
* **fixes video RTP only**:

  * correct markers per H264 (RFC 6184),
  * correct timestamps (monotonic, per-frame),
  * correct association of SPS/PPS with IDR,
* works behind **NAT + comedia**,
* is controlled from **Kamailio via an HTTP JSON API**.

The doorphone **does not support RTCP**, RTCP is fully ignored.

---

## 1. Position in signaling and media flow

RTP-cleaner sits **between the doorphone and rtpengine**.

The WebRTC client talks **directly to rtpengine**.

For each media (audio, video) there is **one UDP port per stream on each leg**:

* **leg A (doorphone-facing)**
  RTP to/from the doorphone
  → requires comedia peer learning

* **leg B (rtpengine-facing)**
  RTP to/from rtpengine
  → peer is fixed, provided via API, no comedia

Per session total:

* audio: A_port + B_port
* video: A_port + B_port
  **Total 4 UDP ports per session**

---

## 2. SDP and Kamailio roles

### SDP to the doorphone

Kamailio injects:

* `c=` → `PUBLIC_IP`
* `m=` → `A_port` (audio / video)

### SDP to rtpengine

Kamailio injects:

* RTP-cleaner IP (reachable from rtpengine)
* `B_port` (audio / video)

`rtpengine_dest` passed to the API is the **rtpengine port that the doorphone would have used directly** if cleaner did not exist.

---

## 3. NAT / Comedia behavior

### Leg A (doorphone-facing)

* listens on UDP `A_port`
* `doorphone_peer` is unknown until the first packet
* on first RTP packet:

  ```
  doorphone_peer = srcIP:srcPort
  ```
* peer relearning is allowed only during the first
  `PEER_LEARNING_WINDOW_SEC` (default 10s)
* reverse traffic (B → A) is sent **only if the peer is known**
* if the peer is unknown — B → A packets are dropped

### Leg B (rtpengine-facing)

* `rtpengine_dest` is provided via API (`ip:port`)
* send **always** to `rtpengine_dest`
* incoming packets are accepted **only from rtpengine_dest.ip**
  (port can be loosely checked because of NAT)
* **no peer learning**

### Important requirement

* RTP sending **must use the same UDP socket** that listens on that port
  (required for comedia in rtpengine).

---

## 4. Transport

* UDP only
* RTP only
* RTCP **is not implemented**, `RTP+1` is not reserved
* SRTP is not used

---

## 5. Control API (HTTP JSON)

Control is done from Kamailio via:

* `http_async_client`, or
* any HTTP/exec module (curl is acceptable for a POC)

### 5.1 Create session

`POST /v1/session`

```json
{
  "call_id": "string",
  "from_tag": "string",
  "to_tag": "string",
  "audio": { "enable": true },
  "video": { "enable": true, "fix": true }
}
```

Response:

```json
{
  "id": "S-123",
  "public_ip": "X.X.X.X",
  "internal_ip": "10.0.0.10",
  "audio": { "a_port": 30000, "b_port": 30002 },
  "video": { "a_port": 30004, "b_port": 30006 }
}
```

### 5.2 Update session (assign rtpengine)

`POST /v1/session/{id}/update`

```json
{
  "audio": { "rtpengine_dest": "10.0.0.5:40100" },
  "video": { "rtpengine_dest": "10.0.0.5:40102" }
}
```

### 5.3 Delete session

`DELETE /v1/session/{id}`

### 5.4 Observability

* `GET /v1/health`
* `GET /v1/session/{id}` — current state, ports, peer, counters

---

## 6. Port allocation

Config:

* `RTP_PORT_MIN` (default 30000)
* `RTP_PORT_MAX` (default 40000)

Requirements:

* allocate ports without conflicts
* release on delete or idle timeout

---

## 7. Timeouts and GC

* `IDLE_TIMEOUT_SEC` (default 60s):
  no RTP → session removed
* `MAX_FRAME_WAIT_MS` (default 120ms):
  if a frame does not complete — forced flush
* `PEER_LEARNING_WINDOW_SEC` (default 10s)

---

## 8. Video fix (H264 over RTP)

Fix applies **only** on **video A → B** direction.

### 8.1 Detection

Support:

* Single NAL (types 1, 5)
* FU-A (type 28)
* SPS/PPS (types 7, 8)

If the payload is not recognized as H264 — proxy the packet as-is.

### 8.2 Marker

* `marker = 1` **only** on the last RTP packet of an access unit
* FU-A → packet with FU-End
* Single NAL slice → the packet itself
* SPS/PPS → marker always `0`

### 8.3 Timestamp (live)

Timestamp is generated **from wallclock**, not from inbound RTP.

Algorithm:

* on frame completion:

  ```
  dt = now - last_frame_sent_time
  dt = clamp(dt, 10ms, 100ms)
  frameTS += round(dt * 90000)
  ```
* all packets of the frame share the same `frameTS`
* SPS/PPS belonging to the frame get `frameTS`

### 8.4 SPS/PPS pending + cache

* SPS/PPS arriving **outside a frame** are not sent immediately
* they are stored as `pending`
* at the start of the next frame:

  * pending SPS/PPS are sent **before the frame** with the frame timestamp
* if the frame is IDR and pending is empty:

  * optionally inject cached SPS/PPS
    (disabled by default; see flag)

### 8.5 Forced flush

If a frame started but does not complete within `MAX_FRAME_WAIT_MS`:

* flush the current buffer
* marker=1 on the last packet
* timestamp = current frameTS
* increment `forced_flushes`

### 8.6 Sequence numbers

POC baseline:

* sequence numbers **are not modified**
* SPS/PPS injection by default **does not add new RTP packets**

Optional (future):

* allow injection with sequence renumbering

---

## 9. Audio

* proxied A ↔ B without changes
* comedia only on leg A
* RTCP is not used

---

## 10. Logs and metrics (POC)

Logs:

* create / update / delete sessions
* peer learned
* UDP send/recv errors
* video forced flush

Counters per session:

* audio/video pkts & bytes (A_in, B_out, B_in, A_out)
* video_frames_flushed
* video_forced_flushes
* current frame buffer size

---

## 11. Technology and structure

* Go ≥ 1.22
* `net/http`, `net`
* no H264 decoding
* project structure:

```
cmd/rtp-cleaner/
internal/
  api/
  session/
  udp/
  rtpfix/
docs/
```

---

## 12. Configuration (env)

* `API_LISTEN_ADDR` (default `0.0.0.0:8080`)
* `PUBLIC_IP` (required)
* `INTERNAL_IP` (if not set, `PUBLIC_IP` is used)
* `RTP_PORT_MIN`, `RTP_PORT_MAX`
* `IDLE_TIMEOUT_SEC`
* `MAX_FRAME_WAIT_MS`
* `PEER_LEARNING_WINDOW_SEC`
* `VIDEO_INJECT_CACHED_SPS_PPS` (default false)

---

## 13. Definition of Done

The POC is considered done when:

1. API create/update/delete works
2. Kamailio successfully rewrites SDP
3. RTP passes through:

   * audio unchanged
   * video decodes in WebRTC (`framesDecoded > 0`)
4. Additional latency ≤ ~150ms
5. No hangs when FU-A End is lost

---

## Analysis and task decomposition

1. **Service skeleton and configuration**
   - Initialize CLI/entrypoint `cmd/rtp-cleaner` and package structure.
   - Implement env configuration loading and validate required parameters.
   - Prepare basic logging.

2. **HTTP control API**
   - Implement create/update/delete/health/get-session endpoints.
   - Define the session model (ID, ports, peer, counters, state).
   - Add JSON input validation and response formatting.

3. **Port allocation and lifecycle management**
   - Implement a safe allocator within the range.
   - Release ports on delete/idle timeout.
   - Background GC task with timeouts.

4. **UDP layer and NAT/comedia for leg A**
   - Open UDP sockets on A/B ports.
   - Peer learning for leg A with a bounded window.
   - Send RTP from the same socket used to receive.

5. **RTP routing A ↔ B**
   - Proxy audio A↔B unchanged.
   - Video stream: A→B through rtpfix, B→A as-is.
   - Filter incoming leg B packets by rtpengine IP.

6. **RTP fix for H264 video**
   - Parse RTP headers and H264 payloads (Single NAL, FU-A, SPS/PPS).
   - Correct marker on access unit boundaries.
   - Generate timestamps from wallclock.
   - Pending SPS/PPS, cache, and optional inject.
   - Forced flush on timer.

7. **Metrics and observability**
   - Counters for pkts/bytes by direction.
   - Counters for video_frames_flushed and video_forced_flushes.
   - Expose state via GET /v1/session/{id}.

8. **Documentation and POC readiness**
   - Document expected flow for Kamailio + rtpengine.
   - Check Definition of Done and readiness criteria.
   - Add notes on limitations (no RTCP/SRTP).
