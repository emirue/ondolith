BIN     := ondolith
PKG     := ./cmd/ondolith
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build run check test test-integration schema e2e screens test-db-down vet fmt docs selftest vuln release verify-release measure verify-upgrade test-toss clean

help:
	@# 숫자를 포함한 대상(e2e)도 나와야 한다 — 목록에 없으면 없는 명령이다.
	@grep -hE '^[a-z0-9-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | expand -t20

build: ## 로컬 바이너리 빌드
	go build -o $(BIN) $(PKG)

run: build ## 빌드 후 실행
	./$(BIN)

check: build vet fmt test docs selftest ## 품질 게이트 — 완료 선언 전 필수 (NFR-404)
	@echo "check ok  $(VERSION)"

docs: ## 문서 규칙 검증 — 규칙 본문은 docs/90-conventions.md (NFR-407)
	@sh scripts/checkdocs.sh

selftest: ## 셸 도구가 위반을 실제로 잡는지 실패 주입으로 검증 (NFR-405)
	@sh scripts/selftest.sh

test: ## 테스트 (경합 탐지 포함). DB 불필요
	# -p 1: DB 테스트는 ONDOLITH_TEST_DSN 이 없으면 건너뛰지만, 개발자 셸에 그 값이
	# 떠 있으면 여기서도 돈다. 패키지가 병렬로 돌면 같은 DB의 스키마를 서로 드롭한다.
	#
	# 건너뛴 건수를 세어 알린다. DSN 없이 돌면 커머스·인증·설치의 단언이 하나도
	# 실행되지 않는데, 그때도 "check ok" 만 나오면 게이트가 전부 돌았다고 읽힌다.
	# 실패로 만들지는 않는다 — 네트워크 없는 환경에서 게이트가 깨지면 안 된다.
	@sh scripts/report-skips.sh

screens: ## D11 의 모든 GET 화면을 열어 본다 (응답·본문·템플릿 오류)
	sh scripts/screens.sh

crawl: ## 홈에서 링크를 따라간다 — 화면이 만든 주소가 실제로 열리는지
	sh scripts/crawl.sh

e2e: ## D83 시나리오 재실행 — 빌드된 바이너리에 빈 DB 를 붙여 설치부터 한 바퀴
	sh scripts/e2e.sh

schema: ## docs/schema.sql 재생성 — ERD 도구에 올리는 통합 스키마
	sh scripts/schema-sql.sh

test-integration: ## 실제 PostgreSQL 대상 통합 테스트. DSN 없으면 Docker 로 띄운다
	@sh scripts/integration.sh

test-db-down: ## 로컬 테스트 DB 컨테이너 제거
	@sh scripts/testdb.sh down

vet: ## go vet
	go vet ./...

fmt: ## gofmt 위반 검사 (수정하지 않는다)
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt 필요:"; echo "$$out"; exit 1; fi

# check 에 넣지 않는다: 네트워크가 필요해 게이트가 오프라인에서 깨진다.
# 릴리즈 전 필수이며, 그 강제는 릴리즈 스크립트가 진다 (D21 1절, D22 8절, D81 W4-02).
vuln: ## 의존성 취약점 조회 (NFR-209). check 에는 없다 — 릴리즈 전 필수
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# **취약점 게이트가 릴리즈의 일부다** (W4-02). 별도 단계로 두면 급할 때
# 건너뛰는 단계가 되고, 건너뛴 릴리즈와 거친 릴리즈를 나중에 구분할 수 없다.
# 오프라인이면 릴리즈가 멈춘다 — 그것이 맞다: 검증하지 않은 것을 내보내는 것이
# 릴리즈를 못 만드는 것보다 나쁘다.
release: vuln ## dist/ 에 크로스 컴파일 + 실행 검증 (NFR-306, NFR-209)
	@rm -rf dist && mkdir -p dist
	@for target in linux/amd64 linux/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-$$os-$$arch $(PKG) || exit 1; \
	done
	@ls -lh dist
	@sh scripts/verify-release.sh

clean:
	rm -rf $(BIN) dist

verify-release: ## dist/ 산출물을 실제 아키텍처에서 실행해 검증 (W4-01)
	@sh scripts/verify-release.sh

measure: ## 1 vCPU / 512MB 티어에서 자원 실측 (NFR-101, W4-08)
	@sh scripts/measure-resources.sh

verify-upgrade: ## 데이터가 든 인스턴스에서 업그레이드 절차 실측 (NFR-301, W4-11)
	@sh scripts/verify-upgrade.sh

# 토스 계정과 네트워크가 필요해 check 에도 test-integration 에도 넣지 않는다.
# 태그 없이는 빌드조차 되지 않으므로 SKIP 이 생기지 않는다 (check-testrun.sh).
test-toss: ## 토스 테스트 키로 어댑터 실측 (W3-34). ONDOLITH_TOSS_TEST_SECRET 필요
	@sh scripts/toss-live.sh
