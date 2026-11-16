// Command ondolith is the whole product: front-end, admin and API in one
// binary. On boot it either serves the install wizard or the site.
//
// 부팅은 internal/boot 가 한다 — 사이트 실행 파일(site.Main, D42)도 같은 코드를 부른다.
package main

import (
	"fmt"
	"os"

	"github.com/emirue/ondolith/internal/boot"
)

// version is stamped at release time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	if err := boot.Run(boot.Options{Version: version}); err != nil {
		fmt.Fprintln(os.Stderr, "ondolith:", err)
		os.Exit(1)
	}
}
