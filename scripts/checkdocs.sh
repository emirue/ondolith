#!/bin/sh
# Enforces the documentation rules in docs/90-conventions.md.
#
# Rules that live only in prose drift. Everything mechanically checkable is
# checked here and wired into `make check`, so breaking a rule breaks the build.
#
# No `set -e`: this script's job is to collect every violation and report them
# together, which is the opposite of aborting on the first non-zero status.
set -u

cd "$(dirname "$0")/.." || exit 1

# 중간 산출물은 실행마다 새 디렉터리에 둔다. 고정된 /tmp 이름을 쓰면 이 스크립트를
# 두 번 동시에 돌릴 때 — selftest 가 배경에서 도는 동안 make check 를 돌리는 것이
# 그렇다 — 서로의 파일을 덮어쓰고, 고치려야 고칠 수 없는 위반이 보고된다.
T=$(mktemp -d) || exit 1
trap 'rm -rf "$T"' EXIT INT TERM

fail=0
mark=0 # violations recorded by the check currently running

err() {
	printf '  ✗ %s\n' "$*"
	fail=1
	mark=1
}
begin() { mark=0; }
# done_ <ok-message>: print it only if the current check recorded nothing.
done_() { [ "$mark" -eq 0 ] && printf '  ✓ %s\n' "$*"; return 0; }

md_files() { find . -name '*.md' -not -path './.git/*'; }

# IDs may be cited from docs, code and migrations alike.
id_files() { find . \( -name '*.md' -o -name '*.go' -o -name '*.sql' \) -not -path './.git/*'; }

# Collect every ID matching a pattern, across everything that may cite one.
used() { id_files | xargs cat 2>/dev/null | perl -nle "while (/$1/g) { print \$1 }" | sort -u; }

echo "checkdocs"

# --- 1. relative links resolve -----------------------------------------------
begin
n=0
for f in $(md_files); do
	d=$(dirname "$f")
	for link in $(perl -nle 'while (/\]\(([^)]+)\)/g) { print $1 }' "$f"); do
		case "$link" in http* | \#*) continue ;; esac
		target=${link%%#*}
		[ -z "$target" ] && continue
		[ -e "$d/$target" ] || err "깨진 링크: $f → $link"
		n=$((n + 1))
	done
done
done_ "상대 링크 $n 개 전부 해석됨"

# --- 2 & 3. FR/NFR defined exactly once, every citation resolves --------------
# A definition is an ID in the FIRST cell of a table row. References always
# embed the ID in prose, so a reference is never mistaken for a definition
# (docs/90-conventions.md 5절).
begin
REQ=docs/10-requirements.md
perl -nle 'print $1 if /^\| ((?:N?FR)-\d{3}) /' "$REQ" | sort >"$T"/cd_def_req
sort -u "$T"/cd_def_req >"$T"/cd_def_req_u
for id in $(uniq -d "$T"/cd_def_req); do err "요구사항 중복 정의: $id"; done
# Every requirement's last column is its acceptance criterion. An empty one is
# a requirement nobody can ever declare done (docs/90-conventions.md 6절).
perl -nle 'next unless /^\|\s*((?:N?FR)-\d{3})\s*\|(.*)$/;
	my ($id, $rest) = ($1, $2);
	my @c = split /\|/, $rest, -1;
	pop @c if @c && $c[-1] =~ /^\s*$/;
	my $last = @c ? $c[-1] : "";
	$last =~ s/\s//g;
	print $id if $last eq "";' "$REQ" >"$T"/cd_req_nocrit
while read -r id; do
	[ -n "$id" ] && err "$REQ: 완료 기준이 비어 있다: $id"
done <"$T"/cd_req_nocrit

# A criterion phrased as a deliberation ("…인지 결론", "…할지 검토") is not a
# criterion — it is an open decision wearing a requirement's clothes, and it can
# never be judged done. Those belong in D18. The empty-cell check above does not
# catch them because the cell is full.
perl -nle 'next unless /^\|\s*((?:N?FR)-\d{3})\s*\|(.*)$/;
	my ($id, $rest) = ($1, $2);
	my @c = split /\|/, $rest, -1;
	pop @c if @c && $c[-1] =~ /^\s*$/;
	my $crit = @c ? $c[-1] : "";
	next if $crit =~ /폐기/;
	print $id if $crit =~ /인지 결론|할지 검토|여부를 검토|결론을 낸다|판단한다$/;' "$REQ" >"$T"/cd_req_vague
while read -r id; do
	[ -n "$id" ] && err "$REQ: 완료 기준이 심의 형태다 (미결이지 기준이 아니다): $id — D18로 옮길 것"
done <"$T"/cd_req_vague

used '\b((?:N?FR)-\d{3})\b' >"$T"/cd_use_req
for id in $(comm -23 "$T"/cd_use_req "$T"/cd_def_req_u); do
	err "$REQ 에 정의되지 않은 요구사항 인용: $id"
done
done_ "요구사항 $(wc -l <"$T"/cd_def_req_u | tr -d ' ') 개 정의 · 인용 해석 · 완료 기준 전부 채워짐"

# --- 4. DEC- citations resolve -----------------------------------------------
begin
DEC=.ai/DECISIONS.md
perl -nle 'print $1 if /^#{2,3} (DEC-\d+(?:\.\d+)?)\b/' "$DEC" | sort -u >"$T"/cd_def_dec
used '\b(DEC-\d+(?:\.\d+)?)\b' >"$T"/cd_use_dec
for id in $(comm -23 "$T"/cd_use_dec "$T"/cd_def_dec); do
	err "$DEC 에 정의되지 않은 결정 인용: $id"
done
done_ "결정 $(wc -l <"$T"/cd_def_dec | tr -d ' ') 개 정의 · 인용 전부 해석됨"

# --- 5. M# citations resolve -------------------------------------------------
begin
MIS=.ai/MISTAKES.md
perl -nle 'print $1 if /^## (M\d+)\b/' "$MIS" | sort -u >"$T"/cd_def_mis
used '\b(M\d+)\b' >"$T"/cd_use_mis
for id in $(comm -23 "$T"/cd_use_mis "$T"/cd_def_mis); do
	err "$MIS 에 정의되지 않은 실수 인용: $id"
done
done_ "실수 기록 인용 전부 해석됨"

# --- 6..9. docs/ naming, index registration, H1 matches filename -------------
begin
INDEX=docs/README.md
for f in docs/*.md; do
	b=$(basename "$f")
	[ "$b" = "README.md" ] && continue

	echo "$b" | grep -qE '^[0-9]{2}-[a-z0-9-]+\.md$' ||
		err "파일명 규칙 위반 (NN-kebab-case.md): $b"

	grep -q "($b)" "$INDEX" || err "인덱스 미등록: $b"

	nn=$(echo "$b" | cut -c1-2)
	head -1 "$f" | grep -qE "^# D$nn\. " ||
		err "H1이 '# D$nn. '로 시작하지 않음: $f"
done
for f in $(perl -nle 'while (/\]\((\d{2}-[a-z0-9-]+\.md)\)/g) { print $1 }' "$INDEX" | sort -u); do
	[ -f "docs/$f" ] || err "인덱스가 없는 파일을 가리킴: docs/$f"
done
done_ "docs/ 파일명·인덱스 등록·H1 ID 일치"

# --- 10. the old, colliding decision-ID format stays gone --------------------
# `D3.1` / bare `D0`..`D4` used to mean decisions and collided with the doc IDs
# D00..D90. The prefix was split to DEC-; do not let the old form come back.
#
# Prose that must name the banned form in order to explain it (the conventions
# doc, changelog entries, the mistake record) opts out per line with the marker
# `checkdocs:allow-legacy-id`. Per line, not per file, so the exemption cannot
# quietly grow to cover a whole document.
begin
md_files |
	xargs perl -nle 'close ARGV if eof;
		next if /checkdocs:allow-legacy-id/;
		print "$ARGV:$.: $&" if /\bD\d\.\d+\b|\bD[0-4]\b(?![\d])/' 2>/dev/null >"$T"/cd_stale
while read -r line; do
	err "옛 결정 ID 형식 (DEC- 를 쓸 것): $line"
done <"$T"/cd_stale
done_ "옛 결정 ID 형식 없음"

# --- 11..14. screen inventory ------------------------------------------------
# Screens are defined once, in D11, as table rows. `접근` and `상태변경` are
# closed vocabularies so that "who can reach this screen, and can it change
# state?" is answerable by grep instead of by reading handlers
# (docs/90-conventions.md 6-1절).
begin
SCR=docs/11-screens.md
if [ ! -f "$SCR" ]; then
	err "화면 인벤토리가 없다: $SCR"
else
	perl -nle 'print $1 if /^\|\s*([PA]-\d{3})\s*\|/' "$SCR" | sort >"$T"/cd_def_scr
	sort -u "$T"/cd_def_scr >"$T"/cd_def_scr_u
	for id in $(uniq -d "$T"/cd_def_scr); do err "화면 중복 정의: $id"; done

	used '\b([PA]-\d{3})\b' >"$T"/cd_use_scr
	for id in $(comm -23 "$T"/cd_use_scr "$T"/cd_def_scr_u); do
		err "$SCR 에 정의되지 않은 화면 인용: $id"
	done

	# Closed vocabularies. Screens are locked with permissions, not roles: roles
	# are data an installation can add, so gating on one would mean editing our
	# code every time (docs/15-access-control.md P1, P2).
	perl -nle 'next unless /^\|\s*([PA]-\d{3})\s*\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|/;
		($id, $acc, $mut, $cls) = ($1, $5, $6, $7);
		for ($acc, $mut, $cls) { s/^\s+//; s/\s+$// }
		print "접근 값이 허용 목록에 없다 ($id): [$acc]"     unless $acc =~ /^(공개|로그인|본인|권한:[a-z][a-z0-9]*\.[a-z][a-z0-9_]*)$/;
		print "상태변경 값이 허용 목록에 없다 ($id): [$mut]"  unless $mut =~ /^(있음|없음)$/;
		print "화면 유형이 SC-1..SC-8 이 아니다 ($id): [$cls]" unless $cls =~ /^SC-[1-8]$/;
		# An admin screen reachable without a permission is the accident this
		# whole table exists to prevent.
		print "관리자 화면인데 접근이 권한이 아니다 ($id): [$acc]" if $id =~ /^A-/ && $acc !~ /^권한:/;
		print "STATEFUL $id" if $mut eq "있음";
	' "$SCR" >"$T"/cd_scr_issues

	grep -v '^STATEFUL ' "$T"/cd_scr_issues >"$T"/cd_scr_bad
	while read -r line; do
		[ -n "$line" ] && err "$line"
	done <"$T"/cd_scr_bad

	# A state-changing screen must carry a detail section somewhere in docs/ —
	# these are the ones that can be abused, so they cannot be a bare table row.
	# The heading may cover several related screens, so the ID only has to
	# appear in it.
	grep '^STATEFUL ' "$T"/cd_scr_issues | cut -d' ' -f2 >"$T"/cd_scr_stateful
	while read -r id; do
		[ -z "$id" ] && continue
		grep -qhE "^#{2,4} .*$id" docs/*.md ||
			err "상태변경 화면인데 상세 절이 없다: $id (제목에 $id 를 포함할 것)"
	done <"$T"/cd_scr_stateful

	n_scr=$(wc -l <"$T"/cd_def_scr_u | tr -d ' ')
	n_state=$(wc -l <"$T"/cd_scr_stateful | tr -d ' ')
	done_ "화면 $n_scr 개 정의 (상태변경 $n_state 개) · 인용·어휘·상세 절 전부 확인"
fi

# --- 14-1. every mandatory FR is realised by a screen ------------------------
# The gap this closes: a feature gets a requirement and a public screen that
# consumes its data, but nothing that creates it. Exceptions are declared in
# D11 rather than hidden here, so adding one is visible in review.
begin
if [ -f "$SCR" ]; then
	# Cited = appears in the 관련 FR column of a screen row.
	perl -nle 'next unless /^\|\s*[PA]-\d{3}\s*\|.*\|([^|]*)\|\s*$/;
		my $c = $1;
		while ($c =~ /\b(FR-\d{3})\b/g) { print $1 }' "$SCR" | sort -u >"$T"/cd_fr_cited
	# Declared exceptions = rows of the "화면이 없는 필수 요구사항" table.
	perl -nle 'if (/^## 화면이 없는 필수 요구사항/) { $in = 1; next }
		if ($in && /^## /) { $in = 0 }
		next unless $in;
		print $1 if /^\|\s*(FR-\d{3})\s*\|/' "$SCR" | sort -u >"$T"/cd_fr_exempt
	sort -u "$T"/cd_fr_cited "$T"/cd_fr_exempt >"$T"/cd_fr_ok

	perl -nle 'print $1 if /^\|\s*(FR-\d{3})\s*\|[^|]*\|\s*필수\s*\|/' "$REQ" | sort -u >"$T"/cd_fr_must
	for fr in $(comm -23 "$T"/cd_fr_must "$T"/cd_fr_ok); do
		err "필수 요구사항을 실현하는 화면이 없다: $fr (화면을 추가하거나 $SCR 의 예외 표에 이유를 적을 것)"
	done
	done_ "필수 FR $(wc -l <"$T"/cd_fr_must | tr -d ' ') 건 · 화면 대응 또는 예외 선언 확인"
fi

# --- 14-2. every table has a screen that writes it and one that shows it -----
# The gap this closes is the one that actually happened six times: a table and
# a screen that reads it, with nothing that creates it (.ai/MISTAKES.md M8).
# Deliberate absences are written as "(사유)" so they are visible, not blank.
begin
COV=docs/16-data-coverage.md
if [ ! -f "$COV" ]; then
	err "데이터 커버리지 표가 없다: $COV"
else
	# D30 3-3 is the single source for table names. Reading them out of the
	# schema prose instead would tie this check to how that prose is formatted —
	# it already broke once when the schema moved from code fences to tables.
	perl -nle 'if (/^### 3-3\./) { $in = 1; next }
		if ($in && /^#{2,3} /) { $in = 0 }
		next unless $in && /^\| Phase /;
		print $1 while /`([a-z][a-z_]{2,})`/g;' docs/30-data-model.md | sort -u >"$T"/cd_tbl_d30
	[ -s "$T"/cd_tbl_d30 ] || err "docs/30-data-model.md 3-3 테이블 목록을 읽지 못했다 (검사가 헛돌았다)"

	perl -nle 'print $1 if /^\|\s*`([a-z][a-z_]{2,})`\s*\|/' "$COV" | sort -u >"$T"/cd_tbl_cov
	for t in $(comm -23 "$T"/cd_tbl_d30 "$T"/cd_tbl_cov); do
		err "$COV 에 없는 테이블: $t (만드는 화면·보여주는 화면을 적을 것)"
	done
	for t in $(comm -13 "$T"/cd_tbl_d30 "$T"/cd_tbl_cov); do
		err "$COV 가 D30 에 없는 테이블을 적었다: $t"
	done

	# Neither producer nor consumer may be blank.
	perl -nle 'next unless /^\|\s*`([a-z][a-z_]{2,})`\s*\|([^|]*)\|([^|]*)\|/;
		my ($t, $make, $show) = ($1, $2, $3);
		for ($make, $show) { s/^\s+//; s/\s+$// }
		print "만드는 화면이 비어 있다: $t"   if $make eq "";
		print "보여주는 화면이 비어 있다: $t" if $show eq "";' "$COV" >"$T"/cd_cov_blank
	while read -r line; do
		[ -n "$line" ] && err "$COV: $line"
	done <"$T"/cd_cov_blank

	done_ "테이블 $(wc -l <"$T"/cd_tbl_cov | tr -d ' ') 종 · 생산·소비 화면 전부 명시"
fi

# --- 14-3. every screen belongs to exactly one module ------------------------
# FR-710 lets an installation run as a plain site with the commerce routes not
# registered at all. That only holds if every screen is unambiguously in one
# module or the other — a screen nobody classified would be registered by
# whichever branch happens to touch it.
begin
if [ -f "$SCR" ]; then
	perl -nle 'print $1 if /^\|\s*([PA]-\d{3})\s*\|/' "$SCR" | sort -u >"$T"/cd_mod_all
	# Commerce = the bands D11 declares, plus P-905 (the declared exception).
	grep -E '^(P-[345]|A-5|P-905)' "$T"/cd_mod_all | sort -u >"$T"/cd_mod_shop
	comm -23 "$T"/cd_mod_all "$T"/cd_mod_shop >"$T"/cd_mod_core
	# A screen in neither list would mean the band regex and the ID set disagree.
	n_all=$(wc -l <"$T"/cd_mod_all | tr -d ' ')
	n_shop=$(wc -l <"$T"/cd_mod_shop | tr -d ' ')
	n_core=$(wc -l <"$T"/cd_mod_core | tr -d ' ')
	if [ $((n_shop + n_core)) -ne "$n_all" ]; then
		err "모듈 분류가 화면 총수와 맞지 않는다: 핵심 $n_core + 커머스 $n_shop != 전체 $n_all"
	fi
	# D11 must actually declare the module split, or the rule lives only here.
	grep -q '^## 모듈 구성' "$SCR" ||
		err "$SCR 에 '## 모듈 구성' 절이 없다 (FR-710)"
	grep -q 'P-905' "$SCR" ||
		err "$SCR 이 P-905 의 모듈 예외를 적지 않았다"
	done_ "모듈 분류 — 핵심 $n_core · 커머스 $n_shop (FR-710)"
fi

# --- 14-4. every HTML-rendering public screen has a template -----------------
# FR-308: the theme contract is only usable if the list of template names is
# complete. A screen with no template would silently fall through to whatever
# the core happens to render.
begin
THEME=docs/17-theme-contract.md
if [ ! -f "$THEME" ]; then
	err "테마 계약 문서가 없다: $THEME"
else
	# Public screens that answer GET are the candidates for a theme template.
	#
	# $1 and $4 are copied out first: evaluating another match in the condition
	# resets the capture variables, which silently emptied this list once.
	perl -nle 'next unless /^\|\s*(P-\d{3})\s*\|([^|]*)\|([^|]*)\|([^|]*)\|/;
		my ($id, $methods) = ($1, $4);
		print $id if $methods =~ /GET/;' "$SCR" | sort -u >"$T"/cd_tpl_get
	# Screens named in the template table, and screens declared template-less.
	perl -nle 'print $1 while /\b(P-\d{3})\b/g' "$THEME" | sort -u >"$T"/cd_tpl_named
	for id in $(comm -23 "$T"/cd_tpl_get "$T"/cd_tpl_named); do
		err "$THEME 에 템플릿도 예외도 없는 화면: $id (FR-308)"
	done
	for id in $(comm -13 "$T"/cd_tpl_get "$T"/cd_tpl_named); do
		err "$THEME 이 GET 없는(또는 없는) 화면에 템플릿을 배정했다: $id"
	done
	# The slash must be escaped: an unescaped one ends the m// delimiter and the
	# pattern silently matches nothing.
	n_tpl=$(perl -nle 'print $1 if /^\|\s*`([a-z][a-z0-9\/._-]*\.(?:html|xml|txt))`\s*\|/' "$THEME" |
		sort -u | wc -l | tr -d ' ')
	[ "$n_tpl" -gt 0 ] || err "$THEME 에서 템플릿 이름을 하나도 읽지 못했다 (표 형식 확인)"

	# ...and every template the core actually renders is one of them, and is a
	# file the fallback theme ships.
	#
	# The contract is a promise to theme authors: override this name and your
	# markup renders. A name the core renders but D17 never listed is a screen
	# an author cannot restyle without reading the source; a listed name with no
	# builtin behind it is a file they write that is never looked at. D17 said
	# `auth/verify.html` while the code rendered verify-done.html and
	# verify-failed.html, and nothing caught it — the check above compares
	# screen IDs, not file names.
	#
	# Only the direction "code → document" is checked. The other way round
	# would fail until Phase 4, because D17 describes the finished product and
	# 25 of its templates belong to screens not yet built.
	#
	# base.html (layout, prose not table), install.html (설치 트리 — 테마가
	# 아니다), admin/ (관리자 UI, 내장 전용) and builtin/ (embed 경로) are out
	# of scope by construction. sitemap.xml·robots.txt too: D17 marks them
	# 코어 생성 and the literals that match here are URL paths, not templates.
	BUILTIN=internal/theme/builtin
	perl -nle 'print $1 if /^\|\s*`([a-z][a-z0-9\/._-]*\.(?:html|xml|txt))`\s*\|/' "$THEME" |
		sort -u >"$T"/cd_tpl_names
	drift=0
	for t in $(find internal -name '*.go' ! -name '*_test.go' -print0 |
			xargs -0 grep -hoE '"[a-z][a-z0-9_-]*(/[a-z0-9._-]+)*\.(html|xml|txt)"' |
			tr -d '"' | sort -u |
			grep -Ev '^(admin|templates|builtin)/|^(base|install)\.html$' |
			grep -Ev '^(sitemap\.xml|robots\.txt)$'); do
		grep -qxF "$t" "$T"/cd_tpl_names ||
			{ err "코어가 그리는 템플릿이 $THEME 에 없다: $t (테마가 갈아끼울 수 없다)"; drift=1; }
		[ -f "$BUILTIN/$t" ] ||
			{ err "코어가 그리는 템플릿이 폴백 테마에 없다: $BUILTIN/$t"; drift=1; }
	done
	[ "$drift" -eq 0 ] && done_ "코어가 그리는 테마 템플릿이 전부 $THEME 에 있고 $BUILTIN/ 에 존재한다 (FR-308)"
	done_ "테마 템플릿 $n_tpl 종 · GET 공개 화면 $(wc -l <"$T"/cd_tpl_get | tr -d ' ') 개 전부 배정 또는 예외 (FR-308)"
fi

# --- 14-4b. no document contains a verbatim copy of itself ------------------
# docs/50-commerce.md carried THREE stale copies of its own 153-line middle,
# and the copies still said "아직 정하지 않은 것" about decisions W3-02 had
# closed. Every other check passed: the links resolved, the IDs existed, the
# headings were well-formed. A reader landing on the wrong copy would have
# implemented against a superseded document.
#
# 20 lines is the window. Shorter runs repeat legitimately — table headers,
# rule preambles, the `|---|---|` separators — and a rule that fires on those
# would be turned off within a week.
begin
dupes=0
for f in docs/*.md .ai/*.md; do
	[ -f "$f" ] || continue
	# Blank and separator-only lines are dropped before windowing: a run of
	# them is not duplicated prose, and keeping them lets two unrelated tables
	# look identical.
	d=$(grep -vE '^\s*$|^\|[-| :]+\|$' "$f" |
		perl -e 'my @l = <STDIN>; my %seen;
			for my $i (0 .. $#l - 19) {
				my $w = join "", @l[$i .. $i + 19];
				print "dup\n" and last if $seen{$w}++;
			}')
	if [ -n "$d" ]; then
		err "$f 안에 20줄 이상이 그대로 복제돼 있다 — 낡은 사본이 남았을 수 있다"
		dupes=1
	fi
done
[ "$dupes" -eq 0 ] && done_ "문서 안에 통째로 복제된 블록 없음"

# --- 14-5. open decisions live in one ledger --------------------------------
# Per-document tables drifted: six items stayed listed after being decided, and
# one item appeared in four documents at once (.ai/MISTAKES.md M9). Whoever
# makes a decision only edits the document they were working in.
begin
LEDGER=docs/18-open-decisions.md
if [ ! -f "$LEDGER" ]; then
	err "미결 대장이 없다: $LEDGER"
else
	for f in docs/*.md; do
		[ "$f" = "$LEDGER" ] && continue
		# A section heading alone is fine (it points at the ledger); table rows
		# under it are not.
		# Any non-separator table row under the heading means a table is back;
		# a pointer-only section has none.
		rows=$(awk '/^#{2,3} 아직 정하지 않은 것/{f=1;next} f&&/^#{1,3} /{f=0} f&&/^\| [^-|]/' "$f" | wc -l | tr -d ' ')
		[ "${rows:-0}" -gt 0 ] &&
			err "$f 에 미결 표가 있다 ($rows 행) — $LEDGER 로 옮길 것"
	done
	# An inline `- **미결**:` note in a screen detail is allowed (D18 rule 3),
	# but it must name the ledger entry it belongs to. Without that link the two
	# drift the same way per-document tables did: an item gets decided, the
	# ledger row is deleted, and the prose keeps saying "not decided yet".
	# This recurred after six items were closed (.ai/MISTAKES.md M9, M11).
	perl -nle 'print "$ARGV:$.: $_" if /^- \*\*미결\*\*/ && !/OPEN-\d{2}/' docs/*.md >"$T"/cd_open_inline
	while read -r line; do
		[ -n "$line" ] && err "인라인 미결이 대장 항목을 인용하지 않았다 (OPEN-## 를 적을 것): $line"
	done <"$T"/cd_open_inline

	perl -nle 'print $1 if /^- \*\*미결\*\*.*\b(OPEN-\d{2})\b/' docs/*.md | sort -u >"$T"/cd_open_cited
	perl -nle 'print $1 if /^\|\s*(OPEN-\d{2})\s*\|/' "$LEDGER" | sort -u >"$T"/cd_open_def
	for id in $(comm -23 "$T"/cd_open_cited "$T"/cd_open_def); do
		err "인라인 미결이 대장에 없는 항목을 인용한다 (이미 결정된 것 아닌가): $id"
	done

	# The rule above only sees `- **미결**:` lines. Every other citation —
	# a table cell in D19, a work item in D81 — rots unnoticed when the ledger
	# row is deleted: OPEN-15 (카테고리 계층) and OPEN-38 (툴체인 승급) both
	# stayed cited for days after they were decided, pointing at nothing.
	for f in docs/*.md; do
		[ "$f" = "$LEDGER" ] && continue
		perl -nle 'print "$ARGV\t$.\t$1" while /\b(OPEN-\d{2})\b/g' "$f"
	done | sort -u >"$T"/cd_open_all
	while IFS="$(printf '\t')" read -r f ln id; do
		[ -z "$id" ] && continue
		grep -q "^$id\$" "$T"/cd_open_def ||
			err "대장에 없는 미결 번호를 인용한다 (닫히면서 인용이 남았다): $f:$ln $id"
	done <"$T"/cd_open_all

	n_open=$(perl -nle 'print $1 if /^\|\s*(OPEN-\d{2})\s*\|/' "$LEDGER" | sort -u | wc -l | tr -d ' ')
	[ "$n_open" -gt 0 ] || err "$LEDGER 에서 OPEN- 항목을 하나도 읽지 못했다"
	dupes=$(perl -nle 'print $1 if /^\|\s*(OPEN-\d{2})\s*\|/' "$LEDGER" | sort | uniq -d)
	for id in $dupes; do err "미결 ID 중복: $id"; done
	done_ "미결 $n_open 건 · 대장 한 곳에만 존재"
fi

# --- 14-6. the dependency table matches go.mod ------------------------------
# D21 states versions that were checked by querying, not remembered (M1). A
# table that drifts from go.mod is worse than no table: it is a wrong answer
# presented as a verified one.
begin
TECH=docs/21-tech-stack.md
if [ ! -f "$TECH" ]; then
	err "기술 스택 문서가 없다: $TECH"
else
	# Direct (non-indirect) requires, as go itself reports them.
	go list -m -f '{{if not .Indirect}}{{.Path}} {{.Version}}{{end}}' all 2>/dev/null |
		grep -v '^github.com/emirue/ondolith' | grep -v '^ *$' | sort >"$T"/cd_dep_mod
	# Rows whose first two cells are a backticked module and a backticked version.
	perl -nle 'print "$1 $2" if /^\|\s*`([a-z][^`]*)`\s*\|\s*`(v[^`]*)`\s*\|/' "$TECH" |
		sort >"$T"/cd_dep_doc

	for line in $(comm -23 "$T"/cd_dep_mod "$T"/cd_dep_doc | tr ' ' '@'); do
		err "$TECH 에 없거나 버전이 다른 의존성: $(echo "$line" | tr '@' ' ')"
	done
	for line in $(comm -13 "$T"/cd_dep_mod "$T"/cd_dep_doc | tr ' ' '@'); do
		err "$TECH 이 go.mod 에 없는 의존성을 적었다: $(echo "$line" | tr '@' ' ')"
	done

	# The go directive is a security boundary (D21 1절); the doc must state it.
	godir=$(perl -nle 'print $1 if /^go (\S+)/' go.mod)
	grep -q "\`$godir\`" "$TECH" ||
		err "$TECH 이 go.mod 의 최소 버전($godir)을 적지 않았다"

	done_ "의존성 $(wc -l <"$T"/cd_dep_mod | tr -d ' ') 종 · go.mod 와 $TECH 일치 · 툴체인 버전 명시"
fi

# --- 15. every screen is written up somewhere -------------------------------
# A screen that exists only as a table row was never actually designed.
begin
for id in $(cat "$T"/cd_def_scr_u 2>/dev/null); do
	case "$id" in
	P-*) detail=docs/12-screens-public.md ;;
	A-*) detail=docs/13-screens-admin.md ;;
	esac
	grep -q "$id" "$detail" 2>/dev/null || err "고아 화면 — $detail 에 언급이 없다: $id"
done
done_ "모든 화면이 상세 문서에 등장"

# --- 15-1. every state-changing screen has an input spec in D19 -------------
# D12/D13 say what a screen is for; D19 says what it accepts, what it refuses,
# and what error it returns. A screen that takes input without D19 is a form
# whose validation gets invented at implementation time — which is how an
# unbounded field or a client-trusted price gets in (docs/90-conventions.md 6-2절).
begin
IO=docs/19-screen-io.md
if [ ! -f "$IO" ]; then
	err "화면 입력·검증 명세가 없다: $IO"
else
	perl -nle 'print $1 if /^###\s+([PA]-\d{3})\b/' "$IO" | sort -u >"$T"/cd_io_spec
	for id in $(comm -23 "$T"/cd_io_spec "$T"/cd_def_scr_u); do
		err "$SCR 에 없는 화면을 $IO 가 명세한다: $id"
	done
	while read -r id; do
		[ -z "$id" ] && continue
		grep -q "^$id\$" "$T"/cd_io_spec ||
			err "상태변경 화면인데 입력 명세가 없다: $id ($IO 에 '### $id' 절을 쓸 것)"
	done <"$T"/cd_scr_stateful
	n_io=$(wc -l <"$T"/cd_io_spec | tr -d ' ')
	[ "$n_io" -gt 0 ] || err "$IO 에서 화면 절을 하나도 찾지 못했다 (검사가 헛돌았다)"

	# 15-2. D19 restates each screen's access and security class in its own
	# heading. Two hand-written copies drift (M9), and this pair drifting means
	# the validation spec defends a screen that is locked differently.
	perl -nle 'next unless /^\|\s*([PA]-\d{3})\s*\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|/;
		($id, $acc, $cls) = ($1, $5, $7);
		for ($acc, $cls) { s/^\s+//; s/\s+$// }
		$acc =~ s/^권한://;
		print "$id\t$acc\t$cls"' "$SCR" >"$T"/cd_io_want

	# The heading itself may carry the metadata (admin sections) or the line
	# after it (public sections), so both are read together.
	perl -e 'my @l = <>; chomp @l;
		for my $i (0 .. $#l) {
			next unless $l[$i] =~ /^### ([PA]-\d{3})/;
			my $id = $1;
			my $head = join " ", @l[$i .. ($i + 3 > $#l ? $#l : $i + 3)];
			my ($cls) = $head =~ /(SC-[1-8])/;
			my ($acc) = $head =~ /`(공개|로그인|본인)`/;
			($acc) = $head =~ /`(?:권한:)?([a-z][a-z0-9]*\.[a-z][a-z0-9_]*)`/ unless $acc;
			print "$id\t", ($acc // "?"), "\t", ($cls // "?"), "\n";
		}' "$IO" >"$T"/cd_io_have

	sort "$T"/cd_io_want >"$T"/cd_io_want_s
	sort "$T"/cd_io_have >"$T"/cd_io_have_s
	while IFS="$(printf '\t')" read -r id acc cls; do
		[ -z "$id" ] && continue
		want=$(grep "^$id$(printf '\t')" "$T"/cd_io_want_s)
		[ -z "$want" ] && continue # unknown-screen case is reported above
		[ "$id$(printf '\t')$acc$(printf '\t')$cls" = "$want" ] ||
			err "$IO 의 $id 머리줄이 $SCR 과 다르다: [$acc / $cls] vs [$(echo "$want" | cut -f2-3 | tr '\t' '/')]"
	done <"$T"/cd_io_have_s
	done_ "상태변경 화면 $n_io 개 입력 명세 · 접근·유형이 $SCR 과 일치"
fi

# --- 15-1b. D19's own scope tables and counts match its sections -------------
# D19 states its scope three times over: a headline count, a per-band 대상 table,
# and the sections themselves. Adding two screens (P-514, A-512) updated the
# sections and every other document but not D19's own bookkeeping — the spec
# claimed 61 screens while specifying 63, and the 대상 table never named the new
# two. 15-1 cannot see this: it compares D19 against D11, never against itself.
begin
if [ -f "$IO" ]; then
	n_p=$(grep -c '^### P-' "$IO")
	n_a=$(grep -c '^### A-' "$IO")
	n_all=$((n_p + n_a))

	# The 대상 tables list screens one band per row: `| P-5xx | P-504 ... |`.
	perl -nle 'next unless /^\|\s*[PA]-\dxx[^|]*\|(.*)$/;
		my $row = $1;
		while ($row =~ /\b([PA]-\d{3})\b/g) { print $1 }' "$IO" | sort -u >"$T"/cd_io_band
	for id in $(comm -23 "$T"/cd_io_band "$T"/cd_io_spec); do
		err "$IO 대상 표에 있는데 절이 없는 화면: $id"
	done
	for id in $(comm -13 "$T"/cd_io_band "$T"/cd_io_spec); do
		err "$IO 이 명세하면서 대상 표에 올리지 않은 화면: $id"
	done

	# A band row may also declare its own size: `| A-5xx (9) | ... |`.
	perl -nle 'next unless /^\|\s*([PA]-\dxx)\s*\((\d+)\)\s*\|(.*)$/;
		my ($band, $want, $row) = ($1, $2, $3);
		my $have = 0;
		$have++ while $row =~ /\b[PA]-\d{3}\b/g;
		print "$band\t$want\t$have" if $want != $have' "$IO" >"$T"/cd_io_bandn
	while IFS="$(printf '\t')" read -r band want have; do
		[ -z "$band" ] && continue
		err "$IO 대역 $band 의 선언 개수가 실제와 다르다: ($want) vs $have 개"
	done <"$T"/cd_io_bandn

	# Every screen count written in prose. Limiting the scan to lines about
	# 화면/대상 keeps unrelated numbers out; leaving it otherwise generic means
	# rewording a sentence cannot silently drop it from the check.
	#
	# The expected value depends on WHERE the line is, not on "is it one of the
	# three counts". Set membership let `## 공개 화면 (28)` pass, because 28 is
	# the admin count — a wrong number that happens to be some other real one.
	# The document is three regions and each states exactly one figure:
	# the header speaks for both halves, then the public half, then the admin.
	perl -nle 'BEGIN { ($p, $a, $all) = @ARGV[0 .. 2]; @ARGV = ($ARGV[3]) }
		$want = $p if /^## 공개 화면/;
		$want = $a if /^## 관리자 화면/;
		$want = $all unless defined $want;
		next if /^### /;
		next unless /화면|대상/;
		my @n;
		while (/(?<!\d)(\d+)\s*개/g) { push @n, $1 }
		while (/\((\d+)\)/g) { push @n, $1 }
		for my $c (@n) { print "$.\t$c\t$want\t$_" }' "$n_p" "$n_a" "$n_all" "$IO" >"$T"/cd_io_counts
	n_counts=0
	while IFS="$(printf '\t')" read -r ln c want line; do
		[ -z "$ln" ] && continue
		n_counts=$((n_counts + 1))
		[ "$c" = "$want" ] ||
			err "$IO:$ln 의 화면 개수 $c 가 그 구간의 실제 값 $want 와 다르다 (공개 $n_p · 관리자 $n_a · 합 $n_all): $line"
	done <"$T"/cd_io_counts
	[ "$n_counts" -gt 0 ] || err "$IO 에서 화면 개수 진술을 하나도 읽지 못했다 (검사가 헛돌았다)"
	done_ "$IO 대상 표 · 개수 진술 $n_counts 건이 절 $n_all 개(공개 $n_p · 관리자 $n_a)와 구간별 일치"
fi

# --- 15-2. D30's column lists match the migrations that create them ---------
# A column written in the document but absent from the SQL is the hardest kind
# of drift to notice, because both sides look right on their own. It happened
# once with a single Phase 1 column.
#
# This used to compare only `users` against 00001_init.sql, which was the whole
# shipped schema at the time. Phase 1 turned ten more documented tables into
# real SQL, so the comparison follows every CREATE TABLE across every migration.
# Tables D30 still describes as planned (Phase 2, Phase 3) have no SQL yet and
# are skipped — they are checked the moment someone writes their migration.
begin
DM=docs/30-data-model.md
if [ ! -f "$DM" ] || [ -z "$(ls internal/migrations/*.sql 2>/dev/null)" ]; then
	err "데이터 모델 문서 또는 마이그레이션이 없다"
else
	# table<TAB>column, from CREATE TABLE bodies and ALTER TABLE ... ADD COLUMN.
	# Constraint clauses are upper-case so the lower-case column pattern skips
	# them, and so do the `--` comment lines.
	cat internal/migrations/*.sql | perl -e 'my $s = do { local $/; <> };
		my ($up) = $s =~ /(.*?)^-- \+goose Down/ms;
		$up = $s unless defined $up;
		# Only the Up half: Down drops things, it does not define them.
		for my $chunk (split /^-- \+goose Up/m, $s) {
			my ($u) = $chunk =~ /(.*?)^-- \+goose Down/ms;
			$u = $chunk unless defined $u;
			while ($u =~ /CREATE TABLE (\w+) \((.*?)^\);/gms) {
				my ($t, $body) = ($1, $2);
				print "$t\t$1\n" while $body =~ /^\s+([a-z_]+)\s+\S/gm;
			}
			# Two steps on purpose. A single pattern with a nested quantifier
			# ((?:[^;]*?ADD COLUMN[^;]*?)+) backtracks catastrophically and hung
			# the whole check on inputs where the statement did not match.
			while ($u =~ /ALTER TABLE (\w+)([^;]*);/gms) {
				my ($t, $body) = ($1, $2);
				next unless $body =~ /ADD COLUMN/;
				print "$t\t$1\n" while $body =~ /ADD COLUMN (\w+)/g;
			}
		}' | sort -u >"$T"/cd_sch_sql

	# table<TAB>column from D30. A heading may name more than one table
	# (`password_reset_tokens` · `email_verification_tokens`) and a table may
	# have more than one block (`users` plus `users 추가분`), so both are unions.
	perl -e 'my @l = <>; chomp @l;
		my (@cur, $incols);
		for my $i (0 .. $#l) {
			my $line = $l[$i];
			if ($line =~ /^### ([a-z_]+)\s*$/) { @cur = ($1); $incols = 0; next }
			if ($line =~ /^\*\*(.+)\*\*/) {
				my @t = $1 =~ /`([a-z_]+)`/g;
				if (@t) { @cur = @t; $incols = 0 }
				next;
			}
			unless ($line =~ /^\|/) { $incols = 0 unless $line =~ /^\s*$/; next }
			# Only a table whose first header cell is 컬럼 defines columns. D30
			# also has key/value tables — the settings key list is `키 | 값 |
			# 화면 | Phase`, and reading it as columns invented ten of them.
			if ($line =~ /^\|\s*컬럼\s*\|/) { $incols = 1; next }
			next unless @cur && $incols;
			# A row the document itself defers ("Phase 2에서 추가") is not part of
			# the migration being compared.
			next if $line =~ /Phase [0-9]에서 추가/;
			next unless $line =~ /^\|([^|]*)\|/;
			my $cell = $1;
			for my $c ($cell =~ /`([a-z_]+)`/g) {
				print "$_\t$c\n" for @cur;
			}
		}' "$DM" | sort -u >"$T"/cd_sch_doc

	if [ ! -s "$T"/cd_sch_doc ] || [ ! -s "$T"/cd_sch_sql ]; then
		err "$DM 또는 마이그레이션에서 컬럼을 하나도 읽지 못했다 (검사가 헛돌았다)"
	fi

	cut -f1 "$T"/cd_sch_sql | sort -u >"$T"/cd_sch_tbl
	n_col=0
	while read -r t; do
		[ -z "$t" ] && continue
		grep "^$t$(printf '\t')" "$T"/cd_sch_sql | cut -f2 | sort -u >"$T"/cd_sch_s1
		grep "^$t$(printf '\t')" "$T"/cd_sch_doc | cut -f2 | sort -u >"$T"/cd_sch_d1
		if [ ! -s "$T"/cd_sch_d1 ]; then
			err "마이그레이션이 만드는 테이블인데 $DM 에 정의가 없다: $t"
			continue
		fi
		n_col=$((n_col + $(wc -l <"$T"/cd_sch_s1 | tr -d ' ')))
		for c in $(comm -23 "$T"/cd_sch_d1 "$T"/cd_sch_s1); do
			err "$DM 이 마이그레이션에 없는 컬럼을 적었다: $t.$c"
		done
		for c in $(comm -13 "$T"/cd_sch_d1 "$T"/cd_sch_s1); do
			err "마이그레이션에 있는데 $DM 에 없는 컬럼: $t.$c"
		done
	done <"$T"/cd_sch_tbl
	done_ "$DM ↔ 마이그레이션: 테이블 $(wc -l <"$T"/cd_sch_tbl | tr -d ' ') 종 · 컬럼 $n_col 개 일치"
fi

# --- 15-3. every screen has a work item -------------------------------------
# A screen with no work item never gets built: the WBS is what someone reads to
# decide what to do next. Two screens added in the same session that added them
# to D11/D12/D19 were missed here, and nothing noticed.
#
# The WBS groups screens into ranges (`P-203~P-206`), so those are expanded
# before comparing — matching IDs literally would report 20 false misses.
begin
WBS=docs/81-work-breakdown.md
if [ ! -f "$WBS" ]; then
	err "작업 분해 문서가 없다: $WBS"
else
	perl -e 'my $t = do { local $/; <> };
		my %c;
		$c{$1} = 1 while $t =~ /\b([PA]-\d{3})\b/g;
		while ($t =~ /\b([PA])-(\d{3})\s*~\s*[PA]?-?(\d{3})\b/g) {
			$c{sprintf "%s-%03d", $1, $_} = 1 for ($2 .. $3);
		}
		print "$_\n" for sort keys %c;' "$WBS" >"$T"/cd_wbs_cov

	# Phase 0 is already built and released; its screens are history, not work.
	printf 'P-001\nP-002\n' | sort >"$T"/cd_wbs_done
	comm -23 "$T"/cd_def_scr_u "$T"/cd_wbs_cov >"$T"/cd_wbs_gap
	for id in $(comm -23 "$T"/cd_wbs_gap "$T"/cd_wbs_done); do
		err "$WBS 에 작업 항목이 없는 화면: $id (만들 사람이 이 화면을 볼 일이 없다)"
	done
	done_ "화면 $(wc -l <"$T"/cd_def_scr_u | tr -d ' ') 개 전부 작업 항목 보유 (Phase 0 구현분 제외)"
fi

# --- 16..18. the permission ↔ screen map agrees in both directions ----------
# D11 says which permission opens a screen; D15 says which screens a permission
# opens. They are written by hand in two files, so they drift unless checked.
begin
ACL=docs/15-access-control.md
if [ ! -f "$ACL" ]; then
	err "권한 문서가 없다: $ACL"
else
	# permission key -> screens, from D15 §2.2 only. Scoped to that section
	# because other tables in the file (the seed matrix in §2.5) start rows
	# with a backticked permission key too and would otherwise be read as
	# permission definitions with an empty screen list.
	perl -nle 'if (/^### 2\.2 /) { $in = 1; next }
		if ($in && /^### /) { $in = 0 }
		next unless $in;
		next unless /^\|\s*`([a-z][a-z0-9]*\.[a-z][a-z0-9_]*)`\s*\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|/;
		($k, $scr) = ($1, $5);
		$scr =~ s/^\s+//; $scr =~ s/\s+$//;
		print "$k\t$scr"' "$ACL" >"$T"/cd_perm_map
	cut -f1 "$T"/cd_perm_map | sort -u >"$T"/cd_perm_def

	# 16. every 권한: used in D11 is a permission D15 defines
	perl -nle 'print $1 if /^\|\s*[PA]-\d{3}\s*\|[^|]*\|[^|]*\|[^|]*\|\s*권한:([a-z0-9._]+)\s*\|/' "$SCR" |
		sort -u >"$T"/cd_perm_used
	for p in $(comm -23 "$T"/cd_perm_used "$T"/cd_perm_def); do
		err "$ACL 에 정의되지 않은 권한을 화면이 요구한다: $p"
	done

	# 17. every permission names at least one screen, and those screens exist
	while IFS="$(printf '\t')" read -r key screens; do
		[ -z "$key" ] && continue
		if [ -z "$screens" ]; then
			err "권한에 사용 화면이 없다 (죽은 권한): $key"
			continue
		fi
		for s in $(printf '%s' "$screens" | tr ',' ' '); do
			case "$s" in [PA]-[0-9][0-9][0-9]) ;; *) continue ;; esac
			grep -q "^| $s |" "$SCR" || err "권한 $key 가 존재하지 않는 화면을 가리킨다: $s"
		done
	done <"$T"/cd_perm_map

	# 18. if a screen's 접근 is 권한:X, D15's row for X must list that screen.
	#
	# Redirection, not a pipe: a `while` on the right of a pipe runs in a
	# subshell and every err() would be lost — the M3 failure, which this
	# check reintroduced once already. scripts/selftest.sh injects a mismatch
	# here so it cannot come back a fourth time.
	perl -nle 'print "$1\t$2" if /^\|\s*([PA]-\d{3})\s*\|[^|]*\|[^|]*\|[^|]*\|\s*권한:([a-z0-9._]+)\s*\|/' \
		"$SCR" >"$T"/cd_scr_perm
	while IFS="$(printf '\t')" read -r id key; do
		[ -z "$id" ] && continue
		row=$(grep "^$key$(printf '\t')" "$T"/cd_perm_map)
		case "$row" in
		*"$id"*) ;;
		*) err "$ACL 의 $key 행이 $id 를 사용 화면으로 적지 않았다" ;;
		esac
	done <"$T"/cd_scr_perm

	# 19. §2.5 seed matrix must cover exactly the permissions §2.2 defines.
	# Two hand-written tables in one file drift the same way two files do.
	perl -nle 'if (/^### 2\.5 /) { $in = 1; next }
		if ($in && /^### /) { $in = 0 }
		next unless $in;
		print $1 if /^\|\s*`([a-z][a-z0-9]*\.[a-z][a-z0-9_]*)`\s*\|/' "$ACL" |
		sort -u >"$T"/cd_perm_seed
	for p in $(comm -23 "$T"/cd_perm_def "$T"/cd_perm_seed); do
		err "권한이 2.5 시드 매트릭스에 없다: $p"
	done
	for p in $(comm -13 "$T"/cd_perm_def "$T"/cd_perm_seed); do
		err "2.5 시드 매트릭스에 정의되지 않은 권한이 있다: $p"
	done
	done_ "권한 $(wc -l <"$T"/cd_perm_def | tr -d ' ') 종 · 화면↔권한 양방향 일치 · 시드 매트릭스 일치"
fi

# --- 19b. the RBAC seed migration matches D15 -------------------------------
# The seed is D15 §2.2 and §2.5 hand-copied into INSERT statements, and D81's
# own risk table says the mitigation is "parse the document table and compare",
# not "write the pairs out a second time". A missing grant is invisible until
# somebody gets a 403 on a screen nobody can explain, because permissions carry
# no implication (D15 §2.1) — one missing cell locks that screen for that role.
#
# This is the same drift M9/M11/M12 recorded three times: a decision changes in
# one document and its copy elsewhere keeps the old answer.
begin
# Phase 마다 시드 파일이 하나씩 는다. 목록을 **적지 않고 찾는다** — 적어
# 두었더니 Phase 3 시드를 새 파일에 넣는 순간 검사가 그것을 못 보고, 권한을
# 하나 빼도 조용히 통과했다. 목록은 다음 Phase 에서 또 낡는다.
SEEDS=$(grep -l 'INSERT INTO permissions\|INSERT INTO role_permissions' \
	internal/migrations/*.sql 2>/dev/null | sort | tr '\n' ' ')
if [ -z "$SEEDS" ] || [ ! -f "$ACL" ]; then
	err "RBAC 시드 마이그레이션 또는 권한 문서가 없다"
else
	# 파일마다 돌린다. perl 에 여러 파일을 주고 <> 로 한 번에 슬러프하면 내용이
	# 이어 붙고, 첫 `-- +goose Down` 에서 자르는 순간 **두 번째 시드가 통째로
	# 사라진다** — 검사는 조용히 통과한다.
	: >"$T"/cd_seed_sql
	for f in $SEEDS; do
		perl -e 'my $s = do { local $/; <> };
			($s) = $s =~ /(.*?)^-- \+goose Down/ms or exit;
			if ($s =~ /INSERT INTO permissions[^;]*?VALUES(.*?);/s) {
				my $b = $1;
				print "perm\t$1\n" while $b =~ /\(\s*'"'"'([a-z][a-z0-9._]*)'"'"'/g;
			}
			if ($s =~ /INSERT INTO role_permissions[^;]*?FROM \(VALUES(.*?)\)\s*AS/s) {
				my $b = $1;
				print "grant\t$1\t$2\n"
					while $b =~ /\(\s*'"'"'([a-z_]+)'"'"'\s*,\s*'"'"'([a-z][a-z0-9._]*)'"'"'\s*\)/g;
			}
			if ($s =~ /INSERT INTO roles[^;]*?VALUES(.*?);/s) {
				my $b = $1;
				print "role\t$1\n" while $b =~ /\(\s*'"'"'([a-z_]+)'"'"'/g;
			}' "$f" >>"$T"/cd_seed_sql
	done

	# D15 §2.2. **어느 Phase 까지 볼지를 적지 않고 유도한다** — `[12]` 로
	# 굳어 있었더니 Phase 3 시드를 넣는 순간 그 권한들이 "문서에 없는 것" 으로
	# 보고됐다. 심어진 권한의 Phase 중 가장 큰 값까지가 대조 범위다.
	perl -nle 'if (/^### 2\.2 /) { $i = 1; next } if ($i && /^### /) { $i = 0 }
		next unless $i;
		print "$1\t$2" if /^\|\s*`([a-z][a-z0-9]*\.[a-z][a-z0-9_]*)`\s*\|[^|]*\|[^|]*\|\s*(\d+)\s*\|/' \
		"$ACL" | sort -u >"$T"/cd_perm_phase
	# 범위는 **시드 파일이 스스로 밝힌 Phase** 에서 온다. 심어진 권한의 Phase
	# 최대값으로 구하면 순환이다 — 문서가 어떤 권한을 나중 Phase 로 옮겨도 그
	# 권한 자신이 범위를 넓혀서 위반이 사라진다. 시드는 "Phase N 권한" 이라고
	# 적고, 그 줄이 없으면 검사가 멈춘다.
	# 파일마다 요구한다. 하나만 있으면 되게 두면 새 시드가 표시를 빠뜨려도
	# 조용히 지나가고, 그 파일의 권한은 범위 밖으로 밀려 "심지 않았다" 가 아니라
	# 아무 말도 없이 통과한다.
	max_phase=0
	for f in $SEEDS; do
		ph=$(perl -nle 'print $1 if /^-- Phase (\d+) 권한/' "$f" | head -1)
		if [ -z "$ph" ]; then
			err "$f 에 '-- Phase N 권한' 표시가 없다 (대조 범위를 정할 수 없다)"
			continue
		fi
		[ "$ph" -gt "$max_phase" ] && max_phase=$ph
	done
	awk -F'\t' -v m="$max_phase" '$2+0 <= m { print "perm\t" $1 }' \
		"$T"/cd_perm_phase | sort -u >"$T"/cd_seed_doc
	# D15 §2.5, restricted to those permissions. ● is a global grant; ◐ is board
	# scoped and the preset writes it when a board is created (2.4) — it must
	# never appear in a seed, or every board is already public before anyone
	# chooses a preset.
	perl -nle 'if (/^### 2\.5 /) { $i = 1; next } if ($i && /^### /) { $i = 0 }
		next unless $i;
		next unless /^\|\s*`([a-z][a-z0-9]*\.[a-z][a-z0-9_]*)`\s*\|([^|]*)\|([^|]*)\|([^|]*)\|([^|]*)\|/;
		my ($k, @r) = ($1, $2, $3, $4, $5);
		my @n = ("anonymous", "member", "editor", "operator");
		# Compare the literal byte sequence: perl reads the file as bytes, so a
		# \x{25cf} wide character never equals the three UTF-8 bytes on the page.
		for my $j (0 .. 3) { my $v = $r[$j]; $v =~ s/\s//g;
			print "grant\t$n[$j]\t$k" if $v eq "●" }' "$ACL" >>"$T"/cd_seed_doc
	# §1.1 builtin roles.
	perl -nle 'if (/^### 1\.1 /) { $i = 1; next } if ($i && /^### /) { $i = 0 }
		next unless $i;
		print "role\t$1" if /^\|\s*`([a-z_]+)`\s*\|/' "$ACL" >>"$T"/cd_seed_doc

	sort -u "$T"/cd_seed_sql >"$T"/cd_seed_sql_s
	sort -u "$T"/cd_seed_doc >"$T"/cd_seed_doc_s
	# Grants are only expected for the Phase 1 permissions; D15 §2.5 also lists
	# later-phase rows, which this migration correctly does not seed.
	grep '^perm	' "$T"/cd_seed_doc_s | cut -f2 | sort -u >"$T"/cd_seed_p1
	awk -F'\t' 'NR==FNR{p[$0];next} $1!="grant" || ($3 in p)' \
		"$T"/cd_seed_p1 "$T"/cd_seed_doc_s >"$T"/cd_seed_want

	for l in $(comm -23 "$T"/cd_seed_want "$T"/cd_seed_sql_s | tr '\t' '/'); do
		err "RBAC 시드가 $ACL 에 있는 것을 심지 않았다: $l"
	done
	for l in $(comm -13 "$T"/cd_seed_want "$T"/cd_seed_sql_s | tr '\t' '/'); do
		err "RBAC 시드가 $ACL 에 없는 것을 심는다: $l"
	done
	n_seed=$(wc -l <"$T"/cd_seed_want | tr -d ' ')
	[ "$n_seed" -gt 0 ] || err "$ACL 에서 시드 대상을 하나도 읽지 못했다 (검사가 헛돌았다)"
	done_ "RBAC 시드 $n_seed 건(역할·Phase 1 권한·부여)이 $ACL 과 일치"
fi

# --- 19c. D81 is a runnable plan, and its progress record is honest ---------
# D82 turns D81 into a loop: scripts/next-task.sh picks the next task whose
# prerequisites are all done. Three things have to hold or that loop misleads
# or stalls, and none of them are visible by reading the table.
begin
if [ ! -f "$WBS" ]; then
	err "작업 분해 문서가 없다: $WBS"
else
	perl -e '
		my $wbs = shift;
		open my $fh, "<", $wbs or die "$wbs: $!\n";
		my (@order, %t);
		while (my $l = <$fh>) {
			next unless $l =~ /^\|\s*(W\d-\d+)\s*\|([^|]*)\|([^|]*)\|([^|]*)\|/;
			my ($id, $what, $pre, $out) = ($1, $2, $3, $4);
			next if exists $t{$id};
			push @order, $id;
			$t{$id} = { what => $what, out => $out,
				done => ($what =~ /\*\*\(완료/ ? 1 : 0),
				opt  => ($what =~ /\(선택\)/ ? 1 : 0),
				pre  => [ $pre =~ /(W\d-\d+)/g ] };
		}
		close $fh;
		unless (@order) { print "ERR\t작업을 하나도 읽지 못했다 (검사가 헛돌았다)\n"; exit }

		# 1. Prerequisites must name real tasks, or next-task.sh waits forever
		#    on something that does not exist.
		for my $id (@order) {
			for my $p (@{ $t{$id}{pre} }) {
				print "ERR\t$id 의 선행 $p 가 표에 없다\n" unless exists $t{$p};
			}
		}

		# 2. The graph must be acyclic. A cycle is a deadlock the loop cannot
		#    report as anything but "no task is ready", with no clue why.
		my %indeg = map { $_ => scalar grep { exists $t{$_} } @{ $t{$_}{pre} } } ();
		for my $id (@order) {
			$indeg{$id} = scalar grep { exists $t{$_} } @{ $t{$id}{pre} };
		}
		my @q = grep { $indeg{$_} == 0 } @order;
		my %seen = map { $_ => 1 } @q;
		my $n = 0;
		while (my $id = shift @q) {
			$n++;
			for my $o (@order) {
				next if $seen{$o};
				next unless grep { $_ eq $id } @{ $t{$o}{pre} };
				$indeg{$o}--;
				if ($indeg{$o} == 0) { push @q, $o; $seen{$o} = 1 }
			}
		}
		print "ERR\t선행 관계에 순환이 있다 (" . (scalar(@order) - $n) . "건이 정렬 불가) — 루프가 교착된다\n"
			if $n != scalar @order;

		# 3. A task marked done must have produced its deliverable. Marking a
		#    row complete costs two words; this is what makes it cost the work.
		for my $id (@order) {
			next unless $t{$id}{done};
			# \s* on both sides: a deliverable written with a stray space inside
			# the backticks used to be skipped silently, so a task could be marked
			# done and its missing file never noticed.
			for my $f ($t{$id}{out} =~ /`\s*([A-Za-z0-9_\/.\-]+\.(?:sql|go|sh|md|json|mod))\s*`/g) {
				next if -e $f;
				my @hit = grep { -e $_ } map { "$_/$f" }
					qw(internal/migrations docs scripts internal cmd .);
				print "ERR\t완료로 표시된 $id 의 산출물이 없다: $f\n" unless @hit;
			}
		}

		my $done = grep { $t{$_}{done} } @order;
		my $opt  = grep { $t{$_}{opt} } @order;
		my %ph;
		$ph{ substr($_, 0, 2) }++ for @order;
		print "OK\t" . scalar(@order) . "\t$done\t$opt\t"
			. join(",", map { "$_=$ph{$_}" } sort keys %ph) . "\n";
	' "$WBS" >"$T"/cd_wbs_loop

	while IFS="$(printf '\t')" read -r kind a b c d; do
		[ "$kind" = "ERR" ] && err "$WBS: $a"
	done <"$T"/cd_wbs_loop

	# 4. The summary table restates the counts by hand. It said 117 while the
	#    table held 123, which is the number a reader plans a schedule with.
	sums=$(grep '^OK	' "$T"/cd_wbs_loop)
	if [ -n "$sums" ]; then
		n_all=$(printf '%s' "$sums" | cut -f2)
		n_opt=$(printf '%s' "$sums" | cut -f4)
		per=$(printf '%s' "$sums" | cut -f5)
		perl -e 'my ($per, $all, $opt, $wbs) = @ARGV;
			my %want = map { my ($k, $v) = split /=/; ($k, $v) } split /,/, $per;
			open my $fh, "<", $wbs or die;
			my ($seen_total, %seen_ph);
			while (my $l = <$fh>) {
				if ($l =~ /^\|\s*Phase (\d)[^|]*\|\s*(\d+)\s*\|/) {
					$seen_ph{"W$1"} = $2;
				}
				if ($l =~ /^\|\s*\*\*합계\*\*\s*\|\s*\*\*(\d+)\*\*\s*\|\s*\*\*(\d+)\*\*/) {
					$seen_total = $1;
					print "요약표 선택 수 $2 가 실제 $opt 와 다르다\n" if $2 != $opt;
				}
			}
			for my $p (sort keys %want) {
				next unless exists $seen_ph{$p};
				print "요약표 $p 작업 수 $seen_ph{$p} 가 실제 $want{$p} 와 다르다\n"
					if $seen_ph{$p} != $want{$p};
			}
			print "요약표 합계 " . ($seen_total // "없음") . " 가 실제 $all 와 다르다\n"
				if !defined $seen_total || $seen_total != $all;
		' "$per" "$n_all" "$n_opt" "$WBS" >"$T"/cd_wbs_sum
		while read -r line; do
			[ -n "$line" ] && err "$WBS: $line"
		done <"$T"/cd_wbs_sum
		done_ "$WBS 작업 $n_all 건 · 선행 순환 없음 · 완료 $(printf '%s' "$sums" | cut -f3)건의 산출물 실재 · 요약표 일치"
	fi
fi

# --- 19d. D20's package list matches internal/ ------------------------------
# W1-02 was written as "재확인" — go look and compare. A one-time look is worth
# nothing the next time a package is added, and the package list is what someone
# reads to decide where new code goes. So the comparison runs every build.
#
# Only the shipped block counts. D20 also lists "Phase 1 이후 추가될 자리"
# (auth/, theme/, content/ ...), which are deliberately absent from the tree.
begin
ARCH=docs/20-architecture.md
if [ ! -f "$ARCH" ]; then
	err "아키텍처 문서가 없다: $ARCH"
else
	perl -nle 'if (/^## 패키지 구조/) { $i = 1; next }
		if ($i && /^Phase 1 이후/) { $i = 0 }
		next unless $i;
		print $1 if /^\s{2}([a-z][a-z0-9_]*)\/\s/' "$ARCH" | sort -u >"$T"/cd_pkg_doc

	# A directory is a package when it holds a non-test .go file. templates/
	# dirs hold embedded assets and no Go, so they are not packages and must not
	# be listed.
	find internal -name '*.go' -not -name '*_test.go' 2>/dev/null |
		sed 's|^internal/||; s|/[^/]*$||' | grep -v '/' | sort -u >"$T"/cd_pkg_real

	if [ ! -s "$T"/cd_pkg_doc ] || [ ! -s "$T"/cd_pkg_real ]; then
		err "$ARCH 또는 internal/ 에서 패키지를 하나도 읽지 못했다 (검사가 헛돌았다)"
	fi
	for p in $(comm -23 "$T"/cd_pkg_doc "$T"/cd_pkg_real); do
		err "$ARCH 이 없는 패키지를 현재 구조로 적었다: internal/$p (「Phase 1 이후」 블록으로 옮길 것)"
	done
	for p in $(comm -13 "$T"/cd_pkg_doc "$T"/cd_pkg_real); do
		err "internal/$p 가 $ARCH 「패키지 구조」에 없다 — 새 코드를 어디 둘지 문서가 답하지 못한다"
	done
	done_ "$ARCH 패키지 구조 $(wc -l <"$T"/cd_pkg_real | tr -d ' ') 개가 internal/ 과 일치"
fi

# --- 19e. internal/app/screens.go matches D11's screen table ----------------
# The boot self-check (D15 4.4 검사 5) compares every registered route against
# this map. A map that disagrees with D11 makes that check confidently wrong in
# both directions: a screen D11 renamed passes as unknown, and a class D11
# changed passes as agreeing. It is a hand-copied table, which is the exact
# shape of .ai/MISTAKES.md M9 — so it is compared, every build.
begin
INV=internal/app/screens.go
if [ ! -f "$INV" ]; then
	err "화면 인벤토리가 없다: $INV"
else
	perl -nle 'print "$1 $2" if /^\t"([PA]-\d+)":\s+SC(\d)/' "$INV" | sort >"$T"/cd_inv_go
	perl -F'\|' -nle 'next unless /^\| [PA]-\d+ /;
		for (@F) { s/^\s+|\s+$//g }
		$F[7] =~ /^SC-(\d)$/ and print "$F[1] $1"' "$SCR" | sort >"$T"/cd_inv_doc

	if [ ! -s "$T"/cd_inv_go ] || [ ! -s "$T"/cd_inv_doc ]; then
		err "$INV 또는 $SCR 에서 화면을 하나도 읽지 못했다 (검사가 헛돌았다)"
	fi
	for l in $(comm -23 "$T"/cd_inv_go "$T"/cd_inv_doc | tr ' ' ':'); do
		err "$INV 의 항목이 $SCR 과 다르다: $(echo "$l" | tr ':' ' ') — D11 에 없거나 유형이 다르다"
	done
	for l in $(comm -13 "$T"/cd_inv_go "$T"/cd_inv_doc | tr ' ' ':'); do
		err "$SCR 의 화면이 $INV 에 없다: $(echo "$l" | tr ':' ' ') — 부팅 점검이 이 화면을 모른다"
	done
	done_ "$INV 화면 $(wc -l <"$T"/cd_inv_doc | tr -d ' ') 개가 $SCR 과 일치"
fi

# --- 19g. P5 exemptions in code match D15's list ----------------------------
# UnsafeGETReason turns off "안전 메서드는 상태를 바꾸지 않는다" for one route.
# An exemption nobody reviewed is the same as no rule, so the list lives in D15
# and the code is compared against it every build.
begin
TREE=internal/app/tree.go
if [ ! -f "$TREE" ]; then
	err "라우트 트리가 없다: $TREE"
else
	# 코드 쪽: UnsafeGETReason 이 붙은 라우트의 화면 ID.
	perl -nle 'if (/Screen: "([PA]-\d+)"/) { $id = $1 }
		print $id if /UnsafeGETReason:/ && $id' "$TREE" | sort -u >"$T"/cd_p5_go
	# 문서 쪽: 「P5 예외」표의 화면 ID.
	perl -nle 'if (/^### P5 예외/) { $in = 1; next } if ($in && /^#{2,3} /) { $in = 0 }
		print $1 if $in && /^\|.*\(([PA]-\d+)\)/' "$ACL" | sort -u >"$T"/cd_p5_doc

	for l in $(comm -23 "$T"/cd_p5_go "$T"/cd_p5_doc); do
		err "$TREE 의 P5 예외가 $ACL 「P5 예외」에 없다: $l"
	done
	for l in $(comm -13 "$T"/cd_p5_go "$T"/cd_p5_doc); do
		err "$ACL 이 예외로 적었는데 코드에 없다: $l"
	done
	done_ "P5 예외 $(wc -l <"$T"/cd_p5_doc | tr -d ' ') 건이 $ACL 과 $TREE 에서 일치"
fi

# --- 19f. internal/commerce/state.go matches D14's order state diagram ------
# D14 5절 opens by naming the commonest mistake: a dropdown listing every status
# with no server check. The defence is that the Go table IS the diagram — so the
# two are compared rather than hand-copied (.ai/MISTAKES.md M9). An arrow the
# document adds and the code misses is a transition an operator cannot make; one
# the code has and the document does not is a transition nobody agreed to.
begin
SM=internal/commerce/state.go
FLOW=docs/14-screen-flows.md
if [ ! -f "$SM" ] || [ ! -f "$FLOW" ]; then
	err "상태머신 또는 흐름 문서가 없다: $SM / $FLOW"
else
	# The mermaid arrows, minus the [*] endpoints — those are "이 상태가
	# 최종이다" markers and D14 says they have no screen.
	# Scoped to 5절: D14 holds several diagrams and the others use arrows of the
	# same shape — an unscoped read pulled a login-flow edge in as a transition.
	perl -nle 'if (/^## 5\. 주문 상태머신/) { $in = 1; next } if ($in && /^## /) { $in = 0 }
		next unless $in;
		print "$1>$2" if /^\s*(\S+)\s*-->\s*(\S+):/ && $1 ne "[*]" && $2 ne "[*]"' \
		"$FLOW" | sort -u >"$T"/cd_sm_doc
	# The Go map: `StatusX: {` opens a source state, `StatusY: {...}` inside it
	# is one arrow. Both sides are printed as the Korean labels the constants
	# hold, so the comparison does not depend on the Go identifier names.
	perl -nle '
		if (/^\tStatus(\w+)\s+Status = "([^"]+)"/) { $label{$1} = $2; next }
		if (/^\tStatus(\w+): \{$/)  { $from = $label{$1} // "?$1"; next }
		if (/^\t\},?$/)              { $from = undef; next }
		if ($from && /^\t\tStatus(\w+):\s*\{/) {
			print "$from>" . ($label{$1} // "?$1");
		}' "$SM" | sort -u >"$T"/cd_sm_go

	if [ ! -s "$T"/cd_sm_doc ] || [ ! -s "$T"/cd_sm_go ]; then
		err "$FLOW 또는 $SM 에서 전이를 하나도 읽지 못했다 (검사가 헛돌았다)"
	fi
	for l in $(comm -23 "$T"/cd_sm_go "$T"/cd_sm_doc); do
		err "$SM 에 있는데 $FLOW 다이어그램에 없는 전이: $(echo "$l" | tr '>' ' ')"
	done
	for l in $(comm -13 "$T"/cd_sm_go "$T"/cd_sm_doc); do
		err "$FLOW 다이어그램에 있는데 $SM 에 없는 전이: $(echo "$l" | tr '>' ' ')"
	done
	done_ "주문 상태 전이 $(wc -l <"$T"/cd_sm_doc | tr -d ' ') 건이 $FLOW 와 $SM 에서 일치 (FR-604)"
fi

# --- 20. D80's planning-completeness table counts match the documents -------
# That table is what anyone asking "얼마나 됐나" reads first, and every number in
# it is copied by hand from another document. The 2026-08-02 version was stale
# in five cells — FR 87 (80), NFR 25 (31), 화면 99 (101), 커버리지 35 (36),
# 템플릿 42 (43) — while `make check` stayed green (.ai/MISTAKES.md M12).
# A count nobody recomputes is a wrong answer wearing a checkmark.
begin
ROADMAP=docs/80-roadmap.md
if [ ! -f "$ROADMAP" ]; then
	err "로드맵 문서가 없다: $ROADMAP"
else
	# Expected values, recomputed from the documents that own them.
	{
		printf '10-requirements.md\t%s %s\n' \
			"$(perl -nle 'print $1 if /^\| (FR-\d{3}) /' "$REQ" | sort -u | wc -l | tr -d ' ')" \
			"$(perl -nle 'print $1 if /^\| (NFR-\d{3}) /' "$REQ" | sort -u | wc -l | tr -d ' ')"
		printf '11-screens.md\t%s\n' "$(wc -l <"$T"/cd_def_scr_u | tr -d ' ')"
		printf '15-access-control.md\t%s %s\n' \
			"$(perl -nle 'if (/^### 1\.1 /) { $in = 1; next } if ($in && /^#{2,3} /) { $in = 0 }
				print $1 if $in && /^\|\s*`([a-z_]+)`\s*\|/' "$ACL" | sort -u | wc -l | tr -d ' ')" \
			"$(wc -l <"$T"/cd_perm_def | tr -d ' ')"
		printf '16-data-coverage.md\t%s\n' "$(wc -l <"$T"/cd_tbl_cov | tr -d ' ')"
		printf '17-theme-contract.md\t%s\n' \
			"$(perl -nle 'print $1 if /^\|\s*`([a-z][a-z0-9\/._-]*\.(?:html|xml|txt))`\s*\|/' "$THEME" |
				sort -u | wc -l | tr -d ' ')"
		printf '19-screen-io.md\t%s\n' "$(wc -l <"$T"/cd_io_spec | tr -d ' ')"
		printf '81-work-breakdown.md\t%s\n' \
			"$(perl -nle 'print $1 if /^\|\s*(W\d-\d+)\s*\|/' "$WBS" | sort -u | wc -l | tr -d ' ')"
		printf '90-conventions.md\t%s\n' "$(ls docs/*.md .ai/*.md | wc -l | tr -d ' ')"
	} >"$T"/cd_rm_want

	# Rows of the 기획 완결성 table only. Phase sections link to the same
	# documents but their numbers are requirement IDs and section numbers, so
	# scanning past this heading would produce nothing but false positives.
	#
	# Links are captured before they are stripped: `[D10](10-requirements.md)`
	# would otherwise contribute a stray "10" to the row's numbers.
	perl -nle 'if (/^## 기획 완결성/) { $in = 1; next }
		if ($in && /^#{2,3} /) { $in = 0 }
		next unless $in && /^\|/;
		my @doc = /\]\(([0-9]{2}-[a-z-]+\.md)\)/g;
		next unless @doc;
		(my $bare = $_) =~ s/\[[^\]]*\]\([^)]*\)//g;
		my @num = $bare =~ /(\d+)/g;
		print "$_\t$.\t@num" for @doc' "$ROADMAP" >"$T"/cd_rm_have

	# Compared as an ordered sequence, not as a set. `(FR 80 / NFR 31)` and
	# `(FR 31 / NFR 80)` contain the same two numbers, and set membership called
	# the swapped one correct — a row can be wrong while every figure in it is
	# real. Order is the only thing that distinguishes them.
	n_rm=0
	while IFS="$(printf '\t')" read -r doc ln nums; do
		[ -z "$doc" ] && continue
		want=$(grep "^$doc$(printf '\t')" "$T"/cd_rm_want | cut -f2)
		[ -z "$want" ] && continue # a row for a document that states no count
		n_rm=$((n_rm + 1))
		[ "$nums" = "$want" ] ||
			err "$ROADMAP:$ln $doc 행의 숫자가 문서에서 센 값과 다르다: [${nums:-없음}] vs [$want]"
	done <"$T"/cd_rm_have
	[ "$n_rm" -gt 0 ] || err "$ROADMAP 기획 완결성 표에서 숫자를 가진 행을 하나도 읽지 못했다 (검사가 헛돌았다)"
	done_ "$ROADMAP 기획 완결성 $n_rm 행의 숫자가 각 문서에서 센 값과 일치"
fi

# ---- 마이그레이션이 CHANGELOG 에 들어갔는가 (NFR-307, W4-03) ------------------
#
# 운영자는 업그레이드 전에 CHANGELOG 만 읽는다 (D70). 스키마를 바꾸는 릴리즈가
# 거기 없으면, 되돌릴 수 없는 변경을 모르고 올린 뒤에야 알게 된다.
#
# 목록을 하드코딩하지 않고 **디렉터리에서 센다** — 하드코딩하면 새 마이그레이션이
# 검사에 안 보이고, 그 상태가 바로 이 검사가 막으려는 것이다.
CL=CHANGELOG.md
if [ -f "$CL" ]; then
	mig_missing=0
	mig_n=0
	for f in internal/migrations/*.sql; do
		[ -f "$f" ] || continue
		name=$(basename "$f")
		mig_n=$((mig_n + 1))
		grep -qF "$name" "$CL" || { err "$CL 에 없는 마이그레이션: $name"; mig_missing=1; }
	done
	if [ "$mig_n" -eq 0 ]; then
		err "마이그레이션을 하나도 찾지 못했다 (검사가 헛돌았다)"
	elif [ "$mig_missing" -eq 0 ]; then
		printf '  ✓ 마이그레이션 %s 개가 전부 %s 에 있다\n' "$mig_n" "$CL"
	fi
	# 다운그레이드 경로가 무엇인지 적혀 있어야 한다. `Down` 이 있다는 것과
	# 되돌릴 수 있다는 것은 다르다 (NFR-308).
	# **가장 최근 릴리즈 절 안에서 찾는다.** 파일 어딘가에 한 번 적혀 있으면
	# 통과하게 두면, 다음 릴리즈가 파괴적 변경을 넣고 아무 말도 안 해도 옛
	# 문장 하나로 계속 초록이 된다.
	latest_section=$(awk '/^## /{n++} n==1' "$CL")
	if [ -z "$latest_section" ]; then
		err "$CL 에서 릴리즈 절을 찾지 못했다 (검사가 헛돌았다)"
	elif printf '%s\n' "$latest_section" | grep -q "백업 복원"; then
		printf '  ✓ %s 의 최신 릴리즈 절이 되돌릴 수 없는 변경의 경로를 밝힌다\n' "$CL"
	else
		err "$CL 의 최신 릴리즈 절에 다운그레이드 경로(백업 복원)가 없다 (NFR-308)"
	fi
else
	err "$CL 이 없다"
fi

# --- 30. docs/schema.sql 이 마이그레이션보다 뒤처지지 않았다 ---------------
#
# 이 파일은 ERD 도구에 올리는 통합 DDL 이고 `make schema` 가 만든다. 만드는
# 데 데이터베이스가 필요하므로 여기서 다시 뽑아 비교할 수는 없다 — 대신
# **마이그레이션이 만드는 테이블이 전부 들어 있는지**만 본다. 테이블을 추가한
# 마이그레이션을 넣고 재생성을 잊으면 여기서 걸린다.
#
# 이 검사가 필요한 이유는 이미 겪었다: `orders.user_id` 가 문서에는 RESTRICT,
# 스키마에는 SET NULL 로 갈라져 있었고 아무도 몰랐다. 손으로 맞추는 사본은
# 반드시 어긋나므로, 사본을 두려면 어긋남을 기계가 잡아야 한다.
begin
SCHEMA_SQL=docs/schema.sql
if [ ! -f "$SCHEMA_SQL" ]; then
	err "$SCHEMA_SQL 이 없다 — make schema"
else
	perl -nle 'print lc $1 while /^CREATE TABLE (?:IF NOT EXISTS )?(\w+)/gi' \
		internal/migrations/*.sql | sort -u >"$T"/cd_sch_mig
	perl -nle 'print lc $1 while /^CREATE TABLE (?:public\.)?(\w+)/gi' \
		"$SCHEMA_SQL" | sort -u >"$T"/cd_sch_dump

	[ -s "$T"/cd_sch_mig ] ||
		err "마이그레이션에서 CREATE TABLE 을 하나도 읽지 못했다 (검사가 헛돌았다)"
	[ -s "$T"/cd_sch_dump ] ||
		err "$SCHEMA_SQL 에서 CREATE TABLE 을 하나도 읽지 못했다 (검사가 헛돌았다)"

	for t in $(comm -23 "$T"/cd_sch_mig "$T"/cd_sch_dump); do
		err "$SCHEMA_SQL 에 없는 테이블: $t — 재생성이 필요하다 (make schema)"
	done
	done_ "$SCHEMA_SQL 이 마이그레이션의 테이블 $(wc -l <"$T"/cd_sch_mig | tr -d ' ')개를 전부 담고 있다"
fi

rm -f "$T"/cd_rm_want "$T"/cd_rm_have "$T"/cd_def_req "$T"/cd_def_req_u "$T"/cd_use_req "$T"/cd_def_dec \
	"$T"/cd_use_dec "$T"/cd_def_mis "$T"/cd_use_mis "$T"/cd_stale \
	"$T"/cd_def_scr "$T"/cd_def_scr_u "$T"/cd_use_scr "$T"/cd_scr_issues \
	"$T"/cd_scr_bad "$T"/cd_scr_stateful "$T"/cd_perm_map "$T"/cd_perm_def \
	"$T"/cd_perm_used "$T"/cd_scr_perm "$T"/cd_perm_seed "$T"/cd_fr_cited \
	"$T"/cd_io_spec "$T"/cd_io_want "$T"/cd_io_have \
	"$T"/cd_open_inline "$T"/cd_open_cited "$T"/cd_open_def \
	"$T"/cd_wbs_cov "$T"/cd_wbs_done "$T"/cd_wbs_gap \
	"$T"/cd_sch_doc "$T"/cd_sch_sql "$T"/cd_sch_mig "$T"/cd_sch_dump \
	"$T"/cd_io_want_s "$T"/cd_io_have_s \
	"$T"/cd_fr_exempt "$T"/cd_fr_ok "$T"/cd_fr_must "$T"/cd_req_nocrit "$T"/cd_req_vague \
	"$T"/cd_tbl_d30 "$T"/cd_tbl_cov "$T"/cd_cov_blank \
	"$T"/cd_mod_all "$T"/cd_mod_shop "$T"/cd_mod_core \
	"$T"/cd_tpl_get "$T"/cd_tpl_named "$T"/cd_dep_mod "$T"/cd_dep_doc

if [ "$fail" -ne 0 ]; then
	printf '\ncheckdocs 실패 — 규칙은 docs/90-conventions.md\n'
	exit 1
fi
echo "checkdocs ok"
