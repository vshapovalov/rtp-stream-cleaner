# Kamailio flow (RTP-cleaner POC)

## Где вызывать API RTP-cleaner

1. **INVITE от doorphone**
   * Создать сессию в RTP-cleaner.
   * Получить `public_ip/internal_ip` и `A/B` порты для audio/video.
2. **После получения портов rtpengine**
   * После `rtpengine_offer`/`rtpengine_answer` известны конечные `ip:port` rtpengine.
   * Вызвать `update` и передать `rtpengine_dest` для audio/video.
3. **BYE / cleanup**
   * На завершение диалога или при ошибке удалить сессию.

## Примеры curl запросов

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

### Update session (назначение rtpengine)

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

## Примеры вызовов API из Kamailio

Ниже — упрощённые примеры с `http_async_client` (аналогично можно использовать `http_client` или `exec`/`curl`).

### Create session (на INVITE)

```kamailio
route[CREATE_RTP_CLEANER] {
    $var(rc_body) = sprintf("{\"call_id\":\"%s\",\"from_tag\":\"%s\",\"to_tag\":\"%s\",\"audio\":{\"enable\":true},\"video\":{\"enable\":true,\"fix\":true}}",
        $ci, $ft, $tt);
    http_async_query("http://127.0.0.1:8080/v1/session", $var(rc_body), "RC_CREATE_REPLY");
}
```

### Update session (после rtpengine_offer/answer)

```kamailio
route[UPDATE_RTP_CLEANER] {
    $var(rc_body) = sprintf("{\"audio\":{\"rtpengine_dest\":\"%s:%s\"},\"video\":{\"rtpengine_dest\":\"%s:%s\"}}",
        $var(rtpe_ip), $var(rtpe_audio_port), $var(rtpe_ip), $var(rtpe_video_port));
    http_async_query("http://127.0.0.1:8080/v1/session/" + $var(rc_id) + "/update", $var(rc_body), "RC_UPDATE_REPLY");
}
```

### Delete session (на BYE)

```kamailio
route[DELETE_RTP_CLEANER] {
    http_async_query("http://127.0.0.1:8080/v1/session/" + $var(rc_id), "", "RC_DELETE_REPLY");
}
```

## Как переписывать SDP

### SDP к doorphone (leg A)

* `c=` → `PUBLIC_IP`
* `m=` → `A_port` (audio/video)

### SDP к rtpengine (leg B)

* `c=` → `INTERNAL_IP` (если задан) иначе `PUBLIC_IP`
* `m=` → `B_port` (audio/video)

## Важные условия

* **rtpengine comedia**: rtpengine ожидает трафик, приходящий именно с `B_port` RTP-cleaner (отправка идёт с того же UDP сокета).
* **doorphone peer learning**: домофонный peer определяется по первому RTP пакету на leg A, до этого трафик B → A может быть отброшен.
