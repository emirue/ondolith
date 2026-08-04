# CONTRIBUTING

> 초안. 프로젝트가 Phase 0에 있어 기여 절차가 아직 굳어지지 않았다.

## 시작하기 전에

**[`.ai/DECISIONS.md`](.ai/DECISIONS.md)를 먼저 읽어주세요.** 이미 검토 후 배제된
선택지(templ 기반 테마, Next.js, PocketBase, Go `plugin` 패키지 등)의 목록과 그 사유가
정리되어 있습니다. 해당 항목을 다시 제안하는 이슈/PR은 그 파일을 근거로 닫힙니다.
뒤집을 근거가 있다면 이슈에서 그 파일을 먼저 논의해 주세요.

## 요구 사항

- Go 1.25 이상
- PostgreSQL 14 이상

## 개발

```bash
make check              # PR 전 필수. build · vet · gofmt · test(-race) · docs · selftest
make test-integration   # 실제 PostgreSQL 대상 설치 흐름 (선택, 아래 참고)
```

`make check`는 데이터베이스 없이 돕니다. 설치 흐름까지 확인하려면:

```bash
docker run -d --name ondolith-test -e POSTGRES_PASSWORD=testpw \
  -e POSTGRES_DB=ondolith -p 55432:5432 postgres:16-alpine
export ONDOLITH_TEST_DSN='postgres://postgres:testpw@127.0.0.1:55432/ondolith?sslmode=disable'
make test-integration
```

문서를 고쳤다면 `make check`의 `docs` 단계가 규칙 위반을 잡습니다 —
규칙은 [docs/90-conventions.md](docs/90-conventions.md)에 있습니다.

## 코드 규칙

- SQL은 항상 파라미터 바인딩. 문자열 연결 금지.
- 테마 렌더링 경로에는 `html/template`만 사용합니다.
- `template.HTML`을 쓰는 지점은 주석으로 사유를 남깁니다.
- 인증·세션·CSRF·업로드 검증을 직접 구현하지 않습니다.
- 의존성 추가는 신중히. 표준 라이브러리로 되는 일에는 의존성을 추가하지 않습니다.

## 보안 취약점

공개 이슈로 올리지 말고 비공개로 알려주세요. (연락처는 첫 릴리즈 전까지 확정 예정)

## 라이선스

기여하신 코드는 [Apache-2.0](LICENSE)으로 배포됩니다.
