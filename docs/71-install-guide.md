# D71. 설치 가이드

빈 데이터베이스 하나와 바이너리 하나로 시작한다. **아래 명령은 그대로 복사해 실행할 수
있다** — 바꿔야 하는 곳은 `<...>` 로 표시했다.

관련: [D70 운영](70-operations.md) · [D20 부팅 모드](20-architecture.md)

---

## 시작하기 전에

| 필요한 것 | 최소 | 확인 |
|---|---|---|
| PostgreSQL | 18 | `psql --version` |
| 리눅스 서버 | 1 vCPU / 512MB | [D70 「자원」](70-operations.md)의 실측 참조 |
| 열린 포트 | 8080 (또는 프록시 뒤) | |

바이너리 외에 설치할 것은 없다 (NFR-102). 런타임·패키지 매니저·컨테이너가 필요 없다.

---

## 1. 빈 데이터베이스 만들기

```bash
sudo -u postgres psql <<'SQL'
CREATE USER ondolith WITH PASSWORD '<강한-비밀번호>';
CREATE DATABASE ondolith OWNER ondolith;
SQL
```

**테이블을 미리 만들지 않는다.** 스키마는 설치 마법사가 마이그레이션으로 적용한다
(FR-103) — 손으로 만든 테이블이 있으면 그것과 충돌한다.

접속을 먼저 확인한다. 여기서 막히면 설치 화면에서도 막힌다:

```bash
psql "postgres://ondolith:<강한-비밀번호>@127.0.0.1:5432/ondolith?sslmode=disable" -c 'SELECT 1'
```

---

## 2. 바이너리 내려받기

```bash
sudo mkdir -p /opt/ondolith && cd /opt/ondolith
sudo curl -sSL -o ondolith \
  https://github.com/emirue/ondolith/releases/download/<vX.Y.Z>/ondolith-linux-amd64
sudo chmod +x ondolith
./ondolith -version
```

arm64 인스턴스면 `ondolith-linux-arm64` 를 받는다. 두 산출물 모두 릴리즈 때 **해당
아키텍처에서 실제로 실행해** 확인한다 ([D70 「릴리즈 만들기」](70-operations.md)).

---

## 3. 실행

```bash
cd /opt/ondolith && ./ondolith -addr 0.0.0.0:8080
```

설정 파일(`ondolith.json`)이 없으면 **설치 마법사**가 뜬다. 있으면 운영 모드로 뜬다 —
설정 파일의 존재가 곧 「설치됨」의 정의다 ([D20](20-architecture.md)).

> ⚠️ **설치를 마치기 전까지, 그 주소에 접근할 수 있는 누구나 사이트를 점유할 수 있다.**
> 워드프레스·그누보드와 같은 성질이다 (FR-101). 마법사에는 인증이 없다 — 인증할 계정이
> 아직 없기 때문이다.
>
> 둘 중 하나를 한다:
> - **방화벽으로 막고 설치한다** (권장)
>   ```bash
>   sudo ufw allow from <내-IP>/32 to any port 8080
>   ```
>   설치를 마친 뒤 규칙을 넓힌다.
> - **띄운 즉시 설치를 끝낸다.** 자리를 비우지 않는다.

---

## 4. 브라우저에서 설치

`http://<서버>:8080/install` 을 연다. 네 가지를 넣는다:

| 항목 | 값 |
|---|---|
| DB 접속 | 1단계에서 만든 호스트·포트·사용자·비밀번호·DB 이름 |
| 사이트 이름 | 나중에 관리자 설정에서 바꿀 수 있다 |
| 관리자 이메일 | 로그인 ID 다 |
| 관리자 비밀번호 | **10자 이상** (NFR-208) |

제출하면 마법사가 마이그레이션을 적용하고, 관리자 계정을 만들고, `ondolith.json` 을
쓰고, **재시작 없이** 운영 모드로 넘어간다 (FR-107).

DB 접속이 틀리면 **원문 오류를 그대로 보여준다.** 이 화면의 청중은 운영자뿐이고, 고칠
사람도 그들이다 ([D60 「오류 메시지」](60-security.md)의 유일한 예외).

---

## 5. 확인

```bash
curl -fsS http://<서버>:8080/healthz     # → ok
```

`unavailable` 이면 DB 연결이 끊어진 것이다. 원인은 로그에만 남는다 — 공개 경로라
내부 구조를 응답에 담지 않는다 ([D12 P-907](12-screens-public.md)).

관리자로 들어가 남은 것을 채운다:

| 화면 | 언제 필요한가 |
|---|---|
| `/admin/settings` | 사이트 이름·유형(`cms`/`shop`) |
| `/admin/settings/mail` | 회원 가입 인증·비밀번호 재설정 메일 |
| `/admin/settings/payment` | **커머스를 쓴다면 필수** — PG 키가 없으면 결제가 되지 않는다 |
| `/admin/settings/business` | `shop` 모드의 전자상거래법 표시 의무 항목 |

---

## 다음

- 상시 운영으로 만들기: [D72 Lightsail 배포](72-deploy-lightsail.md)
- 업그레이드·백업: [D70 운영](70-operations.md)
