# Kamailio flow (RTP-cleaner POC)

## Where to call the RTP-cleaner API

1. **INVITE from the doorphone**
   * Create a session in RTP-cleaner.
   * Obtain `public_ip/internal_ip` and `A/B` ports for audio/video.
2. **After rtpengine ports are known**
   * After `rtpengine_offer`/`rtpengine_answer`, the final rtpengine `ip:port` is known.
   * Call `update` and pass `rtpengine_dest` for audio/video.
3. **BYE / cleanup**
   * On dialog termination or on error, delete the session.

## Example curl requests

### Create session

```bash
curl -s -X POST http://127.0.0.1:8080/v1/session \
  -H 'Content-Type: application/json' \
  -d '{
    "call_id":"call-123",
    "from_tag":"from-1",
    "to_tag":"to-1",
    "audio":{"enable":true},
    "video":{"enable":true,"fix":true}
  }'
```

### Update session (rtpengine destination)

```bash
curl -s -X POST http://127.0.0.1:8080/v1/session/S-123/update \
  -H 'Content-Type: application/json' \
  -d '{
    "audio":{"rtpengine_dest":"10.0.0.5:40100"},
    "video":{"rtpengine_dest":"10.0.0.5:40102"}
  }'
```

### Delete session

```bash
curl -s -X DELETE http://127.0.0.1:8080/v1/session/S-123
```

## Examples of calling the API from Kamailio

Below are simplified examples with `http_async_client` (similarly, you can use `http_client` or `exec`/`curl`).

### Create session (on INVITE)

```kamailio
route[CREATE_RTP_CLEANER] {
    $var(rc_body) = sprintf("{\"call_id\":\"%s\",\"from_tag\":\"%s\",\"to_tag\":\"%s\",\"audio\":{\"enable\":true},\"video\":{\"enable\":true,\"fix\":true}}",
        $ci, $ft, $tt);
    http_async_query("http://127.0.0.1:8080/v1/session", $var(rc_body), "RC_CREATE_REPLY");
}
```

### Update session (after rtpengine_offer/answer)

```kamailio
route[UPDATE_RTP_CLEANER] {
    $var(rc_body) = sprintf("{\"audio\":{\"rtpengine_dest\":\"%s:%s\"},\"video\":{\"rtpengine_dest\":\"%s:%s\"}}",
        $var(rtpe_ip), $var(rtpe_audio_port), $var(rtpe_ip), $var(rtpe_video_port));
    http_async_query("http://127.0.0.1:8080/v1/session/" + $var(rc_id) + "/update", $var(rc_body), "RC_UPDATE_REPLY");
}
```

### Delete session (on BYE)

```kamailio
route[DELETE_RTP_CLEANER] {
    http_async_query("http://127.0.0.1:8080/v1/session/" + $var(rc_id), "", "RC_DELETE_REPLY");
}
```

## How to rewrite SDP

### SDP to the doorphone (leg A)

* `c=` → `PUBLIC_IP`
* `m=` → `A_port` (audio/video)

### SDP to rtpengine (leg B)

* `c=` → `INTERNAL_IP` (if set), otherwise `PUBLIC_IP`
* `m=` → `B_port` (audio/video)

## Important conditions

* **rtpengine comedia**: rtpengine expects traffic coming specifically from RTP-cleaner `B_port` (sending happens from the same UDP socket).
* **doorphone peer learning**: the doorphone peer is determined by the first RTP packet on leg A; before that, B → A traffic may be dropped.
