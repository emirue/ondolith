package app

import (
	"net/http"

	"github.com/emirue/ondolith/internal/admin"
)

// buildTree is the operating route table: every Phase 1 screen, with the D11
// id, the D15 security class and the permission each one requires.
//
// Nothing reaches the mux except through the registry (D15 4.4). That is what
// makes the boot checks meaningful — a handler attached with mux.HandleFunc
// carries no screen id, so no check can see it, and the checks would report
// "all clear" on a tree with a hole in it.
//
// Permission is "" only where D11 says the screen is open to anonymous callers.
// It is a declaration either way: the field cannot be skipped, only decided.
//
// The field does NOT enforce. Only the tree gate (admin.access) and each
// handler's own check refuse a request (D15 4.2) — what this declaration buys is
// the boot check: a key that does not exist stops the server, and a permission
// no route names is reported as dead weight in the role editor.
func buildTree(pub *publicDeps, lg *loginDeps, acc *accountDeps, bd *boardDeps,
	ad *admin.Deps, sh *shopDeps, shop bool, static http.HandlerFunc,
) *Registry {
	r := NewRegistry()

	// ---- 공개 ---------------------------------------------------------------
	r.Add(Route{Screen: "P-201", Method: "GET", Pattern: "/{$}", Class: SC1, Handler: pub.home})
	r.Add(Route{Screen: "P-906", Method: "GET", Pattern: "/static/{path...}", Class: SC7, Handler: static})
	// P-907 헬스체크. 공개이고 상태를 바꾸지 않는다 — 로드밸런서가 읽는다.
	r.Add(Route{Screen: "P-907", Method: "GET", Pattern: "/healthz", Class: SC1,
		Handler: pub.health})

	// ---- 인증 ---------------------------------------------------------------
	r.Add(Route{Screen: "P-101", Method: "GET", Pattern: "/login", Class: SC2, Handler: lg.loginForm})
	r.Add(Route{Screen: "P-101", Method: "POST", Pattern: "/login", Class: SC2, Handler: lg.login})
	r.Add(Route{Screen: "P-102", Method: "POST", Pattern: "/logout", Class: SC2, Handler: lg.logout})
	r.Add(Route{Screen: "P-103", Method: "GET", Pattern: "/signup", Class: SC2, Handler: acc.signupForm})
	r.Add(Route{Screen: "P-103", Method: "POST", Pattern: "/signup", Class: SC2, Handler: acc.signup})
	r.Add(Route{Screen: "P-112", Method: "GET", Pattern: "/verify/{token}", Class: SC2, Handler: acc.verify})
	r.Add(Route{Screen: "P-104", Method: "GET", Pattern: "/password/reset", Class: SC2,
		Handler: acc.resetRequestForm})
	r.Add(Route{Screen: "P-104", Method: "POST", Pattern: "/password/reset", Class: SC2,
		Handler: acc.resetRequest})
	// The token is in the path, so it lands in the access log and in Referer.
	// That is the trade D11 already made for P-112, and it is bounded by the
	// same two properties: 30 minutes, one use.
	r.Add(Route{Screen: "P-105", Method: "GET", Pattern: "/password/reset/{token}", Class: SC2,
		Handler: acc.resetForm})
	r.Add(Route{Screen: "P-105", Method: "POST", Pattern: "/password/reset/{token}", Class: SC2,
		Handler: acc.resetPassword})

	// ---- 내 계정 (SC-3: 소유자는 세션이 정한다) --------------------------------
	r.Add(Route{Screen: "P-108", Method: "GET", Pattern: "/me", Class: SC3, Handler: acc.profileForm})
	r.Add(Route{Screen: "P-108", Method: "POST", Pattern: "/me", Class: SC3, Handler: acc.updateProfile})
	r.Add(Route{Screen: "P-109", Method: "GET", Pattern: "/me/password", Class: SC3, Handler: acc.passwordForm})
	r.Add(Route{Screen: "P-109", Method: "POST", Pattern: "/me/password", Class: SC3, Handler: acc.changePassword})

	// ---- 게시판 (SC-1 은 읽기, SC-2 는 폼, SC-3 은 본인 글) ---------------------
	// Permission is "" on all of them: the board's permission is scoped, so it
	// cannot be judged from the path alone (the slug names the board, and the
	// board id is what role_permissions holds). Every handler calls CanOn with
	// the board it just loaded — D15 4.2's "the gate sees a prefix, the handler
	// sees the target", in its sharpest form.
	r.Add(Route{Screen: "P-203", Method: "GET", Pattern: "/board/{slug}", Class: SC1,
		Handler: bd.boardList})
	r.Add(Route{Screen: "P-205", Method: "GET", Pattern: "/board/{slug}/write", Class: SC2,
		Handler: bd.postForm})
	r.Add(Route{Screen: "P-205", Method: "POST", Pattern: "/board/{slug}/write", Class: SC2,
		Handler: bd.postCreate})
	r.Add(Route{Screen: "P-204", Method: "GET", Pattern: "/board/{slug}/{id}", Class: SC1,
		Handler: bd.postView})
	r.Add(Route{Screen: "P-206", Method: "GET", Pattern: "/board/{slug}/{id}/edit", Class: SC3,
		Handler: bd.postEditForm})
	r.Add(Route{Screen: "P-206", Method: "POST", Pattern: "/board/{slug}/{id}/edit", Class: SC3,
		Handler: bd.postUpdate})

	r.Add(Route{Screen: "P-207", Method: "POST", Pattern: "/board/{slug}/{id}/delete", Class: SC3,
		Handler: bd.postDelete})
	r.Add(Route{Screen: "P-208", Method: "POST", Pattern: "/board/{slug}/{id}/comments", Class: SC2,
		Handler: bd.commentCreate})
	// P-209·P-210 은 경로에 slug 가 없다 (D11). 게시판은 댓글의 글을 거쳐
	// 찾고, 그 게시판의 post.read 가 여전히 판정한다 — 그러지 않으면 댓글
	// id 가 못 여는 게시판으로 들어가는 문이 된다.
	r.Add(Route{Screen: "P-209", Method: "GET", Pattern: "/comments/{id}/edit", Class: SC3,
		Handler: bd.commentEditForm})
	r.Add(Route{Screen: "P-209", Method: "POST", Pattern: "/comments/{id}/edit", Class: SC3,
		Handler: bd.commentUpdate})
	r.Add(Route{Screen: "P-210", Method: "POST", Pattern: "/comments/{id}/delete", Class: SC3,
		Handler: bd.commentDelete})

	// P-211 은 SC-7 이다. 경로에 게시판이 없으므로 핸들러가 첨부 → 글 → 게시판
	// 순으로 거슬러 올라가 post.read 를 **다시** 검사한다 (D15 8절 1번).
	r.Add(Route{Screen: "P-211", Method: "GET", Pattern: "/attachments/{id}", Class: SC7,
		Handler: bd.attachmentDownload})

	r.Add(Route{Screen: "P-212", Method: "GET", Pattern: "/search", Class: SC1,
		Handler: bd.search})

	// P-901·P-902 는 크롤러가 읽는다. 로그인하지 않은 방문자가 열 수 있는
	// 것과 정확히 같은 집합이어야 하므로, 사이트맵은 익명 권한으로 묻는다.
	r.Add(Route{Screen: "P-901", Method: "GET", Pattern: "/sitemap.xml", Class: SC1,
		Handler: bd.sitemap})
	r.Add(Route{Screen: "P-902", Method: "GET", Pattern: "/robots.txt", Class: SC1,
		Handler: bd.robots})

	// ---- 관리자 -------------------------------------------------------------
	// admin.access is the tree gate's key; every screen below also names the
	// permission its own handler checks. The two are not redundant: the gate
	// sees a path prefix, the handler sees the target (D15 4.2).
	r.Add(Route{Screen: "A-101", Method: "GET", Pattern: "/admin/{$}", Class: SC4,
		Permission: "admin.access", Handler: ad.Dashboard})

	r.Add(Route{Screen: "A-201", Method: "GET", Pattern: "/admin/settings", Class: SC4,
		Permission: "settings.update", Handler: ad.SettingsForm})
	r.Add(Route{Screen: "A-201", Method: "POST", Pattern: "/admin/settings", Class: SC5,
		Permission: "settings.update", Handler: ad.SettingsSave})
	r.Add(Route{Screen: "A-205", Method: "GET", Pattern: "/admin/settings/mail", Class: SC4,
		Permission: "settings.update", Handler: ad.MailSettingsForm})
	r.Add(Route{Screen: "A-205", Method: "POST", Pattern: "/admin/settings/mail", Class: SC5,
		Permission: "settings.update", Handler: ad.MailSettingsSave})

	r.Add(Route{Screen: "A-202", Method: "GET", Pattern: "/admin/themes", Class: SC4,
		Permission: "theme.view", Handler: ad.ThemeList})
	r.Add(Route{Screen: "A-202", Method: "POST", Pattern: "/admin/themes", Class: SC5,
		Permission: "theme.activate", Handler: ad.ThemeActivate})

	// A-203 은 SC-7 이고 제품에서 가장 위험한 화면이다 — 업로드가 곧 임의 파일
	// 쓰기이고 쓰인 파일이 템플릿으로 실행된다 (D60).
	r.Add(Route{Screen: "A-203", Method: "GET", Pattern: "/admin/themes/upload", Class: SC7,
		Permission: "theme.upload", Handler: ad.ThemeUploadForm})
	r.Add(Route{Screen: "A-203", Method: "POST", Pattern: "/admin/themes/upload", Class: SC7,
		Permission: "theme.upload", Handler: ad.ThemeUpload})

	r.Add(Route{Screen: "A-204", Method: "GET", Pattern: "/admin/menus", Class: SC4,
		Permission: "menu.manage", Handler: ad.MenuList})
	r.Add(Route{Screen: "A-204", Method: "POST", Pattern: "/admin/menus", Class: SC5,
		Permission: "menu.manage", Handler: ad.MenuCreate})
	r.Add(Route{Screen: "A-204", Method: "POST", Pattern: "/admin/menus/{id}", Class: SC5,
		Permission: "menu.manage", Handler: ad.MenuUpdate})
	r.Add(Route{Screen: "A-204", Method: "POST", Pattern: "/admin/menus/{id}/delete", Class: SC5,
		Permission: "menu.manage", Handler: ad.MenuDelete})

	r.Add(Route{Screen: "A-301", Method: "GET", Pattern: "/admin/pages", Class: SC4,
		Permission: "page.view", Handler: ad.PageList})
	r.Add(Route{Screen: "A-302", Method: "GET", Pattern: "/admin/pages/{id}", Class: SC4,
		Permission: "page.update", Handler: ad.PageForm})
	r.Add(Route{Screen: "A-302", Method: "POST", Pattern: "/admin/pages/{id}", Class: SC5,
		Permission: "page.update", Handler: ad.PageSave})
	r.Add(Route{Screen: "A-303", Method: "POST", Pattern: "/admin/pages/{id}/publish", Class: SC5,
		Permission: "page.publish", Handler: ad.PagePublish})
	// D11 gives A-301 only GET; D15 2.2 puts page.delete on A-302, and D19 reads
	// the two together as "삭제는 A-302 의 POST 동작이다".
	r.Add(Route{Screen: "A-302", Method: "POST", Pattern: "/admin/pages/{id}/delete", Class: SC5,
		Permission: "page.delete", Handler: ad.PageDelete})

	r.Add(Route{Screen: "A-401", Method: "GET", Pattern: "/admin/users", Class: SC4,
		Permission: "user.view", Handler: ad.UserList})
	r.Add(Route{Screen: "A-402", Method: "GET", Pattern: "/admin/users/{id}", Class: SC4,
		Permission: "user.update", Handler: ad.UserDetail})
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}", Class: SC5,
		Permission: "user.create", Handler: ad.UserCreate})
	// The three destructive account operations enforce re-authentication in the
	// handlers (D15 5.3-1), not here. A wrapper was written first and removed:
	// it duplicated a check the handlers already make, and the only behaviour it
	// added — a redirect to the form instead of a 403 — no mutation of it could
	// be made to fail. One enforced copy beats two, one of which nobody can
	// prove is running.
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}/deactivate", Class: SC5,
		Permission: "user.update", Handler: ad.UserDeactivate})
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}/delete", Class: SC5,
		Permission: "user.delete", Handler: ad.UserDelete})
	r.Add(Route{Screen: "A-402", Method: "POST", Pattern: "/admin/users/{id}/reset-password", Class: SC5,
		Permission: "user.reset_password", Handler: ad.UserResetPassword})

	r.Add(Route{Screen: "A-403", Method: "GET", Pattern: "/admin/roles", Class: SC4,
		Permission: "role.view", Handler: ad.RoleList})
	r.Add(Route{Screen: "A-404", Method: "POST", Pattern: "/admin/roles/{id}/permissions", Class: SC5,
		Permission: "role.manage", Handler: ad.RoleGrantPermission})
	r.Add(Route{Screen: "A-405", Method: "POST", Pattern: "/admin/users/{id}/roles", Class: SC5,
		Permission: "role.assign", Handler: ad.RoleAssign})

	r.Add(Route{Screen: "A-304", Method: "GET", Pattern: "/admin/boards", Class: SC4,
		Permission: "board.view", Handler: ad.BoardList})
	r.Add(Route{Screen: "A-305", Method: "GET", Pattern: "/admin/boards/{id}", Class: SC4,
		Permission: "board.manage", Handler: ad.BoardForm})
	r.Add(Route{Screen: "A-305", Method: "POST", Pattern: "/admin/boards/{id}", Class: SC5,
		Permission: "board.manage", Handler: ad.BoardSave})
	r.Add(Route{Screen: "A-305", Method: "POST", Pattern: "/admin/boards/{id}/delete", Class: SC5,
		Permission: "board.manage", Handler: ad.BoardDelete})
	r.Add(Route{Screen: "A-306", Method: "GET", Pattern: "/admin/boards/{id}/fields", Class: SC4,
		Permission: "board.manage", Handler: ad.BoardFields})
	r.Add(Route{Screen: "A-306", Method: "POST", Pattern: "/admin/boards/{id}/fields", Class: SC5,
		Permission: "board.manage", Handler: ad.BoardFieldSave})

	// A-307·A-308 의 Permission 은 admin.access 다. 실제 판정은 post.moderate ·
	// comment.moderate 를 **그 게시판에** 물어 핸들러가 한다 — 스코프 권한은
	// 경로만으로 판정할 수 없다 (D15 2.4).
	r.Add(Route{Screen: "A-307", Method: "GET", Pattern: "/admin/posts", Class: SC4,
		Permission: "admin.access", Handler: ad.PostList})
	r.Add(Route{Screen: "A-307", Method: "POST", Pattern: "/admin/posts", Class: SC5,
		Permission: "admin.access", Handler: ad.PostModerate})
	r.Add(Route{Screen: "A-308", Method: "GET", Pattern: "/admin/comments", Class: SC4,
		Permission: "admin.access", Handler: ad.CommentList})
	r.Add(Route{Screen: "A-308", Method: "POST", Pattern: "/admin/comments", Class: SC5,
		Permission: "admin.access", Handler: ad.CommentModerate})

	// A-309 는 SC-7 이다. 판정은 첨부 id 가 아니라 부모 글의 게시판에 건다 —
	// id 는 그 파일이 어디 있는지 말하지 않는다 (D15 8절 1번과 같은 이유).
	r.Add(Route{Screen: "A-309", Method: "GET", Pattern: "/admin/attachments", Class: SC7,
		Permission: "admin.access", Handler: ad.AttachmentList})
	r.Add(Route{Screen: "A-309", Method: "POST", Pattern: "/admin/attachments", Class: SC7,
		Permission: "admin.access", Handler: ad.AttachmentDelete})

	r.Add(Route{Screen: "A-601", Method: "GET", Pattern: "/admin/oplog", Class: SC4,
		Permission: "log.view", Handler: ad.OpLogList})

	r.Add(Route{Screen: "A-602", Method: "GET", Pattern: "/admin/system", Class: SC4,
		Permission: "settings.view", Handler: ad.System})

	// P-202 is last on purpose: `/{slug}` matches anything one segment long, and
	// ServeMux prefers the more specific pattern regardless of registration
	// order — but reading it here in the order a person would guess is worth the
	// line.
	r.Add(Route{Screen: "P-202", Method: "GET", Pattern: "/{slug}", Class: SC1, Handler: pub.page})

	// ---- 커머스 (FR-710 모듈 게이팅) --------------------------------------
	//
	// **조립 시점에 정한다.** 핸들러 안에서 `if 커머스켜짐` 을 검사하지 않는다
	// (D20 「모듈 게이팅」) — 분기를 핸들러에 넣으면 새 라우트를 추가할 때마다
	// 검사를 빠뜨릴 수 있고, 빠뜨리면 커머스를 끈 사이트에 결제 경로가 열린다.
	//
	// 등록하지 않은 라우트는 404 다. 숨김이 아니다.
	if shop {
		r.Add(Route{Screen: "P-301", Method: "GET", Pattern: "/shop", Class: SC1,
			Handler: sh.productList})
		r.Add(Route{Screen: "P-305", Method: "GET", Pattern: "/shop/search", Class: SC1,
			Handler: sh.productSearch})
		r.Add(Route{Screen: "P-302", Method: "GET", Pattern: "/shop/c/{slug}", Class: SC1,
			Handler: sh.categoryList})
		r.Add(Route{Screen: "P-303", Method: "GET", Pattern: "/shop/p/{slug}", Class: SC1,
			Handler: sh.productDetail})
		r.Add(Route{Screen: "P-304", Method: "GET", Pattern: "/shop/p/{slug}/variant", Class: SC1,
			Handler: sh.variantPick})

		r.Add(Route{Screen: "P-402", Method: "GET", Pattern: "/cart", Class: SC1,
			Handler: sh.cartView})
		r.Add(Route{Screen: "P-401", Method: "POST", Pattern: "/cart/items", Class: SC2,
			Handler: sh.cartAdd})
		// SC-3: 소유자는 세션이 정한다. 경로의 id 는 항목을 가리킬 뿐 주인을
		// 가리키지 않으며, 저장소의 WHERE 절이 그것을 잠근다.
		r.Add(Route{Screen: "P-403", Method: "PATCH", Pattern: "/cart/items/{id}", Class: SC3,
			Handler: sh.cartUpdate})
		r.Add(Route{Screen: "P-404", Method: "DELETE", Pattern: "/cart/items/{id}", Class: SC3,
			Handler: sh.cartUpdate})

		// SC-6: 돈이 움직이는 화면이다. 무엇을 결제할지는 **세션**이 정하고
		// (sessPendingOrder), 콜백의 orderId 는 대조에만 쓴다 — 조회 키로
		// 쓰면 남의 주문번호로 남의 주문을 승인시킬 수 있다 (D19 P-408).
		// 읽기 라우트는 SC-4 다 (D15 4.4). 폼을 그리는 것은 상태를 바꾸지
		// 않으므로 P5 가 적용되고, 그 규칙과 D11 의 화면 유형을 함께
		// 만족시키는 조합이 이것 하나다.
		r.Add(Route{Screen: "P-405", Method: "GET", Pattern: "/checkout", Class: SC4,
			Handler: sh.checkoutForm})
		r.Add(Route{Screen: "P-406", Method: "POST", Pattern: "/checkout", Class: SC6,
			Handler: sh.checkoutCreate})
		r.Add(Route{Screen: "P-407", Method: "GET", Pattern: "/checkout/pay", Class: SC4,
			Handler: sh.checkoutPay})
		// **P5 예외다.** PG 가 브라우저를 GET 으로 돌려보내고 그 GET 이
		// 승인을 일으킨다 — 결제 프로토콜이 정한 것이고 우리가 고를 수
		// 있는 것이 아니다. 방어는 다른 곳에 있다 (아래 사유).
		r.Add(Route{Screen: "P-408", Method: "GET", Pattern: "/checkout/success", Class: SC6,
			UnsafeGETReason: "PG successUrl 은 GET 리다이렉트다. 무엇을 승인할지는 " +
				"세션이 정하고 콜백 값은 대조에만 쓰이며, DB 유니크가 재실행을 막는다",
			Handler: sh.checkoutSuccess})
		r.Add(Route{Screen: "P-409", Method: "GET", Pattern: "/checkout/fail", Class: SC4,
			Handler: sh.checkoutFail})
		r.Add(Route{Screen: "P-410", Method: "GET", Pattern: "/checkout/complete", Class: SC3,
			Handler: sh.checkoutComplete})

		// ---- 주문 조회 (SC-3: 소유자는 세션이 정한다) ----------------------
		r.Add(Route{Screen: "P-501", Method: "GET", Pattern: "/orders", Class: SC3,
			Handler: sh.orderList})
		// 비회원 조회 폼·실행이 /orders/{orderNo} 보다 **먼저** 등록되지만,
		// ServeMux 는 더 구체적인 패턴을 고르므로 순서가 아니라 구체성이
		// 정한다. `guest` 는 리터럴이라 `{orderNo}` 를 이긴다.
		r.Add(Route{Screen: "P-503", Method: "GET", Pattern: "/orders/guest", Class: SC2,
			Handler: sh.guestLookupForm})
		r.Add(Route{Screen: "P-504", Method: "POST", Pattern: "/orders/guest", Class: SC2,
			Handler: sh.guestLookup})
		r.Add(Route{Screen: "P-502", Method: "GET", Pattern: "/orders/{orderNo}", Class: SC3,
			Handler: sh.orderDetail})
		r.Add(Route{Screen: "P-505", Method: "GET", Pattern: "/orders/{orderNo}/shipping", Class: SC3,
			Handler: sh.orderShipping})
		// SC-6: 돈이 움직인다. 취소는 배송 전이라 구매자가 직접 일으키고
		// (D14 5-1), 부분 환불은 요청 행만 만든다 — 승인은 A-507 이다.
		r.Add(Route{Screen: "P-506", Method: "POST", Pattern: "/orders/{orderNo}/cancel", Class: SC6,
			Handler: sh.orderCancel})
		r.Add(Route{Screen: "P-507", Method: "POST", Pattern: "/orders/{orderNo}/refund", Class: SC6,
			Handler: sh.refundRequest})
		r.Add(Route{Screen: "P-508", Method: "GET", Pattern: "/orders/{orderNo}/refunds", Class: SC3,
			Handler: sh.refundStatus})
		r.Add(Route{Screen: "P-509", Method: "GET", Pattern: "/orders/{orderNo}/receipt", Class: SC3,
			Handler: sh.orderReceipt})
		r.Add(Route{Screen: "P-510", Method: "POST", Pattern: "/orders/{orderNo}/confirm", Class: SC3,
			Handler: sh.orderConfirm})
		// 반품·교환. 경로가 종류를 정한다 — 폼이 hidden 으로 실으면 반품 URL
		// 로 교환을 접수하는 요청이 성립한다. 읽기 라우트는 SC-4 다 (D15 4.4).
		r.Add(Route{Screen: "P-511", Method: "GET", Pattern: "/orders/{orderNo}/return", Class: SC4,
			Handler: sh.returnForm})
		r.Add(Route{Screen: "P-511", Method: "POST", Pattern: "/orders/{orderNo}/return", Class: SC6,
			Handler: sh.returnCreate})
		r.Add(Route{Screen: "P-512", Method: "GET", Pattern: "/orders/{orderNo}/exchange", Class: SC4,
			Handler: sh.returnFormExchange})
		r.Add(Route{Screen: "P-512", Method: "POST", Pattern: "/orders/{orderNo}/exchange", Class: SC6,
			Handler: sh.returnCreateExchange})
		r.Add(Route{Screen: "P-513", Method: "GET", Pattern: "/orders/{orderNo}/returns", Class: SC3,
			Handler: sh.returnList})
		// P-514 교환 차액. **차액이 양수인 교환에만 존재한다** — 물건은 이미
		// 수거됐으므로 이 결제가 실패해도 되돌릴 것이 없다 (D19 P-514).
		r.Add(Route{Screen: "P-514", Method: "GET",
			Pattern: "/orders/{orderNo}/exchange/{returnNo}/pay", Class: SC4,
			Handler: sh.exchangePayForm})
		r.Add(Route{Screen: "P-514", Method: "POST",
			Pattern: "/orders/{orderNo}/exchange/{returnNo}/pay", Class: SC6,
			Handler: sh.exchangePayConfirm})

		// ---- 관리자 커머스 (A-5xx) -----------------------------------------
		r.Add(Route{Screen: "A-501", Method: "GET", Pattern: "/admin/products", Class: SC4,
			Permission: "product.view", Handler: ad.ProductList})
		// A-502 상품 편집 · A-503 옵션·재고 편집기. 권한은 **단일**이다 —
		// D15 2.2 에 product.create/delete 가 없다 (D19 A-502).
		r.Add(Route{Screen: "A-502", Method: "GET", Pattern: "/admin/products/{id}", Class: SC4,
			Permission: "product.manage", Handler: ad.ProductForm})
		r.Add(Route{Screen: "A-502", Method: "POST", Pattern: "/admin/products/{id}", Class: SC7,
			Permission: "product.manage", Handler: ad.ProductSave})
		r.Add(Route{Screen: "A-502", Method: "POST", Pattern: "/admin/products/{id}/delete", Class: SC7,
			Permission: "product.manage", Handler: ad.ProductDelete})
		r.Add(Route{Screen: "A-503", Method: "GET", Pattern: "/admin/products/{id}/variants", Class: SC4,
			Permission: "product.manage", Handler: ad.VariantForm})
		r.Add(Route{Screen: "A-503", Method: "POST", Pattern: "/admin/products/{id}/variants", Class: SC5,
			Permission: "product.manage", Handler: ad.VariantSave})
		r.Add(Route{Screen: "A-509", Method: "GET", Pattern: "/admin/categories", Class: SC4,
			Permission: "product.manage", Handler: ad.CategoryList})
		r.Add(Route{Screen: "A-509", Method: "POST", Pattern: "/admin/categories", Class: SC5,
			Permission: "product.manage", Handler: ad.CategoryReparent})
		r.Add(Route{Screen: "A-504", Method: "GET", Pattern: "/admin/orders", Class: SC4,
			Permission: "order.view", Handler: ad.OrderList})
		r.Add(Route{Screen: "A-505", Method: "GET", Pattern: "/admin/orders/{no}", Class: SC4,
			Permission: "order.view", Handler: ad.OrderDetail})
		r.Add(Route{Screen: "A-506", Method: "POST", Pattern: "/admin/orders/{no}/transition", Class: SC5,
			Permission: "order.update", Handler: ad.OrderTransition})
		r.Add(Route{Screen: "A-510", Method: "GET", Pattern: "/admin/orders/{no}/shipping", Class: SC4,
			Permission: "order.update", Handler: ad.ShippingForm})
		r.Add(Route{Screen: "A-510", Method: "POST", Pattern: "/admin/orders/{no}/shipping", Class: SC5,
			Permission: "order.update", Handler: ad.ShippingSave})
		// SC-6: 돈이 나간다. 읽기 라우트는 SC-4 다 (D15 4.4).
		r.Add(Route{Screen: "A-507", Method: "GET", Pattern: "/admin/orders/{no}/refund", Class: SC4,
			Permission: "order.refund", Handler: ad.RefundForm})
		r.Add(Route{Screen: "A-507", Method: "POST", Pattern: "/admin/orders/{no}/refund", Class: SC6,
			Permission: "order.refund", Handler: ad.RefundSave})
		r.Add(Route{Screen: "A-511", Method: "GET", Pattern: "/admin/orders/{no}/returns", Class: SC4,
			Permission: "order.return", Handler: ad.ReturnList})
		r.Add(Route{Screen: "A-511", Method: "POST", Pattern: "/admin/orders/{no}/returns", Class: SC6,
			Permission: "order.return", Handler: ad.ReturnAction})

		// A-514·A-515·A-516·A-517 QR 재고. **QR 은 재고 모델이 아니라 재고
		// 조작의 입력 수단이다** (D13) — 재고는 여전히 정수 하나이고 delta 로만
		// 움직인다. A-517 만 조회라 product.view 다.
		r.Add(Route{Screen: "A-514", Method: "GET", Pattern: "/admin/scan/receive", Class: SC4,
			Permission: "product.manage", Handler: ad.ScanReceive})
		r.Add(Route{Screen: "A-514", Method: "POST", Pattern: "/admin/scan/receive", Class: SC5,
			Permission: "product.manage", Handler: ad.ScanReceive})
		r.Add(Route{Screen: "A-515", Method: "GET", Pattern: "/admin/scan/stocktake", Class: SC4,
			Permission: "product.manage", Handler: ad.Stocktake})
		r.Add(Route{Screen: "A-515", Method: "POST", Pattern: "/admin/scan/stocktake", Class: SC5,
			Permission: "product.manage", Handler: ad.Stocktake})
		r.Add(Route{Screen: "A-516", Method: "GET", Pattern: "/admin/orders/{no}/pick", Class: SC4,
			Permission: "order.update", Handler: ad.PickCheck})
		r.Add(Route{Screen: "A-516", Method: "POST", Pattern: "/admin/orders/{no}/pick", Class: SC5,
			Permission: "order.update", Handler: ad.PickCheck})
		r.Add(Route{Screen: "A-517", Method: "GET", Pattern: "/admin/scan/lookup", Class: SC4,
			Permission: "product.view", Handler: ad.ScanLookup})
		// A-513 QR 라벨. **상태를 바꾸지 않으므로 GET 만 있다** (FR-620).
		r.Add(Route{Screen: "A-513", Method: "GET", Pattern: "/admin/products/{id}/labels", Class: SC4,
			Permission: "product.view", Handler: ad.QRLabel})

		// A-508 결제 대사 · A-603 웹훅 이력. **둘 다 필요하다** — 대사는 PG
		// 조회와의 대조이고, 수신 이력은 "웹훅이 오긴 왔는가" 다 (D13 A-603).
		r.Add(Route{Screen: "A-508", Method: "GET", Pattern: "/admin/reconcile", Class: SC4,
			Permission: "payment.view", Handler: ad.Reconcile})
		r.Add(Route{Screen: "A-603", Method: "GET", Pattern: "/admin/webhooks", Class: SC4,
			Permission: "payment.view", Handler: ad.WebhookLog})

		// A-207 약관 · A-208 사업자 정보. 커머스 전용이다 — cms 모드에는
		// 결제 화면이 없어 받을 동의도, 표시 의무도 없다.
		r.Add(Route{Screen: "A-207", Method: "GET", Pattern: "/admin/terms", Class: SC4,
			Permission: "settings.update", Handler: ad.TermsList})
		r.Add(Route{Screen: "A-207", Method: "POST", Pattern: "/admin/terms", Class: SC5,
			Permission: "settings.update", Handler: ad.TermsAdd})
		r.Add(Route{Screen: "A-208", Method: "GET", Pattern: "/admin/business", Class: SC4,
			Permission: "settings.update", Handler: ad.BusinessForm})
		r.Add(Route{Screen: "A-208", Method: "POST", Pattern: "/admin/business", Class: SC5,
			Permission: "settings.update", Handler: ad.BusinessSave})

		// A-512 커머스 정책. **정책만 정하고 건별 금액은 받지 않는다** —
		// 건별을 여기서 받으면 정책과 실행이 섞이고 과거 건이 소급 변경된다
		// (D19 A-512 받지 않는 필드).
		r.Add(Route{Screen: "A-512", Method: "GET", Pattern: "/admin/commerce/policy", Class: SC4,
			Permission: "settings.update", Handler: ad.PolicyForm})
		r.Add(Route{Screen: "A-512", Method: "POST", Pattern: "/admin/commerce/policy", Class: SC5,
			Permission: "settings.update", Handler: ad.PolicySave})
	}

	return r
}
