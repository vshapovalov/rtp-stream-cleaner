Yes, **this is a very good idea** 👍
A short “prologue” like this really helps Codex **interpret the spec correctly** instead of optimizing the wrong things or simplifying critical details.

Below is a **ready-to-use prologue** you can place **before the spec**, either as `INTRODUCTION.md` or as the top section of `SPEC.md`.

---

# Prologue / Context for the RTP Cleaner Project

## Why this project exists

This project is a **proof-of-concept (POC)** service that repairs **incorrect RTP video (H264)** in real time.

The source is a **doorphone** that:

* sends H264 over RTP,
* **violates RFC 6184**,
* and as a result **WebRTC clients receive RTP but drop the video**.

This project is **not a production solution**. Its goals are:

* to prove the stream can be fixed “on the fly”,
* and to integrate this into an existing SIP/WebRTC stack **without rewriting rtpengine or Kamailio**.

---

## Where the service sits in the infrastructure

Typical call path:

```
Doorphone ⇄ Kamailio ⇄ RTP-cleaner ⇄ rtpengine ⇄ WebRTC client
```

* RTP-cleaner is inserted **only between the doorphone and rtpengine**.
* The WebRTC client **never talks to RTP-cleaner directly**.
* RTP-cleaner knows nothing about SIP, SDP, or WebRTC logic — only RTP.

---

## What exactly was wrong with the RTP stream

From analysis of real packet captures (pcap), we saw:

### 1) Marker bit

* marker (`M=1`) was set:

  * on SPS/PPS,
  * in the middle of a frame,
  * multiple times within one access unit
* WebRTC **expects marker only on the last RTP packet of a frame**

### 2) Timestamp

* the same RTP timestamps were used:

  * for different frames,
  * for SPS/PPS and the previous non-IDR frame
* this is **invalid for WebRTC** and causes dropped frames

### 3) SPS/PPS and IDR

* SPS/PPS could arrive:

  * “between frames”,
  * with the previous frame’s timestamp
* WebRTC cannot match these SPS/PPS with the next IDR and **never starts decoding**

---

## How the problem was reproduced and proven

We used an offline workflow:

1. Extracted an H264 elementary stream from a problematic pcap
2. Re-sent that stream as RTP via ffmpeg
3. The new pcap showed:

   * correct markers
   * correct timestamps
4. That RTP **was decoded successfully by a WebRTC client**

Then a CLI tool was built that:

* read a pcap,
* fixed marker/timestamp/SPS-PPS,
* generated a “repaired” pcap,
* and confirmed that WebRTC starts decoding.

The live service is a **direct continuation of that offline logic**.

---

## What RTP-cleaner actually does

RTP-cleaner **does not decode video** and **does not change codecs**.

It only does the following:

* assembles RTP packets into an **access unit (frame)**,
* sets:

  * `marker=1` only on the last packet of a frame,
  * the same timestamp on all packets of a frame,
* generates timestamps **from wallclock**,
* buffers SPS/PPS and associates them with the correct frame,
* proxies audio **without changes**.

---

## Limitations and assumptions (important)

* This is a **POC**, not production
* RTCP **is not used** (the doorphone doesn’t support it)
* 1 UDP port per stream (RTP only)
* Small additional latency is acceptable (up to ~150 ms)
* Sequence numbers in the baseline version **are not renumbered**
* Video fixes apply **only on the doorphone → rtpengine direction**

---

## Why you cannot “just proxy RTP”

Even if RTP goes through Kamailio/rtpengine:

* WebRTC **receives packets**, but:

  * does not decode,
  * considers the stream invalid,
  * silently drops frames

RTP-cleaner is needed as a **media shim** that normalizes RTP into a WebRTC-friendly form.

---

## What the implementation should deliver

The goal is to **reproduce in live mode** the same logic already proven in pcap:

* minimal buffering (1 frame),
* minimal delay,
* correct H264 RTP semantics.

---

## Key takeaway (if you read only one paragraph)

> RTP-cleaner is not “just another RTP proxy”.
> It is an **H264-aware RTP normalizer** that fixes doorphone errors that prevent WebRTC from decoding video.
