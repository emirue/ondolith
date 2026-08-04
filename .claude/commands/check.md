---
description: 품질 게이트 실행 후 실패만 고친다
allowed-tools: Bash(make:*), Bash(go:*), Bash(gofmt:*), Read, Edit
---

`make check`를 실행한다.

- 통과하면 한 줄로 보고하고 끝낸다.
- 실패하면 **실패 원인만** 고친다. 눈에 띄는 다른 것을 손대지 않는다.
- 고친 뒤 다시 `make check`를 돌려 통과를 확인한다. 확인 없이 통과했다고 말하지 않는다.
