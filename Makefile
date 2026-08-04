BIN     := ondolith
PKG     := ./cmd/ondolith
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build run check test vet fmt docs selftest vuln release clean

help:
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | expand -t20

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
	go test -race -p 1 ./...

test-integration: ## 실제 PostgreSQL 대상 설치 흐름 테스트. ONDOLITH_TEST_DSN 필요
	@sh scripts/integration.sh

vet: ## go vet
	go vet ./...

fmt: ## gofmt 위반 검사 (수정하지 않는다)
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt 필요:"; echo "$$out"; exit 1; fi

# check 에 넣지 않는다: 네트워크가 필요해 게이트가 오프라인에서 깨진다.
# 릴리즈 전 필수이며, 그 강제는 릴리즈 스크립트가 진다 (D21 1절, D22 8절, D81 W4-02).
vuln: ## 의존성 취약점 조회 (NFR-209). check 에는 없다 — 릴리즈 전 필수
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

release: ## dist/ 에 크로스 컴파일 (NFR-306)
	@rm -rf dist && mkdir -p dist
	@for target in linux/amd64 linux/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-$$os-$$arch $(PKG) || exit 1; \
	done
	@ls -lh dist

clean:
	rm -rf $(BIN) dist
