# Architecture (RTP-cleaner POC)

## Назначение сервиса

RTP-cleaner — это RTP normalizer для H264, предназначенный для корректировки проблемных RTP пакетов домофона перед передачей в rtpengine/WebRTC. Сервис находится между doorphone и rtpengine и исправляет только видеопоток (audio проксируется как есть).

## Поток медиаданных

```
Doorphone ↔ (leg A) RTP-cleaner ↔ (leg B) rtpengine ↔ WebRTC
```

### Почему comedia только на leg A

* Doorphone находится за NAT и шлёт RTP с непредсказуемого порта, поэтому leg A обучается по первому входящему RTP пакету (comedia).
* Для leg B порт назначения известен заранее (rtpengine), поэтому peer фиксируется и comedia не используется.

### Почему dest на leg B фиксируется через API

* Kamailio/rtpengine выделяют конечные RTP порты и могут менять их в процессе offer/answer.
* RTP-cleaner получает актуальные `ip:port` через API update и всегда отправляет трафик именно туда.

### Почему RTCP отсутствует

* Домофон не поддерживает RTCP.
* POC ориентирован на минимальный RTP-only прокси, поэтому RTCP не резервируется (`RTP+1` не используется) и не обрабатывается.

## Направления трафика

* **A_in → B_out**: трафик от doorphone в сторону rtpengine.
  * Видео: проходит через video-fix pipeline.
  * Аудио: проксируется без изменений.
* **B_in → A_out**: трафик от rtpengine в сторону doorphone.
  * Видео/аудио: проксируются без изменений.

## Video-fix pipeline (H264 over RTP)

1. **Frame buffering (1 access unit)**
   * Пакеты буферизуются до завершения access unit (FU-A End либо одиночный NAL).
2. **Marker fix**
   * `marker=1` выставляется только на последнем RTP пакете access unit.
3. **Wallclock timestamps**
   * Timestamp генерируется по wallclock, все пакеты кадра получают одинаковый TS.
   * Шаг ограничен разумным диапазоном (по умолчанию 10–100 ms).
4. **SPS/PPS pending**
   * SPS/PPS, пришедшие вне кадра, не отправляются сразу.
   * Они прикрепляются к следующему кадру (перед ним) с тем же timestamp.
