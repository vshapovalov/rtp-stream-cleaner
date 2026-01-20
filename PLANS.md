# PLANS.md — How to work on this project (for Codex)

## Purpose of this document

This document explains **how Codex should approach this project**.

The project is complex and touches RTP, H264 packetization, NAT/comedia, and WebRTC constraints.
Codex **must not attempt to solve everything at once**.

The correct approach is **incremental, layered, and conservative**.

---

## High-level goal (read this first)

> Implement a **POC RTP-cleaner service** that fixes **broken H264-over-RTP video** from a doorphone so that **WebRTC can decode it**, without changing Kamailio or rtpengine.

This is **not** a generic RTP proxy and **not** a production SBC.

---

## What this project is NOT

Codex must **not** assume or implement:

* full RTP stack
* RTCP, SR, RR, NACK, PLI, FIR
* SRTP, ICE, STUN
* media decoding or transcoding
* full jitter buffer or packet reordering
* production-grade reliability

If in doubt → **keep it simple**.

---

## How Codex should think about the system

### 1. Treat this as a media *normalizer*, not a media server

The service:

* does not understand SIP
* does not understand SDP semantics
* does not interact with WebRTC directly

It only:

* receives RTP
* rewrites RTP headers
* forwards RTP

---

### 2. Audio and video are fundamentally different

Codex must **separate concerns early**:

* **Audio**

  * pure UDP proxy
  * no inspection
  * no modification

* **Video**

  * H264-aware
  * frame-based buffering
  * marker/timestamp normalization

Never mix audio/video logic.

---

### 3. Only one frame of video is buffered

Key invariant:

> The service must never need more than **one H264 access unit** to function.

If Codex introduces:

* multi-frame queues
* reordering across frames
* long buffering

→ it is going in the wrong direction.

---

## Correct order of implementation (very important)

Codex **must follow this order**.

### Step 1 — Control plane only

* HTTP API
* session lifecycle
* port allocation

No UDP logic yet.

---

### Step 2 — RTP proxy without fixes

* UDP sockets
* A ↔ B forwarding
* comedia on leg A
* fixed destination on leg B

Still **no RTP parsing**.

---

### Step 3 — Video frame detection

* RTP header parsing
* H264 NAL / FU-A detection
* frame start / frame end

Still **no timestamp rewrite**.

---

### Step 4 — Marker normalization

* marker=1 only on last packet of a frame
* marker=0 everywhere else

At this point:

* RTP is still broken for WebRTC
* but frame boundaries are correct

---

### Step 5 — Timestamp normalization (wallclock-based)

* generate timestamps per frame
* same timestamp for all packets of a frame
* monotonic increase

This step alone already fixes many WebRTC issues.

---

### Step 6 — SPS/PPS handling (critical step)

This is **the main reason this project exists**.

Rules Codex must follow:

* SPS/PPS must never be sent with timestamp of previous frame
* SPS/PPS arriving between frames must be attached to the next frame
* Especially important before IDR frames

This step enables actual WebRTC decoding.

---

## Why SPS/PPS handling is mandatory

During offline analysis it was proven that:

* incorrect SPS/PPS timestamping causes:

  * framesReceived > 0
  * framesDecoded = 0
* fixing SPS/PPS timestamp alignment immediately enables decoding

Codex must **not treat SPS/PPS as “just another NAL”**.

---

## Timestamp strategy (do NOT overthink)

Codex must use **wallclock-based timestamps**:

* measure time between completed frames
* clamp delta to a reasonable range (10–100 ms)
* convert to 90 kHz RTP clock

Codex must **not**:

* try to infer FPS statistically
* trust incoming RTP timestamps
* pre-scan the stream

This is live media.

---

## Sequence numbers

Baseline rule:

* **Do not modify sequence numbers**

Unless Codex explicitly implements:

* packet injection
* seq renumbering

Sequence logic is optional and out of POC scope.

---

## NAT / comedia mental model

Codex must remember:

* Leg A (doorphone):

  * peer unknown until first packet
  * learned dynamically
* Leg B (rtpengine):

  * destination fixed via API
  * no peer learning

Packets must be sent:

* from the same socket they are received on

---

## Error handling philosophy

This is a POC.

When something goes wrong:

* drop packet
* log event
* continue

Do **not** block the pipeline.

---

## How to validate progress

Codex should consider the implementation successful when:

* audio flows unchanged
* video flows
* WebRTC `framesDecoded` increases
* no avalanche of dropped frames
* added latency ≈ one frame

---

## Final reminder for Codex

> This project already worked offline.
> Live service must **replicate the same logic**, not reinvent it.

If Codex is unsure:

* prefer minimal change
* prefer local buffering
* prefer deterministic behavior
* prefer an approach that is efficient in terms of processor time usage
