# ТЗ: RTP Cleaner (POC) для Kamailio + rtpengine

## 0. Контекст и цель

Инфраструктура вызова:

```
Doorphone ⇄ Kamailio ⇄ RTP-cleaner ⇄ rtpengine ⇄ WebRTC client
```

Проблема: домофон шлёт **H264 over RTP** с некорректными:

* `marker` (не по access unit),
* `timestamp` (повторы, немонотонность),
* SPS/PPS могут приходить вне контекста IDR.

Из-за этого WebRTC принимает RTP пакеты, но **декодер дропает кадры**.

Цель проекта — реализовать **live RTP-cleaner (POC, не production)**, который:

* работает **только в плече домофон ⇄ rtpengine**
* пропускает **аудио как есть**
* **чинит только видео RTP**:

  * корректные marker по H264 (RFC 6184),
  * корректные timestamp (монотонные, по кадрам),
  * корректная привязка SPS/PPS к IDR,
* работает в условиях **NAT + comedia**,
* управляется из **Kamailio по HTTP JSON API**.

Домофон **не поддерживает RTCP**, RTCP полностью игнорируется.

---

## 1. Позиция в сигнальном и медиа-флоу

RTP-cleaner находится **между домофоном и rtpengine**.

WebRTC клиент работает **напрямую с rtpengine**.

Для каждого media (audio, video) используется **1 UDP порт на поток на каждой ноге**:

* **leg A (doorphone-facing)**
  RTP от/к домофону
  → требуется comedia peer learning

* **leg B (rtpengine-facing)**
  RTP от/к rtpengine
  → peer фиксированный, задаётся через API, comedia не используется

Итого на одну сессию:

* audio: A_port + B_port
* video: A_port + B_port
  **Всего 4 UDP порта на сессию**

---

## 2. SDP и роли Kamailio

### SDP к домофону

Kamailio подставляет:

* `c=` → `PUBLIC_IP`
* `m=` → `A_port` (audio / video)

### SDP к rtpengine

Kamailio подставляет:

* IP RTP-cleaner (доступный из rtpengine)
* `B_port` (audio / video)

`rtpengine_dest`, передаваемый в API, — это **порт rtpengine, который был бы использован напрямую домофоном**, если бы cleaner не существовал.

---

## 3. NAT / Comedia поведение

### Leg A (doorphone-facing)

* слушает UDP `A_port`
* `doorphone_peer` неизвестен до первого пакета
* при первом RTP пакете:

  ```
  doorphone_peer = srcIP:srcPort
  ```
* разрешено переобучение peer только в первые
  `PEER_LEARNING_WINDOW_SEC` (default 10s)
* обратный трафик (B → A) отправляется **только если peer известен**
* если peer неизвестен — пакеты B → A дропаются

### Leg B (rtpengine-facing)

* `rtpengine_dest` задаётся через API (`ip:port`)
* отправка **всегда** на `rtpengine_dest`
* входящие пакеты принимаются **только от rtpengine_dest.ip**
  (порт можно не проверять жёстко из-за NAT)
* **никакого peer learning**

### Важное требование

* Отправка RTP **должна идти с того же UDP socket**, который слушает соответствующий порт
  (важно для comedia у rtpengine).

---

## 4. Транспорт

* UDP only
* RTP only
* RTCP **не реализуется**, не резервируется `RTP+1`
* SRTP не используется

---

## 5. API управления (HTTP JSON)

Управление осуществляется из Kamailio через:

* `http_async_client`, либо
* любой HTTP/exec модуль (curl допустим для POC)

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

### 5.2 Update session (назначение rtpengine)

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
* `GET /v1/session/{id}` — текущее состояние, порты, peer, counters

---

## 6. Аллокация портов

Конфиг:

* `RTP_PORT_MIN` (default 30000)
* `RTP_PORT_MAX` (default 40000)

Требования:

* порты выделяются без конфликтов
* освобождаются при delete или по idle timeout

---

## 7. Таймауты и GC

* `IDLE_TIMEOUT_SEC` (default 60s):
  нет RTP → сессия удаляется
* `MAX_FRAME_WAIT_MS` (default 120ms):
  если кадр не завершился — forced flush
* `PEER_LEARNING_WINDOW_SEC` (default 10s)

---

## 8. Видео-фикс (H264 over RTP)

Фикс применяется **только** на направлении **video A → B**.

### 8.1 Распознавание

Поддержать:

* Single NAL (types 1, 5)
* FU-A (type 28)
* SPS/PPS (types 7, 8)

Если payload не распознан как H264 — пакет проксируется как есть.

### 8.2 Marker

* `marker = 1` **только** на последнем RTP пакете access unit
* FU-A → пакет с FU-End
* Single NAL slice → сам пакет
* SPS/PPS → marker всегда `0`

### 8.3 Timestamp (live)

Timestamp генерируется **по wallclock**, а не по входящему RTP.

Алгоритм:

* при завершении кадра:

  ```
  dt = now - last_frame_sent_time
  dt = clamp(dt, 10ms, 100ms)
  frameTS += round(dt * 90000)
  ```
* все пакеты кадра получают одинаковый `frameTS`
* SPS/PPS, относящиеся к кадру, получают `frameTS`

### 8.4 SPS/PPS pending + cache

* SPS/PPS, пришедшие **вне кадра**, не отправляются сразу
* они сохраняются как `pending`
* при старте следующего кадра:

  * pending SPS/PPS отправляются **перед кадром** с timestamp кадра
* если кадр IDR и pending нет:

  * допускается (опционально) инжект cached SPS/PPS
    (по умолчанию выключено, см. флаг)

### 8.5 Forced flush

Если кадр начат, но не завершён за `MAX_FRAME_WAIT_MS`:

* flush текущего буфера
* marker=1 на последнем пакете
* timestamp = текущий frameTS
* увеличить счётчик `forced_flushes`

### 8.6 Sequence numbers

POC baseline:

* sequence numbers **не изменяются**
* SPS/PPS injection по умолчанию **без добавления новых RTP пакетов**

Опционально (future):

* разрешить injection с renumbering seq

---

## 9. Аудио

* проксируется A ↔ B без изменений
* comedia только на leg A
* RTCP не используется

---

## 10. Логи и метрики (POC)

Логи:

* create / update / delete сессий
* peer learned
* ошибки UDP send/recv
* video forced flush

Counters per session:

* audio/video pkts & bytes (A_in, B_out, B_in, A_out)
* video_frames_flushed
* video_forced_flushes
* current frame buffer size

---

## 11. Технологии и структура

* Go ≥ 1.22
* `net/http`, `net`
* без декодирования H264
* структура проекта:

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

## 12. Конфигурация (env)

* `API_LISTEN_ADDR` (default `0.0.0.0:8080`)
* `PUBLIC_IP` (обязателен)
* `INTERNAL_IP`
* `RTP_PORT_MIN`, `RTP_PORT_MAX`
* `IDLE_TIMEOUT_SEC`
* `MAX_FRAME_WAIT_MS`
* `PEER_LEARNING_WINDOW_SEC`
* `VIDEO_INJECT_CACHED_SPS_PPS` (default false)

---

## 13. Definition of Done

POC считается готовым, если:

1. API create/update/delete работает
2. Kamailio успешно переписывает SDP
3. RTP проходит:

   * audio без изменений
   * video декодируется в WebRTC (`framesDecoded > 0`)
4. Дополнительная задержка ≤ ~150ms
5. Нет зависаний при потере FU-A End

---

## Анализ и декомпозиция на подзадачи

1. **Каркас сервиса и конфигурация**
   - Инициализировать CLI/entrypoint `cmd/rtp-cleaner` и структуру пакетов.
   - Реализовать загрузку env-конфигурации и валидацию обязательных параметров.
   - Подготовить базовое логирование.

2. **HTTP API управления**
   - Реализовать эндпоинты create/update/delete/health/get-session.
   - Описать модель сессии (ID, порты, peer, counters, состояние).
   - Добавить валидацию входных JSON и формирование ответов.

3. **Аллокация портов и управление жизненным циклом**
   - Реализовать безопасный выделитель портов в диапазоне.
   - Освобождение портов при delete/idle timeout.
   - Фоновая GC-задача с тайм-аутами.

4. **UDP слой и NAT/comedia для leg A**
   - Открытие UDP сокетов на A/B портах.
   - Peer learning для leg A с ограничением окна.
   - Отправка RTP с того же сокета, что и приём.

5. **Маршрутизация RTP A ↔ B**
   - Прокси аудио A↔B без изменений.
   - Видеопоток: A→B через rtpfix, B→A как есть.
   - Фильтрация входящих пакетов leg B по IP rtpengine.

6. **RTP fix для H264 видео**
   - Парсинг RTP заголовка и H264 payload (Single NAL, FU-A, SPS/PPS).
   - Коррекция marker по границам access unit.
   - Генерация timestamp по wallclock.
   - Pending SPS/PPS, cache и опциональный inject.
   - Forced flush по таймеру.

7. **Метрики и наблюдаемость**
   - Счётчики pkts/bytes по направлениям.
   - Счётчики video_frames_flushed и video_forced_flushes.
   - Экспорт состояния через GET /v1/session/{id}.

8. **Документация и готовность POC**
   - Описать expected flow для Kamailio + rtpengine.
   - Проверить Definition of Done и критерии готовности.
   - Добавить notes по ограничениям (RTCP/SRTP отсутствуют).
