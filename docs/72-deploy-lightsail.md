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
| 시간대 | `sudo timedatectl set-timezone Asia/Seoul` | **서울 리전도 기본이 `Etc/UTC` 다** (2026-10-02 `ap-northeast-2a` · `ubuntu_24_04` 실측). 관리자 기간 조회가 서버 시간대로 하루를 자른다 — 근거·확인 방법은 [D71 「시작하기 전에」](71-install-guide.md) |

PostgreSQL을 같은 인스턴스에 올린다면 `shared_buffers`를 낮춘다 — 기본값은 이 크기를
가정하지 않는다 ([D70 「자원」](70-operations.md)).

---

## 2. systemd 유닛

**먼저 유닛이 쓸 계정을 만들고 디렉터리를 넘긴다.** 아래 유닛의 `User=ondolith` 는
이 계정을 가리키는데, `/opt/ondolith` 는 [D71 2절](71-install-guide.md)까지만 밟았다면
설치한 사람 계정(또는 root) 소유다 — 이 두 줄을 건너뛰면 서비스가 설정 파일도 업로드도
쓰지 못한다:

```bash
sudo useradd --system --home-dir /opt/ondolith --shell /usr/sbin/nologin ondolith
sudo chown -R ondolith:ondolith /opt/ondolith
```

D71 을 이미 밟아 자기 계정으로 설치를 마쳤다면 `chown` 이 그 산출물(`ondolith.json`·
업로드·테마)의 소유권까지 함께 넘긴다.

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

    client_max_body_size 65m;   # 앱의 가장 큰 상한(글+첨부·상품 이미지 64MiB) + 멀티파트 여유.
                                # 더 작으면 앱의 안내 대신 nginx 의 413 화면이 나온다. 테마 zip 은 앱이 24MiB 로 자른다
    # 한 번 HTTPS 로 온 브라우저는 이후 평문으로 오지 않는다. 앱은 TLS 를 모르므로
    # 이 헤더는 프록시가 낸다 — 세션 쿠키가 평문으로 다니는 창을 닫는 것이다.
    add_header Strict-Transport-Security "max-age=31536000" always;

    location / {
        proxy_pass         http://127.0.0.1:8080;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;   # ← 이 줄이 중요하다
        proxy_set_header   X-Real-IP         $remote_addr;  # ← 속도 제한이 방문자마다 되려면
    }
}
```

그리고 `/opt/ondolith/ondolith.json` 에 한 줄을 더한다 (아래 「프록시 뒤의 속도 제한」):

```json
"proxy_ip_header": "X-Real-IP"
```

### 프록시 뒤의 속도 제한

D15 4.3-2 의 IP 별 제한(로그인 10/분, 재설정 3/시간, 관리자 화면 60/분 …)은 클라이언트
주소로 센다. 같은 인스턴스의 프록시 뒤에서는 **모든 연결의 `RemoteAddr` 가 127.0.0.1** 이므로,
`proxy_ip_header` 가 없으면 IP 별 제한이 **사이트 전체 제한 하나**가 된다. 로그인은 IP 버킷이
비면 계정 확인 전에 거절하므로, **익명 한 명이 분당 10번 틀리는 것만으로 관리자를 포함한 모든
로그인이 막힌다.** 관리자 화면 제한도 같아서 초당 1회면 관리자 화면 전체가 잠긴다
(2026-10-07 감사에서 확인). nginx 의 `limit_req` 를 더해도 앱 안의 전역 버킷은 그대로라 풀리지 않는다.

그래서 위 nginx 블록이 `X-Real-IP` 를 **덮어써** 넘기고, `ondolith.json` 의
`"proxy_ip_header": "X-Real-IP"` 가 그 헤더를 클라이언트 주소로 쓰게 한다. 앱은 이 헤더를
**루프백에서 온 연결에서만** 읽는다 — 밖에서 곧장 들어와 같은 헤더를 보내는 요청은 무시하므로,
요청자가 자기 버킷을 고를 수 없다. 값이 IP 하나가 아니면 역시 무시한다(그래서
`X-Forwarded-For` 가 아니라 `X-Real-IP` 다). 작업 로그·웹훅 로그의 IP 도 함께 실제 주소가 된다.

**CloudFront 를 앞에 두면** nginx 의 `$remote_addr` 가 CloudFront 엣지 주소다. nginx 의 realip
모듈로 CloudFront 대역에서 온 연결에 한해 실제 방문자 주소를 꺼내야 `X-Real-IP` 가 방문자가 된다
(4절). 그러지 않으면 엣지 하나를 거치는 방문자들이 버킷 하나를 나눠 쓴다.

```nginx
# server 블록 안. 대역은 AWS 가 공개하는 CloudFront 오리진 쪽 주소다 — ip-ranges.json 의
# "service": "CLOUDFRONT" 항목, 또는 관리형 접두사 목록 com.amazonaws.global.cloudfront.origin-facing.
set_real_ip_from  <CloudFront 대역>;   # 대역마다 한 줄
real_ip_header    X-Forwarded-For;
# real_ip_recursive 는 기본값(off) 그대로 — 헤더의 **마지막** 주소를 쓴다. CloudFront 는
# 방문자 주소를 X-Forwarded-For 의 끝에 덧붙이므로, 방문자가 보낸 앞부분은 쓰이지 않는다.
```

(2026-10-07 확인: AWS CloudFront 개발자 안내서 「Request and response behavior for custom origins」의
Client IP addresses 절, 「Locations and IP address ranges of CloudFront edge servers」, nginx
`ngx_http_realip_module` 문서.) 대역은 바뀌므로 갱신 방법을 정해 둔다.

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

`site_url`도 같은 파일에 있다(메일 링크와 사이트맵·robots.txt 가 이 값을 쓴다). 설치 요청의 주소를 그대로 잡으므로, 도메인을 붙이거나 TLS 로
옮긴 뒤에는 `https://<도메인>` 으로 고친다 — 재설정·인증 메일의 링크가 이 값으로 만들어진다.

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
| 오리진 요청 정책 — 헤더 | **`Host`, `Origin`, `Sec-Fetch-Site`** (「Include the following headers」) | 아래 「`Origin`·`Sec-Fetch-Site`」. `Host` 를 넘기면 오리진 인증서가 방문자 도메인과 맞아야 한다 |
| 오리진 요청 정책 — 쿠키 | All | 세션 쿠키. 「None」 이면 로그인이 유지되지 않는다 |
| 오리진 요청 정책 — 쿼리 문자열 | All | 「None」 이면 `?page=`·`?q=`·`?sort=` 가 오리진에 닿지 않는다 |
| 오리진 응답 시한 | 기본 30초 그대로 | A-508 은 조회 예산 20초 + 조회 하나 5초로 그 안에 끝난다. 넘기면 CloudFront 가 GET 을 끊고 다시 보내 대사가 처음부터 다시 돈다 |
| WAF 룰 | AWS 관리형 공통 룰셋 + 레이트 리밋 | |

**`Origin`·`Sec-Fetch-Site` 를 넘기지 않으면 CSRF 방어가 꺼진다.** 앱의 CSRF 방어는 Go 의
`http.CrossOriginProtection` 하나이고, 그것은 이 두 헤더로 교차 출처를 판단한다 — **둘 다 없으면
「같은 출처이거나 브라우저가 아닌 요청」으로 보고 통과시킨다** (`go doc net/http.CrossOriginProtection`).
오리진 요청 정책이 「Include the following headers」 이면 거기 적지 않은 방문자 헤더는 오리진에 가지
않으므로, 예전 이 표의 `Host`·`X-Forwarded-Proto`·`Cookie` 대로 두면 남의 사이트가 보낸 POST 가 전부
통과한다. 오류도 경고도 없다 — 같은 사이트에서는 모든 것이 정상으로 보인다. `X-Forwarded-Proto` 는
적을 필요가 없다: CloudFront 는 방문자가 보낸 그 헤더를 지우고, nginx 가 자기 `$scheme`(오리진이
HTTPS only 라 `https`)으로 채운다. (2026-10-07 확인: AWS CloudFront 개발자 안내서 「Understand origin
request policies」·「Request and response behavior for custom origins」의 헤더 표·「Origin settings」의
Response timeout.)

**웹훅 경로(`/webhooks/payment/*`)를 WAF가 막지 않게 한다.** PG 서버는 브라우저가 아니라
봇 탐지 룰에 걸리기 쉽다. 이 경로가 막히면 가상계좌 입금이 주문에 반영되지 않고, 그 사실은
[A-603 웹훅 수신 이력](13-screens-admin.md)에 **아무 기록도 없는 것**으로 나타난다 —
가장 알아차리기 어려운 실패다.

---

## 5. 백업

업그레이드 전마다, 그리고 정기적으로:

```bash
sudo mkdir -p /backup
sudo -u postgres pg_dump -Fc ondolith | sudo tee /backup/ondolith-$(date +%F).dump >/dev/null
sudo cp /opt/ondolith/ondolith.json /backup/
sudo tar czf /backup/uploads-$(date +%F).tgz -C /opt/ondolith uploads
```

**`sudo` 가 붙는 이유.** 2절의 `chown -R ondolith` 뒤로 `ondolith.json` 은
`ondolith:ondolith 600` 이라 운영자 계정은 읽지 못하고, `pg_dump` 는 운영자 계정이
PostgreSQL 롤이 아니라서 peer 인증에서 막힌다. 앞선 판의 세 줄은 Lightsail 에서 **셋 다
실패했다** — `/backup` 이 없고, `Permission denied`, 그리고 롤 없음 (W4-13). 백업이
안 뜨는 것은 업그레이드 직전에 알게 된다.

**세 가지를 함께 뜬다.** DB만 뜨면 첨부 파일이 없는 글이 되살아나고, 설정 파일이 없으면
복원한 DB에 붙을 방법이 없다 — 그리고 `secret_key` 가 거기 있어, 파일 없이는 DB 에 봉인된
PG 시크릿·SMTP 비밀번호·소셜 client_secret 을 풀 수 없다([D60](60-security.md) 「저장된
자격증명」). 그 경우 관리자 화면에서 다시 입력하면 된다.

되돌릴 수 없는 마이그레이션이 포함된 릴리즈의 다운그레이드는 **이 백업의 복원이 유일한
경로다** (NFR-308).

**관리자 화면에서도 받는다** (A-604, FR-713). 최고 관리자가 비밀번호를 다시 넣으면 DB 덤프와 업로드
폴더가 zip 하나로 내려온다 — 셸 없이 받는 길이다. 설정 파일은 들어 있지 않으므로 위의 `ondolith.json`
복사는 따로 한다. 서버에 `pg_dump` 가 있어야 하는데, 2절에서 PostgreSQL 을 같은 서버에 설치했다면 이미 있다.

### 정기 백업은 자동 스냅샷으로

위의 명령과 A-604 는 **사람이 할 때만** 뜬다. 매일 뜨는 백업은 Lightsail 의 인스턴스 자동 스냅샷을 켠다:
콘솔 → **Instances** → 인스턴스 → **Snapshots** 탭 → **Automatic snapshots** 켜기. 디스크 전체(DB·업로드·설정
파일)가 함께 담긴다.

| 사실 (AWS 문서, 2026-10-08 확인) | 뜻 |
|---|---|
| 매일 한 번, 정한 시각(UTC)에 뜬다 | 하루치 이상을 잃지 않는다 |
| **최근 7개만** 남고 가장 오래된 것이 교체된다 | 일주일보다 오래된 시점은 없다 — 남길 것은 수동 스냅샷으로 복사한다 |
| **인스턴스를 지우면 자동 스냅샷도 지워진다** | 인스턴스를 갈아엎기 전에 수동 스냅샷으로 복사해 둔다 |
| 스냅샷 저장 요금이 붙는다 | 요금은 AWS 요금표를 본다 |

스냅샷은 서버 통째의 되돌리기이고, A-604 의 zip 은 **다른 서버로 옮기거나 한 사이트만 꺼내는** 백업이다.
A-604 의 **자동 백업**(FR-715)을 켜면 그 zip 이 매일 서버의 `backups/` 에도 쌓인다 — 디스크를 쓰는 만큼 Lightsail
디스크 크기를 본다. 같은 디스크에 있으므로 스냅샷을 대신하지 않는다.

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
