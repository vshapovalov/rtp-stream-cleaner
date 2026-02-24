# RTP peer learning and re-learning

This document describes the current RTP source learning behavior implemented in `rtp-stream-cleaner`.

## Why RTP peer learning exists

This proxy receives UDP packets on leg A from a doorphone side. In real deployments, the sender address (`IP:port`) on that side can change during a call (for example because of NAT rebinding, source port changes, or network switch such as Wi-Fi to LTE).

To keep reverse traffic (B -> A) flowing to the correct endpoint, the proxy learns a current source address and stores it as `learned_peer`. Reverse packets are forwarded to `learned_peer`.

## State machine

The tracker has two states:

- `LEARNING`
- `LOCKED`

Transitions:

1. Start in `LEARNING`.
2. In `LEARNING`, suitable packets are counted per candidate source (`udp src IP:port`).
3. When one candidate reaches `peer_learning_min_packets`, tracker sets `learned_peer` to this source and moves to `LOCKED`.
4. In `LOCKED`, only packets from `learned_peer` are accepted by the tracker.
5. If suitable packets from `learned_peer` are absent longer than `peer_relearn_idle_ms`, tracker clears `learned_peer`, returns to `LEARNING`, and starts candidate collection again.

## Initial learning flow

- Proxy tracker is created in `LEARNING` state.
- Candidates are keyed by full UDP source (`IP:port`).
- Only **suitable** packets participate in candidate counters.
- Once a candidate reaches `peer_learning_min_packets`, that source becomes `learned_peer`.
- After lock, state becomes `LOCKED`.

## Candidates and TTL

Candidate counts are tracked separately for each source address (`IP:port`).

Each candidate has a TTL controlled by `peer_learning_candidate_ttl_ms`:

- If a candidate is not seen for longer than TTL, its counter is removed.
- This prevents old/rare packets from contributing to a lock much later.

## Which packets are considered suitable

Suitability differs between audio and video:

- **Audio:** suitable packet = packet with a valid RTP header (`ParseRTPHeader` succeeds).
- **Video:** suitable packet = RTP packet that can be parsed as H264 media packet (`ParseRTPHeader` + H264 payload parse succeeds). Non-media/garbage UDP does not participate in learning.

## Re-learning behavior

While in `LOCKED`, tracker keeps forwarding eligibility only for the current `learned_peer`.

Re-learning trigger:

- If suitable packets from `learned_peer` do not arrive for `peer_relearn_idle_ms`, tracker reactivates learning:
  - state `LOCKED` -> `LEARNING`
  - `learned_peer` is cleared
  - candidate counters are reset
- Then locking happens again using the same rule: one source must deliver `peer_learning_min_packets` suitable packets.

This is what allows the proxy to recover from NAT rebinding, source port rotation, or source IP change (for example Wi-Fi -> LTE).

## Log-based diagnostics

Useful fields and messages:

- `learned_peer` in `audio.proxy.stats` and `video.proxy.stats` shows current lock target (`none` if not locked).
- `audio.proxy.sources` / `video.proxy.sources` (final stats) show observed source addresses and packet counts.
- `drop_peer_update_rejected` increases when A->B packets are rejected by tracker logic (for example not yet locked, wrong peer while locked, or unsuitable packets during learning).
- `drop_dest_ip_mismatch` is a different check on B->A path (rtpengine source IP mismatch vs configured destination IP). It is not a learning state transition signal.

Practical interpretation examples:

1. Two addresses in `*.proxy.sources` with meaningful counts can indicate source switch (or parallel senders).
2. If `learned_peer` in stats changes from one `IP:port` to another, re-learning happened.
3. After a source change, a short gap is expected: roughly `peer_relearn_idle_ms` (to unlock) + time to collect `peer_learning_min_packets` suitable packets (to re-lock).

## Configuration parameters

The behavior is controlled by these configuration keys:

### `peer_learning_min_packets`

- Meaning: number of suitable packets required from one candidate source to lock.
- Unit: packets.
- Default: `5`.
- Effect:
  - lower value = faster lock/re-lock, but easier to lock on transient traffic;
  - higher value = more stable lock, but slower recovery after source changes.

### `peer_learning_candidate_ttl_ms`

- Meaning: max age for inactive candidate counters in learning state.
- Unit: milliseconds.
- Default: `4000`.
- Effect:
  - lower value = stale candidates are forgotten sooner;
  - higher value = candidates survive longer pauses.

### `peer_relearn_idle_ms`

- Meaning: max idle duration without suitable packets from current `learned_peer` before returning to learning.
- Unit: milliseconds.
- Default: `1000`.
- Effect:
  - lower value = faster re-learning on source migration, but can re-learn on short jitter/bursts;
  - higher value = more tolerant to temporary silence/loss, but slower adaptation to real source changes.
