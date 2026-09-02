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

**PostgreSQL 18 은 배포판 패키지로 오지 않는다.** Ubuntu 24.04 의 `apt install postgresql`
은 **16** 을 준다 — Lightsail 에서 문서대로 밟다가 여기서 막혔다 (W4-13). PostgreSQL 공식
apt 저장소(PGDG)에서 받는다 ([공식 절차](https://www.postgresql.org/download/linux/ubuntu/)):

```bash
sudo apt install -y postgresql-common
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
sudo apt update && sudo apt install -y postgresql-18
psql --version    # → psql (PostgreSQL) 18.x
```

512MB 인스턴스에서 기본 설정(`shared_buffers` 128MB)으로 기동됐고 앱까지 올린 뒤 가용
메모리가 약 180MB 남았다 (2026-09-02 실측, `nano_3_0`).

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
sudo mkdir -p /opt/ondolith
sudo chown "$(id -un)" /opt/ondolith    # ← 없으면 4절 제출이 실패한다
cd /opt/ondolith
sudo curl -sSL -o ondolith \
  https://github.com/emirue/ondolith/releases/download/<vX.Y.Z>/ondolith-linux-amd64
sudo chmod +x ondolith
./ondolith -version
```

> **`chown` 을 빼면 4절에서 막힌다.** 이 디렉터리는 내려받는 곳이 아니라 **앱이
> 쓰는 곳**이다 — 마법사가 `ondolith.json` 을 여기 쓰고, 업로드·테마 디렉터리도
> 그 파일 위치를 기준으로 잡힌다 (`internal/config`). `sudo mkdir` 로 만든
> 디렉터리는 root 소유라, 아래 3절처럼 일반 사용자로 띄우면 제출 순간
> `설정 파일을 저장하지 못했습니다: ... permission denied` 가 난다.
> 서비스로 돌릴 것이라면 이 사용자 대신 전용 계정에 넘긴다
> ([D72 2절](72-deploy-lightsail.md)).

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
> - **방화벽으로 막고 설치한다** (권장). Lightsail 이면 **인스턴스 방화벽**으로 한다 —
>   콘솔의 「네트워킹 → IPv4 방화벽」에서 8080 을 내 IP 로만 열거나:
>   ```bash
>   aws lightsail put-instance-public-ports --instance-name <이름> \
>     --port-infos "fromPort=22,toPort=22,protocol=tcp,cidrs=<내-IP>/32" \
>                  "fromPort=8080,toPort=8080,protocol=tcp,cidrs=<내-IP>/32"
>   ```
>   **`ufw allow` 만으로는 아무것도 막히지 않는다.** Lightsail 의 Ubuntu 는 `ufw` 가
>   **inactive** 로 온다 — `sudo ufw allow from <내-IP>/32 to any port 8080` 은
>   `Rules updated` 를 찍고 끝이고, 규칙은 `sudo ufw enable` 전까지 적용되지 않는다.
>   문서대로 하고 보호된다고 믿은 채 열려 있던 것을 Lightsail 에서 확인했다 (W4-13).
>   `ufw` 를 쓰려면 **SSH 를 먼저 허용하고** 켠다 — 순서가 바뀌면 자신이 잠긴다:
>   ```bash
>   sudo ufw allow OpenSSH && sudo ufw allow from <내-IP>/32 to any port 8080 && sudo ufw enable
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
