# PATTERNS

이 저장소의 코드 관례. 새 패턴이 자리 잡으면 여기에 추가한다.

## 커넥션

`pgxpool.Pool` 하나가 앱과 세션 스토어(`scs/pgxstore`)를 모두 담당한다.
goose는 `*sql.DB`를 요구하므로 `stdlib.OpenDBFromPool(pool)`로 브리지를 만들어 넘긴다.
접속 설정은 한 군데만 존재해야 한다.

```go
pool, err := pgxpool.New(ctx, dsn)
db := stdlib.OpenDBFromPool(pool)   // goose 전용, 마이그레이션 후 Close
sessions.Store = pgxstore.New(pool) // scs
```

**`db.Close()`는 풀을 닫지 않는다.** pgx 문서가 명시한다 —
*"closing the returned `*sql.DB` will not close the `*pgxpool.Pool`"*.
부팅 전체가 이 성질에 걸려 있으므로(닫혔다면 첫 요청에서 DB 연결이 끊긴다)
`internal/app/integration_test.go`가 이를 고정한다. pgx를 올릴 때 그 테스트가
깨지면 동작이 바뀐 것이다.

## 마이그레이션

`goose.NewProvider(dialect, db, fsys)` — 전역 상태(`goose.SetBaseFS`) 대신 Provider를 쓴다.
`fsys`는 `internal/migrations`의 `embed.FS`. **별도 CLI 실행 단계가 없어야 한다.**

## 에러

핸들러는 에러를 삼키지 않는다. 사용자에게는 일반 메시지, 로그에는 원인.
설치 마법사만 예외 — DB 접속 실패 같은 것은 사용자가 고쳐야 하므로 원문을 보여준다.

## 템플릿

- 코어가 함수맵을 제공한다. 테마는 데이터만 받아 렌더링한다.
- 개발 모드: 캐시 끄고 매 요청 재파싱. 운영 모드: 캐시.
