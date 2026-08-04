# D70. 배포·패치·운영

> 요구사항: [D10](10-requirements.md) NFR-1xx, NFR-3xx.

## 설계 목표: 패치가 쉬울 것

운영자는 개발자가 아니다. **업그레이드는 바이너리 교체 + 재시작이 전부여야 한다** (NFR-301).
운영 문서에 3단계 이상이 적히면 그건 설계 실패다.

이를 위해 코드가 지키는 것:

| 보장 | 어떻게 |
|---|---|
| 마이그레이션 자동 적용 | 부팅 시 `app.New`가 대기분을 적용한다 (NFR-302) |
| 설정 파일 불변 | 업그레이드가 `ondolith.json`을 건드리지 않는다 (NFR-304) |
| 업로드·테마 불변 | 바이너리 밖에 있다 (NFR-304) |
| 버전 확인 | `ondolith -version` (NFR-305) |
| 되돌리기 | 모든 마이그레이션에 `Down` (NFR-303). 불가한 경우 CHANGELOG에 명시 (NFR-308) |

## 업그레이드 절차

```bash
# 1. 백업 (아래 백업 절 참조)
pg_dump -Fc ondolith > /backup/ondolith-$(date +%F).dump
cp /opt/ondolith/ondolith.json /backup/

# 2. 교체
systemctl stop ondolith
curl -sSL -o /opt/ondolith/ondolith https://github.com/emirue/ondolith/releases/download/vX.Y.Z/ondolith-linux-amd64
chmod +x /opt/ondolith/ondolith

# 3. 재시작 — 대기 마이그레이션은 부팅 시 자동 적용된다
systemctl start ondolith
/opt/ondolith/ondolith -version
```

**릴리즈 노트에 파괴적 변경 표시가 있으면 그것만 추가로 읽는다.** 없으면 위가 전부다.

## 다운그레이드 (NFR-308)

1. 서비스 중지
2. 이전 바이너리로 교체
3. 새 릴리즈가 추가한 마이그레이션을 되돌린다
4. 시작

되돌릴 수 없는 마이그레이션(컬럼 삭제 등)이 포함된 릴리즈는 **CHANGELOG에 명시**하고,
그 경우 다운그레이드는 백업 복원이 유일한 경로다.

이런 상황을 줄이려고 [D30](30-data-model.md)의 **두 릴리즈 규칙**을 지킨다:
릴리즈 N에서 새 컬럼 추가 + 양쪽 쓰기 → N+1에서 옛 컬럼 삭제. 이러면 N+1에서 N으로
돌아가는 경로가 항상 남는다.

## 릴리즈 만들기

```bash
make release          # dist/ 에 크로스 컴파일 산출물 (NFR-306)
```

- 버전은 `-ldflags "-X main.version=vX.Y.Z"`로 바이너리에 새긴다
- `-s -w`로 심볼을 제거해 크기를 줄인다
- 대상: `linux/amd64`, `linux/arm64` (Lightsail은 양쪽 다 판다)
- `CGO_ENABLED=0` — 순수 정적 바이너리. 대상 서버의 glibc 버전을 신경 쓰지 않는다

## 서버 구성

### 최소

```
Lightsail 인스턴스 1대
 ├─ ondolith 바이너리 (systemd)
 └─ PostgreSQL 18    (같은 인스턴스에 직접 설치. 관리형 DB는 아래 참조)
```

**PostgreSQL 18을 최소로 두는 이유.** 예전에 적혀 있던 `14+`에는 근거가 없었다.
**PG 14는 2026-11-12에 EOL**이고([endoflife.date](https://endoflife.date/postgresql) 조회, 2026-08-03),
그건 이 제품의 첫 릴리즈보다 이르다 — 나오자마자 지원 종료된 DB를 최소로 요구하는 셈이 된다.

| 항목 | 값 |
|---|---|
| 개발·CI·통합 테스트 | **18** |
| 설치처 최소 요구 | **18** |
| 확인 방법 | 설치 마법사가 접속 직후 `SHOW server_version_num`으로 검사하고, 미달이면 **설치를 거부**한다 (FR-107의 원문 오류 표시 대상) |

> **관리형 DB를 쓰려면 버전을 먼저 확인해야 한다.** Amazon Lightsail Managed Database가
> 2026-08 현재 어느 메이저까지 제공하는지는 **공식 문서에서 확인하지 못했다** — 검색 결과가
> 2024년 기준이라 신뢰할 수 없다. D70의 기본 구성은 **같은 인스턴스에 직접 설치**이고
> 그 경로에는 제약이 없다. 관리형 DB 안내를 문서에 넣기 전에 콘솔에서 실제 목록을 확인한다
> ([DEC-4](../.ai/DECISIONS.md) 미검증 목록).

리버스 프록시 없이도 동작한다 (NFR-106). 다만 TLS가 필요하면 프록시를 둔다.

> **휴대폰 카메라로 QR 재고 스캔을 쓰려면 TLS가 사실상 필수다.** 브라우저의 `getUserMedia`는
> 보안 컨텍스트(HTTPS 또는 localhost)에서만 동작한다. 평문 HTTP 설치처에서는 카메라 경로가
> 조용히 죽으므로 **전용 스캐너나 손입력으로 A-514~A-517을 쓴다** ([D13](13-screens-admin.md)).
> 그래서 스캔 값은 장치와 무관하게 같은 입력창으로 받는다 — 카메라가 없다고 화면이 통째로
> 못 쓰게 되지 않는다.

### 권장 (결제를 다루는 경우)

```
CloudFront  →  Lightsail 인스턴스  →  ondolith
   │
   └─ AWS WAF
```

**Lightsail에는 AWS WAF를 직접 붙일 수 없다.** 결제를 다루므로 CloudFront를 앞단에 두고
WAF를 CloudFront에 붙이는 구성을 권장한다.

프록시 뒤에 둘 때:

- `X-Forwarded-Proto`를 넘긴다 → 설치 시 `secure_cookies` 감지에 쓰인다
- 이미 설치를 마쳤는데 값이 틀렸다면 `ondolith.json`의 `secure_cookies`를 직접 고친다

### systemd 유닛 예

```ini
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

`ProtectSystem=strict` + `ReadWritePaths`로 쓰기 가능한 경로를 좁힌다 — 업로드 취약점이
생겨도 피해 범위가 줄어든다.

## 자원 (NFR-101)

- 목표: 1 vCPU / 512MB~1GB 티어에서 동작
- 백그라운드 작업은 고루틴이다. 별도 워커 프로세스·크론 유닛을 추가하지 않는다 (NFR-103)
- 같은 인스턴스에 PostgreSQL을 올린다면 `shared_buffers` 등을 티어에 맞게 낮춘다.
  기본값은 이 크기 인스턴스를 가정하지 않는다

## 백업

업그레이드 전마다, 그리고 정기적으로.

| 대상 | 방법 |
|---|---|
| 데이터베이스 | `pg_dump -Fc` |
| 설정 파일 | `ondolith.json` — **DB 비밀번호가 들어 있다.** 공개 저장소에 두지 않는다 |
| 업로드 파일 | 업로드 디렉터리 전체 |
| 사용자 테마 | 테마 디렉터리 |

바이너리는 백업 대상이 아니다. 릴리즈에서 다시 받으면 된다.

**복원 테스트를 하지 않은 백업은 백업이 아니다.**

## 모니터링

Phase 4에서 정한다. 현재는 `slog` 텍스트 로그가 stderr로 나가고 systemd가 journald에 받는다.

```bash
journalctl -u ondolith -f
```

## 첫 설치

1. 빈 PostgreSQL 데이터베이스를 만든다
2. 바이너리를 실행한다
3. 브라우저로 `http://서버:8080/install`
4. DB 접속정보·사이트 이름·관리자 계정을 입력하고 제출

> ⚠️ **설치를 마치기 전까지 그 주소에 접근할 수 있는 누구나 사이트를 점유할 수 있다.**
> 워드프레스·그누보드와 같은 성질이다. 방화벽으로 접근을 제한한 상태에서 설치하거나,
> 서버를 띄운 직후 바로 설치를 마친다.

## 아직 정하지 않은 것

미결은 [D18 미결 대장](18-open-decisions.md)에 모아 둔다. 문서마다 표를 두면 결정을 내려도 일부만 지워져 낡은 항목이 남는다.
