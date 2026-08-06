# D72. Lightsail 배포 가이드

[D71 설치](71-install-guide.md)가 「띄운다」였다면 여기는 「계속 떠 있게 한다」다:
systemd·TLS·CloudFront·WAF.

관련: [D70 운영](70-operations.md) · [D60 보안](60-security.md)

---

## 1. 인스턴스

| 항목 | 값 | 근거 |
|---|---|---|
| 플랜 | 1 vCPU / 512MB 이상 | 유휴 RSS 5 MiB 실측 ([D70 「자원」](70-operations.md)) |
| 블루프린트 | Ubuntu LTS | 바이너리가 정적이라 배포판을 가리지 않는다 |
| 아키텍처 | amd64 또는 arm64 | 릴리즈가 둘 다 낸다 |

PostgreSQL을 같은 인스턴스에 올린다면 `shared_buffers`를 낮춘다 — 기본값은 이 크기를
가정하지 않는다 ([D70 「자원」](70-operations.md)).

---

## 2. systemd 유닛

```ini
# /etc/systemd/system/ondolith.service
[Unit]
Description=Ondolith
After=network.target postgresql.service

[Service]
User=ondolith
WorkingDirectory=/opt/ondolith
ExecStart=/opt/ondolith/ondolith -addr 127.0.0.1:8080
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/opt/ondolith

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ondolith
systemctl status ondolith
```

**`ProtectSystem=strict`가 핵심이다.** 파일시스템 전체가 읽기 전용이 되고
`ReadWritePaths`만 쓸 수 있다. 업로드 경로 탈출이 성공해도 쓸 곳이 없다
([D60 「파일 업로드」](60-security.md)의 마지막 방어선).

**`-addr 127.0.0.1:8080`** — TLS를 붙일 것이므로 밖으로 열지 않는다. 프록시 없이 쓸
거라면 `0.0.0.0:8080`이지만, 그러면 평문이다.

---

## 3. TLS

리버스 프록시 없이도 동작한다 (NFR-106). 다만 **다음 중 하나라도 해당하면 TLS가 필수다**:

- 결제를 다룬다 (카드 정보가 PG 결제창으로 가더라도 세션 쿠키가 평문으로 다닌다)
- 휴대폰 카메라로 QR 재고 스캔을 쓴다 — `getUserMedia`는 보안 컨텍스트에서만 동작한다
  ([D13 A-514~A-517](13-screens-admin.md))
- 관리자가 공용 네트워크에서 접속한다

```nginx
server {
    listen 443 ssl;
    server_name <도메인>;

    ssl_certificate     /etc/letsencrypt/live/<도메인>/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/<도메인>/privkey.pem;

    client_max_body_size 24m;   # 테마 zip 업로드 상한 + 멀티파트 여유

    location / {
        proxy_pass         http://127.0.0.1:8080;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;   # ← 이 줄이 중요하다
    }
}
```

### `X-Forwarded-Proto`와 `secure_cookies`

**설치 마법사는 이 헤더를 보고 `secure_cookies`를 정한다.** HTTPS로 접속했다고 판단하면
세션 쿠키에 `Secure` 플래그가 붙고, 그 쿠키는 평문 HTTP로는 전송되지 않는다.

| 상황 | 결과 |
|---|---|
| 프록시 뒤 + 헤더 넘김 + HTTPS로 설치 | `secure_cookies: true` — 옳다 |
| 프록시 뒤 + **헤더 안 넘김** | `false`로 잡힌다. 쿠키가 평문으로도 다닌다 |
| 평문 HTTP로 설치한 뒤 TLS를 붙임 | `false`로 남는다 |

뒤 두 경우는 `/opt/ondolith/ondolith.json`의 `secure_cookies`를 직접 `true`로 고치고
재시작한다. **설치를 다시 하지 않는다** — 설정 파일 한 줄이다.

---

## 4. 결제를 다루는 경우: CloudFront + WAF

```
사용자 → CloudFront (+ AWS WAF) → Lightsail 인스턴스 → ondolith
```

**Lightsail에는 AWS WAF를 직접 붙일 수 없다.** 결제를 다루면 CloudFront를 앞단에 두고
WAF를 CloudFront에 붙인다.

| 설정 | 값 | 이유 |
|---|---|---|
| 오리진 프로토콜 | HTTPS only | 오리진 구간도 평문으로 두지 않는다 |
| 캐시 정책 | `/static/*`만 캐시 | 나머지는 세션에 따라 내용이 달라진다 |
| **캐시하지 않을 것** | `/healthz`, `/admin/*`, `/orders/*`, `/cart` | 캐시된 `ok`는 죽은 인스턴스를 살아 있다고 보고한다 |
| 전달 헤더 | `Host`, `X-Forwarded-Proto`, `Cookie` | 위 절의 이유 |
| WAF 룰 | AWS 관리형 공통 룰셋 + 레이트 리밋 | |

**웹훅 경로(`/webhooks/payment/*`)를 WAF가 막지 않게 한다.** PG 서버는 브라우저가 아니라
봇 탐지 룰에 걸리기 쉽다. 이 경로가 막히면 가상계좌 입금이 주문에 반영되지 않고, 그 사실은
[A-603 웹훅 수신 이력](13-screens-admin.md)에 **아무 기록도 없는 것**으로 나타난다 —
가장 알아차리기 어려운 실패다.

---

## 5. 백업

업그레이드 전마다, 그리고 정기적으로:

```bash
pg_dump -Fc ondolith > /backup/ondolith-$(date +%F).dump
cp /opt/ondolith/ondolith.json /backup/
tar czf /backup/uploads-$(date +%F).tgz /opt/ondolith/uploads
```

**세 가지를 함께 뜬다.** DB만 뜨면 첨부 파일이 없는 글이 되살아나고, 설정 파일이 없으면
복원한 DB에 붙을 방법이 없다.

되돌릴 수 없는 마이그레이션이 포함된 릴리즈의 다운그레이드는 **이 백업의 복원이 유일한
경로다** (NFR-308).

---

## 6. 모니터링

```bash
journalctl -u ondolith -f
```

| 보이는 것 | 뜻 | 조치 |
|---|---|---|
| `운영 모드 site=... version=...` | 정상 기동 | — |
| `헬스체크 실패` | DB 연결이 끊겼다 | DB 상태·자격증명 확인 |
| `웹훅 검증 실패` | 형식이 안 맞는 요청. 대개 스캔 트래픽 | 반복되면 원격 IP 확인 |
| `웹훅 처리` + 오류 | 수신은 됐고 처리가 실패했다 | **A-603에서 그 건을 본다** — 자동 재처리는 없다 |
| `작업 로그 기록 실패` | 감사 기록이 안 남았다 | DB 쓰기 가능 여부 확인 |
| `관리자 렌더링` | 템플릿 오류 | 테마를 되돌린다 |

**DSN·토큰·시크릿은 로그에 나오지 않는다** ([D22](22-dev-standards.md) 4절). 이것은 실기동
로그로 확인한다 — `make verify-upgrade`가 그 검사를 포함한다.

메트릭 엔드포인트는 두지 않는다. 이유는 [D70 「메트릭 노출」](70-operations.md)에 있다.

---

## 7. 업그레이드

[D70 「업그레이드 절차」](70-operations.md) 3단계가 전부다: 백업 → 바이너리 교체 →
재시작. 대기 마이그레이션은 부팅 때 적용된다. 데이터가 든 인스턴스에서 실측한 결과가
같은 절에 있다.
